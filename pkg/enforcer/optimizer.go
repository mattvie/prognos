// Package enforcer aplica as políticas obrigatórias sobre a Representação
// Intermediária antes que ela seja traduzida para um script .mry.
//
// O construtor da IR (pkg/ir) se preocupa apenas em mapear a semântica das rotas
// HTTP para uma topologia de Rede de Petri. Essa topologia, sozinha, não é segura
// para o motor do Mercury: filas abertas provocam explosão do espaço de estados,
// lugares de acumulação impedem a análise estacionária de convergir e a escolha
// errada do solver esgota a memória da JVM. Este módulo é quem impõe essas
// garantias, conforme documentado em docs/spn_mry_guidelines.md.
package enforcer

import (
	"fmt"
	"regexp"
	"strings"

	"prognos/pkg/ir"
)

// DefaultRouteCapacity é a capacidade K adotada quando a especificação não declara
// x-service-performance.route_capacity.
//
// O valor precisa ser baixo para manter a matriz da cadeia de Markov tratável em
// modelos com várias rotas (a recomendação das diretrizes é de 3 a 5 tokens).
const DefaultRouteCapacity = 3

// EffectiveRouteCapacity devolve a capacidade da rota que será efetivamente aplicada. Um
// valor ausente ou não positivo na especificação recai sobre o padrão, pois uma
// capacidade nula impediria qualquer requisição de ser admitida.
func EffectiveRouteCapacity(graph *ir.SPNGraph) int {
	if graph.RouteCapacity > 0 {
		return graph.RouteCapacity
	}
	return DefaultRouteCapacity
}

// Prefixos dos lugares gerados por rota. A convenção é definida em pkg/ir/builder.go,
// que é quem cria estes lugares; o Enforcer apenas os localiza para injetar as
// políticas. Qualquer renomeação no construtor da IR precisa ser refletida aqui.
const (
	prefixGen     = "P_Gen_"     // requisição recém-chegada, antes de disputar posição na rota
	prefixQueue   = "P_Queue_"   // fila de espera da rota
	prefixBusy    = "P_Busy_"    // processamento: P_Busy_<rota> é o local, P_Busy_<dep>_<rota> o da dependência
	prefixFree    = "P_Free_"    // posições livres da rota (criado por este módulo)
	prefixWaitDep = "P_Wait_"    // espera por uma posição do pool de um recurso externo
	prefixPoolDep = "P_Workers_" // pool global de um recurso externo
)

// stageSuffixRE casa o sufixo acrescentado aos estágios Erlang gerados por
// dependencies[].calls (_S2, _S3, ...). Ele vem depois do nome da rota, e por
// isso precisa ser removido antes de testar a que rota um lugar pertence.
var stageSuffixRE = regexp.MustCompile(`_S\d+$`)

// belongsToRoute informa se um lugar pertence à rota indicada.
func belongsToRoute(place, route string) bool {
	return strings.HasSuffix(stageSuffixRE.ReplaceAllString(place, ""), "_"+route)
}

// routePlaces reúne os lugares que uma requisição da rota ocupa entre a admissão
// na rota e a devolução da posição.
type routePlaces struct {
	queue   string   // fila de entrada
	busyApp string   // processamento local
	waitDep []string // espera por posição em cada recurso externo
	busyDep []string // atendimento por recurso externo, um por estágio Erlang
}

// collectRoutePlaces localiza esses lugares varrendo o grafo. A busca é feita
// pelos prefixos da nomenclatura definida em pkg/ir/builder.go, e não por uma
// lista fixa de dependências, porque nem toda rota declara todos os recursos e o
// número de estágios varia conforme o calls de cada dependência.
func collectRoutePlaces(graph *ir.SPNGraph, route string) routePlaces {
	rp := routePlaces{
		queue:   prefixQueue + route,
		busyApp: prefixBusy + route,
	}

	for _, p := range graph.Places {
		if !belongsToRoute(p.Name, route) {
			continue
		}
		switch {
		case p.Name == rp.busyApp:
			// Já contabilizado como processamento local. O teste é por igualdade,
			// e não por prefixo, porque os estágios das dependências começam com o
			// mesmo prefixo: P_Busy_<dep>_<rota>.
		case strings.HasPrefix(p.Name, prefixWaitDep):
			rp.waitDep = append(rp.waitDep, p.Name)
		case strings.HasPrefix(p.Name, prefixBusy):
			rp.busyDep = append(rp.busyDep, p.Name)
		}
	}

	return rp
}

// sumExpected monta a soma dos valores esperados dos lugares indicados. Somar
// termos E{#lugar} é a única forma de aritmética que o Mercury aceita dentro de
// uma métrica; qualquer operação sobre #lugar diretamente quebra o parser.
func sumExpected(places ...string) string {
	terms := make([]string, 0, len(places))
	for _, p := range places {
		terms = append(terms, "E{#"+p+"}")
	}
	return strings.Join(terms, " + ")
}

// OptimizeIR aplica, em ordem, todas as políticas obrigatórias do modelo.
//
// A ordem importa: as métricas de saturação dependem dos lugares de posições livres
// criados pela política de capacidade limitada, e a varredura de tokens órfãos precisa enxergar
// o grafo já com todos os arcos injetados.
func OptimizeIR(graph *ir.SPNGraph) (*ir.SPNGraph, error) {
	if graph == nil {
		return nil, fmt.Errorf("grafo SPN nulo recebido pelo enforcer")
	}

	if err := enforceBoundedQueues(graph); err != nil {
		return nil, err
	}
	enforceTokenDestruction(graph)
	injectSaturationMetrics(graph)
	selectSolverMethod(graph)

	return graph, nil
}

// enforceBoundedQueues impõe a regra da capacidade limitada.
//
// Para cada rota é criado um lugar de posições livres com K tokens. A transição
// de entrada passa a consumir uma posição junto com a requisição, a transição que
// devolve os recursos ao final da rota passa a devolver também a posição, e uma nova
// transição imediata de descarte, guiada por um arco inibidor, consome a
// requisição quando não há mais posições (o Drop / HTTP 503).
//
// Sem essa política a transição de chegada seria aberta e o espaço de estados da
// rede cresceria indefinidamente, travando o Mercury.
func enforceBoundedQueues(graph *ir.SPNGraph) error {
	k := EffectiveRouteCapacity(graph)

	for _, route := range graph.Routes {
		pGen := prefixGen + route
		pQueue := prefixQueue + route
		pBusy := prefixBusy + route
		pFree := prefixFree + route

		graph.Places = append(graph.Places, ir.Place{Name: pFree, Tokens: k})

		// A entrada da fila é a transição que deposita a requisição em P_Queue.
		entry := findTransition(graph, func(t *ir.Transition) bool {
			return contains(t.Inputs, pGen) && contains(t.Outputs, pQueue)
		})
		if entry == nil {
			return fmt.Errorf("rota %s: transição de entrada da fila não encontrada", route)
		}
		entry.Inputs = append(entry.Inputs, pFree)

		// A saída da rota é a transição que consome o lugar de processamento local.
		// É ela quem devolve a posição à rota, liberando espaço para a próxima
		// requisição apenas quando o processamento realmente termina.
		exit := findTransition(graph, func(t *ir.Transition) bool {
			return contains(t.Inputs, pBusy)
		})
		if exit == nil {
			return fmt.Errorf("rota %s: transição de saída do processamento não encontrada", route)
		}
		exit.Outputs = append(exit.Outputs, pFree)

		graph.Transitions = append(graph.Transitions, ir.Transition{
			Name:       "IT_Drop_" + route,
			IsTimed:    false,
			Inputs:     []string{pGen},
			Inhibitors: []string{pFree},
		})
	}

	return nil
}

// enforceTokenDestruction impõe a Regra da Destruição de Tokens.
//
// Um lugar que recebe tokens mas nunca é consumido por nenhuma transição é um
// ponto de acumulação morta (o clássico p_done). Ele faz a marcação da rede
// crescer para sempre e impede a existência de um estado estacionário. A política
// remove esses lugares e os arcos que apontam para eles, fazendo o token da
// requisição simplesmente desaparecer ao final do fluxo.
func enforceTokenDestruction(graph *ir.SPNGraph) {
	consumed := make(map[string]bool)
	for _, t := range graph.Transitions {
		for _, in := range t.Inputs {
			consumed[in] = true
		}
		for _, inh := range t.Inhibitors {
			consumed[inh] = true
		}
	}

	dead := make(map[string]bool)
	for _, t := range graph.Transitions {
		for _, out := range t.Outputs {
			if !consumed[out] {
				dead[out] = true
			}
		}
	}
	if len(dead) == 0 {
		return
	}

	for i := range graph.Transitions {
		graph.Transitions[i].Outputs = reject(graph.Transitions[i].Outputs, dead)
	}

	places := graph.Places[:0]
	for _, p := range graph.Places {
		if !dead[p.Name] {
			places = append(places, p)
		}
	}
	graph.Places = places
}

// injectSaturationMetrics adiciona as métricas derivadas que dependem da topologia
// imposta pelas políticas acima, e que por isso não podem ser geradas pelo
// construtor da IR.
//
//   - MSS: número médio de requisições no sistema, somando todos os lugares que a
//     requisição ocupa entre a admissão e a devolução da posição.
//   - Drop: probabilidade de a capacidade da rota estar esgotada, ou seja, de uma requisição ser
//     rejeitada. Só faz sentido porque a política de capacidade limitada existe.
//   - BusyApp, WaitDep e BusyDep: decomposição do MSS, que distingue o tempo gasto
//     na CPU da aplicação do tempo gasto esperando e usando um recurso externo.
//   - FreeDep: posições livres de cada pool externo, de onde sai a sua utilização.
func injectSaturationMetrics(graph *ir.SPNGraph) {
	// Número médio de instâncias livres. A utilização média do pool decorre dele,
	// e não da probabilidade de saturação: P{#P_Workers = 0} responde com que
	// frequência TODAS as instâncias estão ocupadas, o que só coincide com a
	// utilização quando existe uma única instância.
	graph.Metrics = append(graph.Metrics, ir.Metric{
		Name:       "Free_App",
		Expression: "E{#P_Workers}",
	})

	for _, route := range graph.Routes {
		rp := collectRoutePlaces(graph, route)

		// O MSS soma todos os lugares que a requisição ocupa enquanto retém a posição
		// da rota. Essa soma equivale a K menos o número médio de posições livres,
		// pois a posição é adquirida na admissão e devolvida somente ao final, mas a
		// forma somada é preferida por dispensar aritmética sobre a marcação.
		ocupacao := append([]string{rp.queue}, rp.waitDep...)
		ocupacao = append(ocupacao, rp.busyDep...)
		ocupacao = append(ocupacao, rp.busyApp)

		graph.Metrics = append(graph.Metrics,
			ir.Metric{
				Name:       "MSS_" + route,
				Expression: sumExpected(ocupacao...),
			},
			ir.Metric{
				Name:       "Drop_" + route,
				Expression: fmt.Sprintf("P{(#%s%s=0)}", prefixFree, route),
			},
			ir.Metric{
				Name:       "BusyApp_" + route,
				Expression: sumExpected(rp.busyApp),
			},
		)

		// As duas parcelas seguintes só existem para rotas que declaram algum
		// recurso externo. Emiti-las vazias produziria uma métrica sem expressão.
		if len(rp.waitDep) > 0 {
			graph.Metrics = append(graph.Metrics, ir.Metric{
				Name:       "WaitDep_" + route,
				Expression: sumExpected(rp.waitDep...),
			})
		}
		if len(rp.busyDep) > 0 {
			graph.Metrics = append(graph.Metrics, ir.Metric{
				Name:       "BusyDep_" + route,
				Expression: sumExpected(rp.busyDep...),
			})
		}
	}

	// Posições livres de cada pool externo. A utilização do recurso decorre dela,
	// da mesma forma que a do pool de instâncias decorre de Free_App.
	for _, dep := range graph.Dependencies {
		graph.Metrics = append(graph.Metrics, ir.Metric{
			Name:       "FreeDep_" + dep,
			Expression: sumExpected(prefixPoolDep + dep),
		})
	}
}

// selectSolverMethod escolhe o motor matemático usado na análise estacionária.
//
// O método direto inverte a matriz de estados e é exato, mas o consumo de memória
// explode conforme a rede cresce. A partir de duas rotas o modelo passa a ter
// concorrência entre subredes e a matriz fica grande demais, então usamos o método
// iterativo (Gauss-Seidel), que aproxima o resultado sem esgotar a RAM.
func selectSolverMethod(graph *ir.SPNGraph) {
	method := "direct"
	if len(graph.Routes) > 1 {
		method = "iterative"
	}

	for i := range graph.Metrics {
		// Uma métrica que já veio com um método explícito (ex: fornecida pelo
		// usuário) tem precedência sobre a política automática.
		if graph.Metrics[i].Method == "" {
			graph.Metrics[i].Method = method
		}
	}
}

func findTransition(graph *ir.SPNGraph, pred func(*ir.Transition) bool) *ir.Transition {
	for i := range graph.Transitions {
		if pred(&graph.Transitions[i]) {
			return &graph.Transitions[i]
		}
	}
	return nil
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func reject(list []string, blocked map[string]bool) []string {
	kept := make([]string, 0, len(list))
	for _, item := range list {
		if !blocked[item] {
			kept = append(kept, item)
		}
	}
	return kept
}
