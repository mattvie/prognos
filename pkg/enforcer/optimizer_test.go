package enforcer

import (
	"strings"
	"testing"

	"prognos/pkg/frontend"
	"prognos/pkg/ir"
	"prognos/pkg/semantic"
)

// buildGraph monta um grafo passando pelo pipeline real (parser -> validador ->
// construtor da IR), para que os testes do Enforcer operem sobre exatamente a
// mesma topologia que ele receberá em produção.
func buildGraph(t *testing.T, paths map[string]frontend.PathItem) *ir.SPNGraph {
	t.Helper()

	spec := &frontend.OpenAPISpec{
		Info:    frontend.Info{Title: "Teste", Version: "1.0.0"},
		Service: frontend.ServicePerformance{Instances: 1, WorkerType: "standard"},
		Paths:   paths,
	}

	valid, err := semantic.ValidateSpec(spec)
	if err != nil {
		t.Fatalf("validação falhou: %v", err)
	}

	graph, err := ir.BuildIR(valid)
	if err != nil {
		t.Fatalf("construção da IR falhou: %v", err)
	}
	return graph
}

func getRoute(processingMS int) frontend.PathItem {
	return frontend.PathItem{
		Get: &frontend.Operation{
			Summary: "rota de teste",
			Performance: &frontend.Performance{
				ProcessingTime: frontend.ProcessingTime{Distribution: "exponential", MeanMS: processingMS},
			},
		},
	}
}

func findPlace(graph *ir.SPNGraph, name string) *ir.Place {
	for i := range graph.Places {
		if graph.Places[i].Name == name {
			return &graph.Places[i]
		}
	}
	return nil
}

func findByName(graph *ir.SPNGraph, name string) *ir.Transition {
	for i := range graph.Transitions {
		if graph.Transitions[i].Name == name {
			return &graph.Transitions[i]
		}
	}
	return nil
}

// O construtor da IR não deve mais produzir nenhuma política: sem lugar de vagas,
// sem transição de descarte e sem método de solver escolhido.
func TestBuilderNaoAplicaPoliticas(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})

	if p := findPlace(graph, "P_Free_GET_export"); p != nil {
		t.Error("o construtor da IR criou P_Free_GET_export; isso é responsabilidade do Enforcer")
	}
	if tr := findByName(graph, "IT_Drop_GET_export"); tr != nil {
		t.Error("o construtor da IR criou a transição de descarte; isso é responsabilidade do Enforcer")
	}
	for _, m := range graph.Metrics {
		if m.Method != "" {
			t.Errorf("métrica %s já veio com method=%q do construtor da IR", m.Name, m.Method)
		}
	}
}

func TestFilaLimitadaInjetaBuffer(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	free := findPlace(graph, "P_Free_GET_export")
	if free == nil {
		t.Fatal("o Enforcer não criou o lugar de vagas livres P_Free_GET_export")
	}
	if free.Tokens != DefaultRouteCapacity {
		t.Errorf("P_Free_GET_export com %d tokens, esperado %d", free.Tokens, DefaultRouteCapacity)
	}

	// A entrada da fila precisa consumir uma vaga...
	entry := findByName(graph, "IT_Enter_GET_export")
	if entry == nil || !contains(entry.Inputs, "P_Free_GET_export") {
		t.Error("a transição de entrada não passou a consumir uma vaga do buffer")
	}

	// ...e o fim do processamento precisa devolvê-la, senão o buffer esvazia para
	// sempre e a rota trava depois de K requisições.
	exit := findByName(graph, "TT_Processing_GET_export")
	if exit == nil || !contains(exit.Outputs, "P_Free_GET_export") {
		t.Error("a transição de saída não devolve a vaga ao buffer")
	}

	// O descarte só pode disparar quando não há vagas: exige o arco inibidor.
	drop := findByName(graph, "IT_Drop_GET_export")
	if drop == nil {
		t.Fatal("o Enforcer não criou a transição de descarte")
	}
	if !contains(drop.Inhibitors, "P_Free_GET_export") {
		t.Error("a transição de descarte não possui arco inibidor sobre o buffer")
	}
	if drop.IsTimed {
		t.Error("a transição de descarte precisa ser imediata")
	}
}

// A chegada de tokens tem que ser fechada: toda transição que produz requisições
// deve ter, em algum ponto do caminho, um limite que impeça o crescimento infinito
// da marcação. Na prática isso significa que nenhuma rota pode ficar sem descarte.
func TestNenhumaRotaFicaSemDescarte(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{
		"/export": getRoute(250),
		"/ping":   getRoute(10),
	})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	for _, route := range graph.Routes {
		if findByName(graph, "IT_Drop_"+route) == nil {
			t.Errorf("rota %s ficou sem transição de descarte (fila aberta)", route)
		}
		if findPlace(graph, "P_Free_"+route) == nil {
			t.Errorf("rota %s ficou sem lugar de vagas livres", route)
		}
	}
}

func TestDestruicaoDeTokensRemoveLugarMorto(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})

	// Simula o antipadrão descrito nas diretrizes: um p_done que acumula tokens
	// e nunca é consumido por nenhuma transição.
	graph.Places = append(graph.Places, ir.Place{Name: "P_Done"})
	exit := findByName(graph, "TT_Processing_GET_export")
	exit.Outputs = append(exit.Outputs, "P_Done")

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	if findPlace(graph, "P_Done") != nil {
		t.Error("o lugar de acumulação morta P_Done não foi removido")
	}
	if contains(findByName(graph, "TT_Processing_GET_export").Outputs, "P_Done") {
		t.Error("o arco que alimentava P_Done não foi removido")
	}
}

// Contraprova do teste acima: nenhum lugar legítimo pode ser removido junto.
func TestDestruicaoDeTokensPreservaLugaresVivos(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	esperados := []string{
		"P_Workers", "P_Gen_GET_export", "P_Queue_GET_export",
		"P_Busy_GET_export", "P_Free_GET_export",
	}
	for _, nome := range esperados {
		if findPlace(graph, nome) == nil {
			t.Errorf("o lugar legítimo %s foi removido indevidamente", nome)
		}
	}
}

func TestSolverDiretoParaRotaUnica(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	for _, m := range graph.Metrics {
		if m.Method != "direct" {
			t.Errorf("métrica %s usa method=%q, esperado direct em modelo de rota única", m.Name, m.Method)
		}
	}
}

func TestSolverIterativoParaMultiplasRotas(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{
		"/export": getRoute(250),
		"/ping":   getRoute(10),
	})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	for _, m := range graph.Metrics {
		if m.Method != "iterative" {
			t.Errorf("métrica %s usa method=%q, esperado iterative em modelo multi-rota", m.Name, m.Method)
		}
	}
}

func TestSolverRespeitaMetodoExplicito(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})
	graph.Metrics = append(graph.Metrics, ir.Metric{
		Name: "Custom", Method: "iterative", Expression: "E{#P_Workers}",
	})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	for _, m := range graph.Metrics {
		if m.Name == "Custom" && m.Method != "iterative" {
			t.Errorf("o método explícito da métrica Custom foi sobrescrito por %q", m.Method)
		}
	}
}

func TestMetricasDeSaturacaoInjetadas(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	encontradas := make(map[string]string)
	for _, m := range graph.Metrics {
		encontradas[m.Name] = m.Expression
	}

	if _, ok := encontradas["MSS_GET_export"]; !ok {
		t.Error("métrica MSS não foi injetada")
	}
	drop, ok := encontradas["Drop_GET_export"]
	if !ok {
		t.Fatal("métrica Drop não foi injetada")
	}
	// A métrica de Drop só é válida porque a política de fila limitada criou o
	// lugar de vagas; se ela deixar de referenciá-lo, o Mercury não resolve.
	if !strings.Contains(drop, "P_Free_GET_export") {
		t.Errorf("métrica Drop não referencia o buffer da rota: %q", drop)
	}

	// O parser do Mercury quebra com '=='; a igualdade lógica usa um '=' só.
	for nome, expr := range encontradas {
		if strings.Contains(expr, "==") {
			t.Errorf("métrica %s usa '==', proibido pelo parser do Mercury: %q", nome, expr)
		}
	}
}

func TestOptimizeIRRejeitaGrafoNulo(t *testing.T) {
	if _, err := OptimizeIR(nil); err == nil {
		t.Error("OptimizeIR deveria falhar com grafo nulo")
	}
}

// Se a topologia esperada não existir, o Enforcer precisa falhar alto em vez de
// gerar um modelo com fila aberta que travaria o Mercury silenciosamente.
func TestEnforcerFalhaSeTopologiaInesperada(t *testing.T) {
	graph := &ir.SPNGraph{Routes: []string{"GET_fantasma"}}

	if _, err := OptimizeIR(graph); err == nil {
		t.Error("OptimizeIR deveria falhar quando a rota não possui a topologia esperada")
	}
}

// buildGraphComK monta o grafo declarando explicitamente o limite do buffer.
func buildGraphComK(t *testing.T, k int, paths map[string]frontend.PathItem) *ir.SPNGraph {
	t.Helper()

	spec := &frontend.OpenAPISpec{
		Info:    frontend.Info{Title: "Teste", Version: "1.0.0"},
		Service: frontend.ServicePerformance{Instances: 1, RouteCapacity: k},
		Paths:   paths,
	}

	valid, err := semantic.ValidateSpec(spec)
	if err != nil {
		t.Fatalf("validação falhou: %v", err)
	}
	graph, err := ir.BuildIR(valid)
	if err != nil {
		t.Fatalf("construção da IR falhou: %v", err)
	}
	return graph
}

func TestBufferRespeitaLimiteDeclarado(t *testing.T) {
	const k = 7
	graph := buildGraphComK(t, k, map[string]frontend.PathItem{"/export": getRoute(250)})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	free := findPlace(graph, "P_Free_GET_export")
	if free == nil {
		t.Fatal("lugar de vagas livres não foi criado")
	}
	if free.Tokens != k {
		t.Errorf("P_Free_GET_export com %d tokens, esperado o valor declarado %d", free.Tokens, k)
	}
}

// Omitir o campo, ou declará-lo com valor não positivo, precisa recair sobre o
// padrão: um buffer de capacidade nula impediria qualquer requisição de entrar.
func TestBufferRecaiNoPadraoQuandoNaoDeclarado(t *testing.T) {
	for _, k := range []int{0, -1} {
		graph := buildGraphComK(t, k, map[string]frontend.PathItem{"/export": getRoute(250)})

		if _, err := OptimizeIR(graph); err != nil {
			t.Fatalf("OptimizeIR retornou erro: %v", err)
		}

		free := findPlace(graph, "P_Free_GET_export")
		if free == nil || free.Tokens != DefaultRouteCapacity {
			t.Errorf("route_capacity=%d deveria recair no padrão %d, obtido %v", k, DefaultRouteCapacity, free)
		}
	}
}

// Todas as rotas do mesmo serviço compartilham o limite declarado, já que o campo
// pertence ao escopo global da especificação.
func TestLimiteDeclaradoValeParaTodasAsRotas(t *testing.T) {
	const k = 5
	graph := buildGraphComK(t, k, map[string]frontend.PathItem{
		"/export": getRoute(250),
		"/ping":   getRoute(10),
	})

	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}

	for _, rota := range graph.Routes {
		free := findPlace(graph, "P_Free_"+rota)
		if free == nil || free.Tokens != k {
			t.Errorf("rota %s: buffer %v, esperado %d tokens", rota, free, k)
		}
	}
}

// rotaComDependencia devolve uma rota que declara um recurso externo, com o
// número de chamadas indicado, para exercitar a decomposição do MSS.
func rotaComDependencia(processingMS, calls int) frontend.PathItem {
	return frontend.PathItem{
		Get: &frontend.Operation{
			Summary: "rota com recurso externo",
			Performance: &frontend.Performance{
				ProcessingTime: frontend.ProcessingTime{Distribution: "exponential", MeanMS: processingMS},
				Dependencies:   []frontend.Dependency{{Service: "postgresql-db", Calls: calls, MeanMS: 30}},
			},
		},
	}
}

func metricasDe(t *testing.T, graph *ir.SPNGraph) map[string]string {
	t.Helper()
	if _, err := OptimizeIR(graph); err != nil {
		t.Fatalf("OptimizeIR retornou erro: %v", err)
	}
	encontradas := make(map[string]string, len(graph.Metrics))
	for _, m := range graph.Metrics {
		encontradas[m.Name] = m.Expression
	}
	return encontradas
}

// O MSS precisa somar todos os lugares que a requisição ocupa enquanto retém a
// vaga do buffer. Contar apenas fila e CPU local subestimaria sistematicamente as
// rotas que conversam com um recurso externo.
func TestMSSIncluiLugaresDaDependencia(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/relatorio": rotaComDependencia(40, 1)})
	mss := metricasDe(t, graph)["MSS_GET_relatorio"]

	for _, lugar := range []string{
		"P_Queue_GET_relatorio",
		"P_Wait_postgresql_db_GET_relatorio",
		"P_Busy_postgresql_db_GET_relatorio",
		"P_Busy_GET_relatorio",
	} {
		if !strings.Contains(mss, "E{#"+lugar+"}") {
			t.Errorf("MSS não contabiliza o lugar %s: %q", lugar, mss)
		}
	}
}

// Com calls > 1 o construtor encadeia estágios Erlang, e o tempo gasto em cada um
// deles também é tempo de residência. Nenhum estágio pode ficar de fora.
func TestMSSContabilizaTodosOsEstagiosErlang(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/escrita": rotaComDependencia(40, 3)})
	mss := metricasDe(t, graph)["MSS_GET_escrita"]

	for _, lugar := range []string{
		"P_Busy_postgresql_db_GET_escrita",
		"P_Busy_postgresql_db_GET_escrita_S2",
		"P_Busy_postgresql_db_GET_escrita_S3",
	} {
		if !strings.Contains(mss, "E{#"+lugar+"}") {
			t.Errorf("MSS não contabiliza o estágio %s: %q", lugar, mss)
		}
	}
}

// Uma rota sem recurso externo não pode ter o seu MSS alterado por esta mudança:
// é o que preserva as tabelas de validação analítica da monografia.
func TestMSSDeRotaSemDependenciaPermaneceFilaMaisCPU(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})
	mss := metricasDe(t, graph)["MSS_GET_export"]

	esperado := "E{#P_Queue_GET_export} + E{#P_Busy_GET_export}"
	if mss != esperado {
		t.Errorf("MSS de rota simples mudou:\n  obtido:   %q\n  esperado: %q", mss, esperado)
	}
}

func TestDecomposicaoDoMSSInjetada(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/relatorio": rotaComDependencia(40, 2)})
	metricas := metricasDe(t, graph)

	casos := map[string]string{
		"BusyApp_GET_relatorio": "P_Busy_GET_relatorio",
		"WaitDep_GET_relatorio": "P_Wait_postgresql_db_GET_relatorio",
		"BusyDep_GET_relatorio": "P_Busy_postgresql_db_GET_relatorio_S2",
	}
	for nome, lugar := range casos {
		expr, ok := metricas[nome]
		if !ok {
			t.Errorf("métrica %s não foi injetada", nome)
			continue
		}
		if !strings.Contains(expr, "E{#"+lugar+"}") {
			t.Errorf("métrica %s não referencia %s: %q", nome, lugar, expr)
		}
	}

	// A utilização do pool externo decorre do número médio de posições livres.
	if expr, ok := metricas["FreeDep_postgresql_db"]; !ok {
		t.Error("métrica FreeDep do recurso externo não foi injetada")
	} else if !strings.Contains(expr, "E{#P_Workers_postgresql_db}") {
		t.Errorf("FreeDep não referencia o pool do recurso: %q", expr)
	}
}

// Rota sem recurso externo não deve receber métricas de dependência: elas ficariam
// com expressão vazia e o Mercury não resolveria o modelo.
func TestRotaSemDependenciaNaoRecebeMetricasDeDependencia(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{"/export": getRoute(250)})
	metricas := metricasDe(t, graph)

	for _, nome := range []string{"WaitDep_GET_export", "BusyDep_GET_export"} {
		if _, ok := metricas[nome]; ok {
			t.Errorf("métrica %s não deveria existir para rota sem dependência", nome)
		}
	}
	if _, ok := metricas["BusyApp_GET_export"]; !ok {
		t.Error("BusyApp deve ser emitida mesmo sem dependência")
	}
}

// A varredura que localiza os lugares de uma rota precisa distinguir rotas cujo
// nome é sufixo do de outra, situação que o achatamento caminho/método produz.
func TestDecomposicaoNaoConfundeRotasDeNomeParecido(t *testing.T) {
	graph := buildGraph(t, map[string]frontend.PathItem{
		"/appointments":    rotaComDependencia(25, 1),
		"/v1/appointments": rotaComDependencia(40, 2),
	})
	metricas := metricasDe(t, graph)

	curta := metricas["MSS_GET_appointments"]
	if strings.Contains(curta, "v1_appointments") {
		t.Errorf("MSS da rota curta absorveu lugares da rota longa: %q", curta)
	}
	longa := metricas["MSS_GET_v1_appointments"]
	if !strings.Contains(longa, "P_Busy_postgresql_db_GET_v1_appointments_S2") {
		t.Errorf("MSS da rota longa não contabiliza o seu segundo estágio: %q", longa)
	}
}
