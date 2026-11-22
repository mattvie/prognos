package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"prognos/pkg/backend"
	"prognos/pkg/enforcer"
	"prognos/pkg/frontend"
	"prognos/pkg/ir"
	"prognos/pkg/orchestrator"
	"prognos/pkg/semantic"
)

func main() {
	fmt.Println("=====================================================")
	fmt.Println("  Transpilador OpenAPI -> Mercury SPN | Inicializado ")
	fmt.Println("=====================================================")

	inputYAML := "samples/openapi.yaml"

	// If the user provided an argument, override the default
	if len(os.Args) > 1 {
		inputYAML = os.Args[1]
	}

	fmt.Printf("[0/6] Arquivo alvo configurado: %s\n", inputYAML)

	fmt.Println("[1/6] Executando Parser do OpenAPI...")
	rawSpec, err := frontend.ParseOpenAPI(inputYAML)
	if err != nil {
		log.Fatalf("Erro no Frontend: %v", err)
	}
	fmt.Printf("   -> Sucesso! API Carregada: %s (v%s)\n", rawSpec.Info.Title, rawSpec.Info.Version)
	fmt.Printf("   -> Instâncias do Serviço : %d\n", rawSpec.Service.Instances)

	fmt.Println("[2/6] Executando Análise Semântica e Validação...")
	validSpec, err := semantic.ValidateSpec(rawSpec)
	if err != nil {
		log.Fatalf("Erro na Análise Semântica: %v", err)
	}
	fmt.Printf("   -> Rotas Validadas para Simulação: %d\n", len(validSpec.Routes))
	fmt.Printf("   -> Dependências Externas Encontradas: %v\n", validSpec.Dependencies)

	fmt.Println("[3/6] Construindo Representação Intermédia (Grafo SPN)...")
	spnGraph, err := ir.BuildIR(validSpec)
	if err != nil {
		log.Fatalf("Erro ao construir IR: %v", err)
	}
	fmt.Printf("   -> Lugares (Places) Criados      : %d\n", len(spnGraph.Places))
	fmt.Printf("   -> Transições (Transitions) Criadas: %d\n", len(spnGraph.Transitions))
	fmt.Printf("   -> Métricas (Metrics) Geradas    : %d\n", len(spnGraph.Metrics))

	fmt.Println("[4/6] Aplicando Políticas de Otimização...")
	optimizedGraph, err := enforcer.OptimizeIR(spnGraph)
	if err != nil {
		log.Fatalf("Erro ao otimizar IR: %v", err)
	}
	// O Enforcer injeta a capacidade limitada das rotas, as transições de descarte e as
	// métricas de saturação, por isso os totais crescem em relação à etapa [3/6].
	fmt.Printf("   -> Capacidade por Rota (K)         : %d\n", enforcer.EffectiveRouteCapacity(optimizedGraph))
	fmt.Printf("   -> Lugares (Places) Totais         : %d\n", len(optimizedGraph.Places))
	fmt.Printf("   -> Transições (Transitions) Totais : %d\n", len(optimizedGraph.Transitions))
	fmt.Printf("   -> Métricas (Metrics) Totais       : %d\n", len(optimizedGraph.Metrics))

	fmt.Println("[5/6] Gerando Código do Script Mercury (.mry)...")
	mryScript, err := backend.GenerateMRY(optimizedGraph)
	if err != nil {
		log.Fatalf("Erro na Geração de Código: %v", err)
	}
	// Salva o texto gerado em um arquivo físico para podermos inspecionar
	err = os.WriteFile("modelo_gerado.mry", []byte(mryScript), 0644)
	if err != nil {
		log.Fatalf("Erro ao salvar o arquivo: %v", err)
	}
	fmt.Println("   -> Script salvo com sucesso em 'modelo_gerado.mry'!")
	fmt.Println("   -> Gerando visualização em Grafo (Graphviz)...")
	dotGraph, err := backend.GenerateDOT(optimizedGraph)
	if err != nil {
		log.Fatalf("Erro ao gerar DOT: %v", err)
	}
	err = os.WriteFile("modelo_visual.dot", []byte(dotGraph), 0644)
	if err != nil {
		log.Fatalf("Erro ao salvar arquivo DOT: %v", err)
	}
	fmt.Println("   -> Grafo salvo com sucesso em 'modelo_visual.dot'!")

	fmt.Println("[6/6] Invocando CLI do Mercury...")
	results, err := orchestrator.ExecuteMercury(mryScript)
	if err != nil {
		log.Fatalf("Erro na Execução da Simulação: %v", err)
	}

	fmt.Println("\n=====================================================")
	fmt.Println("         RELATORIO DE DESEMPENHO DA API              ")
	fmt.Println("=====================================================")

	// 1. Exibe primeiramente os indicadores globais de Infraestrutura.
	//
	// A utilizacao media do pool e a fracao das instancias que esta ocupada, e
	// decorre do numero medio de instancias livres. Ja a saturacao e a
	// probabilidade de TODAS estarem ocupadas ao mesmo tempo; as duas coincidem
	// apenas quando existe uma unica instancia.
	for metricName, value := range results.Outputs {
		if strings.HasPrefix(metricName, "util_") {
			fmt.Printf("   [CPU]  Utilizacao (0 a 1)              : %.4f\n", value)
		}
	}
	instancias := float64(rawSpec.Service.Instances)
	if livres, ok := results.Outputs["free_app"]; ok && instancias > 0 {
		fmt.Printf("   [CPU]  Utilizacao media do pool (0 a 1): %.4f\n", (instancias-livres)/instancias)
	}

	// Utilizacao de cada recurso externo, obtida da mesma forma: a capacidade
	// declarada menos o numero medio de posicoes livres, dividido pela capacidade.
	deps := append([]string(nil), optimizedGraph.Dependencies...)
	sort.Strings(deps)
	for _, dep := range deps {
		livres, ok := results.Outputs["freedep_"+strings.ToLower(dep)]
		if !ok {
			continue
		}
		capacidade := 0.0
		for _, p := range optimizedGraph.Places {
			if p.Name == "P_Workers_"+dep {
				capacidade = float64(p.Tokens)
			}
		}
		if capacidade > 0 {
			fmt.Printf("   [DEP]  %-31s: %.4f\n", dep, (capacidade-livres)/capacidade)
		}
	}

	// 2. Mapeia as rotas dinamicamente a partir das chaves de fila existentes.
	// A ordenacao e necessaria porque a varredura sobre um mapa em Go nao tem
	// ordem definida: sem ela, duas execucoes da mesma especificacao imprimiriam
	// os blocos de rota em ordens diferentes.
	var routes []string
	for metricName := range results.Outputs {
		if strings.HasPrefix(metricName, "q_") {
			routes = append(routes, strings.TrimPrefix(metricName, "q_"))
		}
	}
	sort.Strings(routes)

	// As taxas de chegada declaradas sao indexadas pelo nome sanitizado da rota,
	// enquanto o orquestrador normaliza as chaves das metricas para minusculas.
	taxas := make(map[string]float64, len(optimizedGraph.ArrivalRates))
	for rota, taxa := range optimizedGraph.ArrivalRates {
		taxas[strings.ToLower(rota)] = taxa
	}

	// 3. Imprime o Raio-X agrupado por Rota seguindo o padrao MQS, MSS e Drop
	for _, route := range routes {
		fmt.Printf("\n   [ROTA] %s\n", route)

		qVal, temQ := results.Outputs["q_"+route]
		if temQ {
			fmt.Printf("     -> Req. na Fila (MQS)          : %.4f reqs\n", qVal)
		}
		mssVal, temMSS := results.Outputs["mss_"+route]
		if temMSS {
			fmt.Printf("     -> Total no Sistema (MSS)      : %.4f reqs\n", mssVal)
		}
		dropVal, temDrop := results.Outputs["drop_"+route]
		if temDrop {
			fmt.Printf("     -> Probabilidade de Drop (Drop): %.4f (%.2f%%)\n", dropVal, dropVal*100)
		}

		// A vazao efetiva desconta da carga declarada a fracao rejeitada na
		// entrada. Dela decorrem, pela Lei de Little, o tempo medio de resposta e
		// o tempo medio de espera na fila.
		lambda, temTaxa := taxas[route]
		if temTaxa && temDrop {
			tp := lambda * (1 - dropVal)
			fmt.Printf("     -> Vazao Efetiva (TP)          : %.4f reqs/s\n", tp)
			if tp > 0 {
				if temMSS {
					fmt.Printf("     -> Tempo de Resposta (MRT)     : %.4f s\n", mssVal/tp)
				}
				if temQ {
					fmt.Printf("     -> Tempo de Espera (MWT)       : %.4f s\n", qVal/tp)
				}
			}
		}

		// Decomposicao do MSS: onde a requisicao passa o seu tempo de residencia.
		if v, ok := results.Outputs["busyapp_"+route]; ok {
			fmt.Printf("     -> Em processamento            : %.4f reqs\n", v)
		}
		if v, ok := results.Outputs["waitdep_"+route]; ok {
			fmt.Printf("     -> Espera por Dependencia      : %.4f reqs\n", v)
		}
		if v, ok := results.Outputs["busydep_"+route]; ok {
			fmt.Printf("     -> Dentro da Dependencia       : %.4f reqs\n", v)
		}
	}
	fmt.Println("=====================================================")
}
