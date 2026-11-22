package ir

import (
	"fmt"
	"prognos/pkg/semantic"
	"strings"
)

const (
	// DefaultDependencyPool é a capacidade adotada por um recurso externo que não
	// tenha a sua declarada em x-service-performance.dependency_pools.
	DefaultDependencyPool = 5

	// DefaultCallMS é o custo, em milissegundos, de uma chamada a um recurso
	// externo cuja dependência não declare mean_ms.
	DefaultCallMS = 10
)

func sanitizeRouteName(method, path string) string {
	// Mercury identifiers cannot have spaces or special chars like '/'.
	// We replace them with underscores.
	cleanPath := strings.ReplaceAll(path, "/", "_")
	cleanPath = strings.ReplaceAll(cleanPath, "{", "")
	cleanPath = strings.ReplaceAll(cleanPath, "}", "")
	// Remove leading "_" if path was "/"
	if strings.HasPrefix(cleanPath, "_") {
		cleanPath = cleanPath[1:]
	}
	return strings.ToUpper(method) + "_" + cleanPath
}

// BuildIR traduz os conceitos de rotas HTTP para nós e arcos.
//
// Este módulo faz apenas o mapeamento semântico (rota -> topologia). As políticas
// de segurança do modelo (capacidade limitada, destruição de tokens, escolha do solver e
// métricas derivadas) são responsabilidade do pacote enforcer, aplicado logo depois.
func BuildIR(valid *semantic.ValidSpec) (*SPNGraph, error) {
	graph := &SPNGraph{
		Places:       make([]Place, 0),
		Transitions:  make([]Transition, 0),
		Metrics:      make([]Metric, 0),
		Routes:       make([]string, 0),
		ArrivalRates: make(map[string]float64),
	}

	// A configuração declarada é repassada adiante para que as políticas do
	// enforcer possam consultá-la sem precisar reabrir a especificação.
	graph.RouteCapacity = valid.RawSpec.Service.RouteCapacity

	// 1. RECURSOS GLOBAIS
	graph.Places = append(graph.Places, Place{Name: "P_Workers", Tokens: valid.RawSpec.Service.Instances})

	for _, dep := range valid.Dependencies {
		safeDep := strings.ReplaceAll(dep, "-", "_")
		graph.Dependencies = append(graph.Dependencies, safeDep)

		// A capacidade do recurso é declarada em x-service-performance.dependency_pools,
		// indexada pelo nome original da dependência. Ausente ou não positiva, adota-se
		// o padrão.
		capacidade := DefaultDependencyPool
		if declarada, ok := valid.RawSpec.Service.DependencyPools[dep]; ok && declarada > 0 {
			capacidade = declarada
		}
		graph.Places = append(graph.Places, Place{Name: "P_Workers_" + safeDep, Tokens: capacidade})
	}

	// 2. CONSTRUÇÃO DAS ROTAS
	for _, route := range valid.Routes {
		rName := sanitizeRouteName(route.Method, route.Path)
		graph.Routes = append(graph.Routes, rName)

		delaySec := float64(route.Performance.ProcessingTime.MeanMS) / 1000.0

		// A taxa chega em requisições por segundo; o Mercury espera o intervalo
		// médio entre chegadas, que é o seu inverso. A taxa original é preservada
		// no grafo porque o inverso, sozinho, não permite recuperá-la no relatório.
		arrivalRate := 1.0 / route.ArrivalRate
		graph.ArrivalRates[rName] = route.ArrivalRate

		pGen := "P_Gen_" + rName
		pIn := "P_Queue_" + rName
		pBusy := "P_Busy_" + rName

		graph.Places = append(graph.Places,
			Place{Name: pGen, Tokens: 0},
			Place{Name: pIn, Tokens: 0},
			Place{Name: pBusy, Tokens: 0},
		)

		graph.Transitions = append(graph.Transitions,
			Transition{Name: "TT_Arrival_" + rName, IsTimed: true, DelayValue: arrivalRate, Outputs: []string{pGen}},
			Transition{Name: "IT_Enter_" + rName, IsTimed: false, Inputs: []string{pGen}, Outputs: []string{pIn}},
		)

		// Se tiver dependências, criamos uma cadeia (Chain) de processamento síncrono
		if len(route.Performance.Dependencies) > 0 {
			// A. O Token entra na rota e pega a Thread do Go
			firstDepSafe := strings.ReplaceAll(route.Performance.Dependencies[0].Service, "-", "_")
			pWaitFirst := "P_Wait_" + firstDepSafe + "_" + rName
			graph.Transitions = append(graph.Transitions, Transition{
				Name:    "IT_Start_" + rName,
				IsTimed: false,
				Inputs:  []string{pIn, "P_Workers"},
				Outputs: []string{pWaitFirst},
			})

			// B. Loop para encadear todas as dependências sequencialmente
			for i, dep := range route.Performance.Dependencies {
				safeDep := strings.ReplaceAll(dep.Service, "-", "_")
				pWaitDB := "P_Wait_" + safeDep + "_" + rName
				pBusyDB := "P_Busy_" + safeDep + "_" + rName
				pPoolDB := "P_Workers_" + safeDep

				// Custo de uma chamada a este recurso, declarado pela própria rota.
				callMS := dep.MeanMS
				if callMS <= 0 {
					callMS = DefaultCallMS
				}
				delayDB := float64(callMS) / 1000.0

				graph.Places = append(graph.Places, Place{Name: pWaitDB}, Place{Name: pBusyDB})

				// Descobre para onde o token vai depois dessa dependência terminar
				var nextPlace string
				if i < len(route.Performance.Dependencies)-1 {
					nextDepSafe := strings.ReplaceAll(route.Performance.Dependencies[i+1].Service, "-", "_")
					nextPlace = "P_Wait_" + nextDepSafe + "_" + rName
				} else {
					nextPlace = pBusy // Se for a última dep, vai para a CPU final da rota
				}

				// O token adquire uma posição do pool uma única vez e a mantém
				// durante toda a conversa com a dependência.
				graph.Transitions = append(graph.Transitions, Transition{
					Name: "IT_Start_" + safeDep + "_" + rName, IsTimed: false,
					Inputs: []string{pWaitDB, pPoolDB}, Outputs: []string{pBusyDB},
				})

				// Cada chamada declarada em 'calls' vira um estágio exponencial em
				// série. N estágios de média igual formam uma distribuição Erlang-N,
				// que preserva a média total mas reduz a variância na proporção 1/N,
				// refletindo que N idas e voltas à dependência flutuam menos que uma
				// única espera exponencial de mesma duração. A rede permanece
				// markoviana, o que mantém a análise estacionária viável.
				calls := dep.Calls
				if calls < 1 {
					calls = 1
				}
				for stage := 1; stage <= calls; stage++ {
					stageSuffix := ""
					if stage > 1 {
						stageSuffix = fmt.Sprintf("_S%d", stage)
					}

					// A saída do estágio é o próximo estágio; a do último é a
					// continuação da rota, devolvendo a posição do pool.
					stageOutputs := []string{nextPlace, pPoolDB}
					if stage < calls {
						pNextStage := fmt.Sprintf("%s_S%d", pBusyDB, stage+1)
						graph.Places = append(graph.Places, Place{Name: pNextStage})
						stageOutputs = []string{pNextStage}
					}

					graph.Transitions = append(graph.Transitions, Transition{
						Name:           "TT_Processing_" + safeDep + "_" + rName + stageSuffix,
						IsTimed:        true,
						DelayValue:     delayDB,
						Inputs:         []string{pBusyDB + stageSuffix},
						Outputs:        stageOutputs,
						InfiniteServer: true,
					})
				}
			}

			// C. Termina o processamento na CPU do Go e destrói token
			graph.Transitions = append(graph.Transitions, Transition{
				Name:           "TT_Processing_" + rName,
				IsTimed:        true,
				DelayValue:     delaySec,
				Inputs:         []string{pBusy},
				Outputs:        []string{"P_Workers"},
				InfiniteServer: true,
			})
		} else {
			// Rota Simples sem dependências
			graph.Transitions = append(graph.Transitions,
				Transition{Name: "IT_Start_" + rName, IsTimed: false, Inputs: []string{pIn, "P_Workers"}, Outputs: []string{pBusy}},
				Transition{Name: "TT_Processing_" + rName, IsTimed: true, DelayValue: delaySec, Inputs: []string{pBusy}, Outputs: []string{"P_Workers"}, InfiniteServer: true},
			)
		}

		// 3. MÉTRICAS DA ROTA
		// O campo Method fica vazio de propósito: a escolha do motor matemático
		// (direct/iterative) é uma política aplicada pelo Enforcer.
		graph.Metrics = append(graph.Metrics, Metric{
			Name:       "Q_" + rName,
			Expression: fmt.Sprintf("E{#%s}", pIn),
		})
	}

	graph.Metrics = append(graph.Metrics, Metric{
		Name:       "Util_App",
		Expression: "P{(#P_Workers=0)}",
	})

	return graph, nil
}
