package ir

import (
	"testing"

	"prognos/pkg/frontend"
	"prognos/pkg/semantic"
)

func rotaComDependencia(calls int) frontend.PathItem {
	return frontend.PathItem{
		Post: &frontend.Operation{
			Summary: "rota de teste",
			Performance: &frontend.Performance{
				ProcessingTime: frontend.ProcessingTime{Distribution: "exponential", MeanMS: 85},
				Dependencies:   []frontend.Dependency{{Service: "postgresql-db", Calls: calls}},
			},
		},
	}
}

func construir(t *testing.T, instances int, paths map[string]frontend.PathItem) *SPNGraph {
	t.Helper()

	valid, err := semantic.ValidateSpec(&frontend.OpenAPISpec{
		Info:    frontend.Info{Title: "Teste", Version: "1.0.0"},
		Service: frontend.ServicePerformance{Instances: instances},
		Paths:   paths,
	})
	if err != nil {
		t.Fatalf("validação falhou: %v", err)
	}

	graph, err := BuildIR(valid)
	if err != nil {
		t.Fatalf("construção da IR falhou: %v", err)
	}
	return graph
}

func transicao(graph *SPNGraph, name string) *Transition {
	for i := range graph.Transitions {
		if graph.Transitions[i].Name == name {
			return &graph.Transitions[i]
		}
	}
	return nil
}

// O Mercury assume SingleServer por omissão, o que serializaria o atendimento e
// tornaria o número de instâncias irrelevante para a vazão do sistema.
func TestAtendimentoUsaServidorInfinito(t *testing.T) {
	graph := construir(t, 4, map[string]frontend.PathItem{"/pedidos": rotaComDependencia(1)})

	servico := transicao(graph, "TT_Processing_POST_pedidos")
	if servico == nil {
		t.Fatal("transição de processamento não encontrada")
	}
	if !servico.InfiniteServer {
		t.Error("o processamento da rota precisa ter semântica de servidor infinito, senão o pool de instâncias não aumenta a vazão")
	}

	dep := transicao(graph, "TT_Processing_postgresql_db_POST_pedidos")
	if dep == nil {
		t.Fatal("transição da dependência não encontrada")
	}
	if !dep.InfiniteServer {
		t.Error("o atendimento da dependência precisa ter semântica de servidor infinito")
	}
}

// A chegada é uma fonte de Poisson sem lugar de entrada: o grau de habilitação
// não faz sentido ali, e ela deve permanecer com a semântica padrão.
func TestChegadaNaoUsaServidorInfinito(t *testing.T) {
	graph := construir(t, 1, map[string]frontend.PathItem{"/pedidos": rotaComDependencia(1)})

	chegada := transicao(graph, "TT_Arrival_POST_pedidos")
	if chegada == nil {
		t.Fatal("transição de chegada não encontrada")
	}
	if chegada.InfiniteServer {
		t.Error("a transição de chegada não deve usar semântica de servidor infinito")
	}
}

// Cada chamada declarada vira um estágio exponencial em série, formando uma
// Erlang-N: mesma média que uma única espera de N vezes a duração, porém com a
// variância correta.
func TestCallsGeraEstagiosEmSerie(t *testing.T) {
	const calls = 3
	graph := construir(t, 1, map[string]frontend.PathItem{"/pedidos": rotaComDependencia(calls)})

	base := "TT_Processing_postgresql_db_POST_pedidos"
	nomes := []string{base, base + "_S2", base + "_S3"}

	var somaDelays float64
	for _, nome := range nomes {
		tr := transicao(graph, nome)
		if tr == nil {
			t.Fatalf("estágio %s não foi gerado", nome)
		}
		somaDelays += tr.DelayValue
	}

	if transicao(graph, base+"_S4") != nil {
		t.Error("foi gerado um estágio a mais do que o número de chamadas declaradas")
	}

	// A média total precisa ser preservada em relação ao modelo de estágio único.
	if esperado := 0.010 * calls; somaDelays < esperado-1e-9 || somaDelays > esperado+1e-9 {
		t.Errorf("soma dos atrasos dos estágios = %v, esperado %v", somaDelays, esperado)
	}

	// Os estágios precisam estar encadeados, e só o último devolve o pool.
	primeiro := transicao(graph, base)
	if len(primeiro.Outputs) != 1 || primeiro.Outputs[0] != "P_Busy_postgresql_db_POST_pedidos_S2" {
		t.Errorf("o primeiro estágio não encadeia no segundo: %v", primeiro.Outputs)
	}
	ultimo := transicao(graph, base+"_S3")
	if !contemString(ultimo.Outputs, "P_Workers_postgresql_db") {
		t.Errorf("o último estágio não devolve a posição do pool: %v", ultimo.Outputs)
	}
	for _, nome := range nomes[:len(nomes)-1] {
		if contemString(transicao(graph, nome).Outputs, "P_Workers_postgresql_db") {
			t.Errorf("o estágio %s devolve o pool antes do fim da conversa com a dependência", nome)
		}
	}
}

// Com uma única chamada a topologia precisa continuar idêntica à anterior, para
// que os modelos já validados na monografia não mudem.
func TestCallsUnitarioNaoCriaEstagioExtra(t *testing.T) {
	graph := construir(t, 1, map[string]frontend.PathItem{"/pedidos": rotaComDependencia(1)})

	base := "TT_Processing_postgresql_db_POST_pedidos"
	if transicao(graph, base) == nil {
		t.Fatal("o estágio único não foi gerado")
	}
	if transicao(graph, base+"_S2") != nil {
		t.Error("uma única chamada não deve gerar estágios adicionais")
	}
	for _, p := range graph.Places {
		if p.Name == "P_Busy_postgresql_db_POST_pedidos_S2" {
			t.Error("uma única chamada não deve criar lugares de estágio adicionais")
		}
	}
}

func contemString(list []string, alvo string) bool {
	for _, item := range list {
		if item == alvo {
			return true
		}
	}
	return false
}

// O construtor da IR não aplica o limite do buffer, mas precisa repassá-lo para
// que o enforcer possa consultá-lo.
func TestIRRepassaLimiteDeBufferDeclarado(t *testing.T) {
	valid, err := semantic.ValidateSpec(&frontend.OpenAPISpec{
		Info:    frontend.Info{Title: "Teste", Version: "1.0.0"},
		Service: frontend.ServicePerformance{Instances: 1, RouteCapacity: 9},
		Paths:   map[string]frontend.PathItem{"/pedidos": rotaComDependencia(1)},
	})
	if err != nil {
		t.Fatalf("validação falhou: %v", err)
	}

	graph, err := BuildIR(valid)
	if err != nil {
		t.Fatalf("construção da IR falhou: %v", err)
	}

	if graph.RouteCapacity != 9 {
		t.Errorf("RouteCapacity = %d, esperado 9", graph.RouteCapacity)
	}
	if findPlaceIR(graph, "P_Free_POST_pedidos") != nil {
		t.Error("o construtor da IR não deve criar o lugar de vagas livres")
	}
}

func findPlaceIR(graph *SPNGraph, name string) *Place {
	for i := range graph.Places {
		if graph.Places[i].Name == name {
			return &graph.Places[i]
		}
	}
	return nil
}

// A taxa declarada em requisições por segundo vira o intervalo médio entre
// chegadas, que é o seu inverso.
func TestTaxaDeChegadaDeclarada(t *testing.T) {
	rota := frontend.PathItem{Post: &frontend.Operation{
		Performance: &frontend.Performance{
			ProcessingTime: frontend.ProcessingTime{MeanMS: 50},
			ArrivalRate:    10, // 10 req/s -> 0,1 s entre chegadas
		},
	}}
	valid, err := semantic.ValidateSpec(&frontend.OpenAPISpec{
		Service: frontend.ServicePerformance{Instances: 1},
		Paths:   map[string]frontend.PathItem{"/pedidos": rota},
	})
	if err != nil {
		t.Fatalf("validação falhou: %v", err)
	}
	graph, err := BuildIR(valid)
	if err != nil {
		t.Fatalf("construção falhou: %v", err)
	}

	chegada := transicao(graph, "TT_Arrival_POST_pedidos")
	if chegada == nil {
		t.Fatal("transição de chegada não encontrada")
	}
	if d := chegada.DelayValue; d < 0.1-1e-9 || d > 0.1+1e-9 {
		t.Errorf("atraso de chegada = %v, esperado 0.1", d)
	}
}

// Rotas distintas precisam poder declarar cargas distintas: é essa diferença que
// caracteriza o perfil de tráfego de um sistema real.
func TestTaxasDistintasPorRota(t *testing.T) {
	nova := func(ms int, taxa float64) frontend.PathItem {
		return frontend.PathItem{Get: &frontend.Operation{
			Performance: &frontend.Performance{
				ProcessingTime: frontend.ProcessingTime{MeanMS: ms},
				ArrivalRate:    taxa,
			},
		}}
	}
	valid, _ := semantic.ValidateSpec(&frontend.OpenAPISpec{
		Service: frontend.ServicePerformance{Instances: 2},
		Paths: map[string]frontend.PathItem{
			"/rapida": nova(10, 20),
			"/lenta":  nova(80, 2),
		},
	})
	graph, err := BuildIR(valid)
	if err != nil {
		t.Fatalf("construção falhou: %v", err)
	}

	esperado := map[string]float64{
		"TT_Arrival_GET_rapida": 0.05,
		"TT_Arrival_GET_lenta":  0.5,
	}
	for nome, quero := range esperado {
		tr := transicao(graph, nome)
		if tr == nil {
			t.Fatalf("%s não encontrada", nome)
		}
		if tr.DelayValue < quero-1e-9 || tr.DelayValue > quero+1e-9 {
			t.Errorf("%s com atraso %v, esperado %v", nome, tr.DelayValue, quero)
		}
	}
}

// A capacidade do recurso externo é declarada globalmente, pois o pool pertence
// ao recurso e não à rota que o consome.
func TestCapacidadeDoPoolDeclarada(t *testing.T) {
	valid, _ := semantic.ValidateSpec(&frontend.OpenAPISpec{
		Service: frontend.ServicePerformance{
			Instances:       1,
			DependencyPools: map[string]int{"postgresql-db": 20},
		},
		Paths: map[string]frontend.PathItem{"/pedidos": rotaComDependencia(1)},
	})
	graph, err := BuildIR(valid)
	if err != nil {
		t.Fatalf("construção falhou: %v", err)
	}

	pool := findPlaceIR(graph, "P_Workers_postgresql_db")
	if pool == nil {
		t.Fatal("pool da dependência não encontrado")
	}
	if pool.Tokens != 20 {
		t.Errorf("pool com %d posições, esperado as 20 declaradas", pool.Tokens)
	}
}

func TestPoolRecaiNoPadraoQuandoNaoDeclarado(t *testing.T) {
	graph := construir(t, 1, map[string]frontend.PathItem{"/pedidos": rotaComDependencia(1)})
	pool := findPlaceIR(graph, "P_Workers_postgresql_db")
	if pool == nil || pool.Tokens != DefaultDependencyPool {
		t.Errorf("pool deveria recair no padrão %d, obtido %v", DefaultDependencyPool, pool)
	}
}

// O custo por chamada é da rota, e alimenta cada estágio da Erlang.
func TestCustoPorChamadaDeclarado(t *testing.T) {
	rota := frontend.PathItem{Post: &frontend.Operation{
		Performance: &frontend.Performance{
			ProcessingTime: frontend.ProcessingTime{MeanMS: 85},
			Dependencies: []frontend.Dependency{
				{Service: "postgresql-db", Calls: 3, MeanMS: 30},
			},
		},
	}}
	valid, _ := semantic.ValidateSpec(&frontend.OpenAPISpec{
		Service: frontend.ServicePerformance{Instances: 1},
		Paths:   map[string]frontend.PathItem{"/pedidos": rota},
	})
	graph, err := BuildIR(valid)
	if err != nil {
		t.Fatalf("construção falhou: %v", err)
	}

	base := "TT_Processing_postgresql_db_POST_pedidos"
	var soma float64
	for _, nome := range []string{base, base + "_S2", base + "_S3"} {
		tr := transicao(graph, nome)
		if tr == nil {
			t.Fatalf("estágio %s não gerado", nome)
		}
		if tr.DelayValue < 0.030-1e-9 || tr.DelayValue > 0.030+1e-9 {
			t.Errorf("%s com atraso %v, esperado 0.030", nome, tr.DelayValue)
		}
		soma += tr.DelayValue
	}
	// Três chamadas de 30 ms totalizam 90 ms de conversa com o recurso.
	if soma < 0.090-1e-9 || soma > 0.090+1e-9 {
		t.Errorf("soma dos estágios = %v, esperado 0.090", soma)
	}
}
