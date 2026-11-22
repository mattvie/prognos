package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prognos/pkg/backend"
	"prognos/pkg/enforcer"
	"prognos/pkg/frontend"
	"prognos/pkg/ir"
	"prognos/pkg/semantic"
)

// Teste de integração do pipeline: roda os cinco primeiros módulos (o sexto
// depende da JVM e do Mercury.jar, e por isso fica fora dos testes automatizados)
// sobre todas as especificações de exemplo do repositório, verificando as
// invariantes que precisam valer para que o Mercury consiga resolver o modelo.
func especificacoes(t *testing.T) []string {
	t.Helper()

	arquivos, err := filepath.Glob("samples/*.yaml")
	if err != nil {
		t.Fatalf("falha ao listar as especificações: %v", err)
	}
	if len(arquivos) == 0 {
		t.Fatal("nenhuma especificação .yaml encontrada em samples/")
	}
	return arquivos
}

// transpilar executa a cadeia Frontend -> Semântico -> IR -> Enforcer -> Backend.
func transpilar(t *testing.T, arquivo string) (*ir.SPNGraph, string) {
	t.Helper()

	spec, err := frontend.ParseOpenAPI(arquivo)
	if err != nil {
		t.Fatalf("%s: frontend falhou: %v", arquivo, err)
	}

	valid, err := semantic.ValidateSpec(spec)
	if err != nil {
		t.Fatalf("%s: análise semântica falhou: %v", arquivo, err)
	}

	graph, err := ir.BuildIR(valid)
	if err != nil {
		t.Fatalf("%s: construção da IR falhou: %v", arquivo, err)
	}

	otimizado, err := enforcer.OptimizeIR(graph)
	if err != nil {
		t.Fatalf("%s: enforcer falhou: %v", arquivo, err)
	}

	script, err := backend.GenerateMRY(otimizado)
	if err != nil {
		t.Fatalf("%s: geração do .mry falhou: %v", arquivo, err)
	}

	return otimizado, script
}

func TestPipelineCompletoNasEspecificacoesDeExemplo(t *testing.T) {
	for _, arquivo := range especificacoes(t) {
		t.Run(arquivo, func(t *testing.T) {
			graph, script := transpilar(t, arquivo)

			if len(graph.Routes) == 0 {
				t.Fatal("nenhuma rota foi mapeada")
			}
			if !strings.Contains(script, "SPN Model") || !strings.Contains(script, "main {") {
				t.Error("o script gerado não possui a estrutura esperada do Mercury")
			}
		})
	}
}

// Integridade referencial: toda transição só pode citar lugares declarados. Um
// lugar faltando é justamente o tipo de quebra que a separação entre construtor
// da IR e Enforcer poderia introduzir, já que os dois módulos criam arcos.
func TestTodosOsLugaresReferenciadosExistem(t *testing.T) {
	for _, arquivo := range especificacoes(t) {
		t.Run(arquivo, func(t *testing.T) {
			graph, _ := transpilar(t, arquivo)

			declarados := make(map[string]bool)
			for _, p := range graph.Places {
				if declarados[p.Name] {
					t.Errorf("lugar %s declarado mais de uma vez", p.Name)
				}
				declarados[p.Name] = true
			}

			for _, tr := range graph.Transitions {
				for _, grupo := range [][]string{tr.Inputs, tr.Outputs, tr.Inhibitors} {
					for _, lugar := range grupo {
						if !declarados[lugar] {
							t.Errorf("transição %s referencia o lugar inexistente %s", tr.Name, lugar)
						}
					}
				}
			}
		})
	}
}

// As três regras que impedem o Mercury de travar, verificadas na saída final.
func TestPoliticasObrigatoriasNoModeloFinal(t *testing.T) {
	for _, arquivo := range especificacoes(t) {
		t.Run(arquivo, func(t *testing.T) {
			graph, _ := transpilar(t, arquivo)

			// 1. Fila limitada: toda rota tem buffer e descarte.
			for _, rota := range graph.Routes {
				temBuffer := false
				for _, p := range graph.Places {
					if p.Name == "P_Free_"+rota && p.Tokens > 0 {
						temBuffer = true
					}
				}
				if !temBuffer {
					t.Errorf("rota %s não possui buffer limitado", rota)
				}

				temDescarte := false
				for _, tr := range graph.Transitions {
					if tr.Name == "IT_Drop_"+rota && len(tr.Inhibitors) > 0 {
						temDescarte = true
					}
				}
				if !temDescarte {
					t.Errorf("rota %s não possui descarte com arco inibidor", rota)
				}
			}

			// 2. Destruição de tokens: nenhum lugar recebe sem nunca ser consumido.
			consumidos := make(map[string]bool)
			for _, tr := range graph.Transitions {
				for _, in := range tr.Inputs {
					consumidos[in] = true
				}
				for _, inh := range tr.Inhibitors {
					consumidos[inh] = true
				}
			}
			for _, tr := range graph.Transitions {
				for _, out := range tr.Outputs {
					if !consumidos[out] {
						t.Errorf("o lugar %s acumula tokens sem nunca ser consumido", out)
					}
				}
			}

			// 3. Solver: nenhuma métrica pode sair sem método definido.
			for _, m := range graph.Metrics {
				if m.Method != "direct" && m.Method != "iterative" {
					t.Errorf("métrica %s com method inválido: %q", m.Name, m.Method)
				}
			}
			if len(graph.Routes) > 1 {
				for _, m := range graph.Metrics {
					if m.Method != "iterative" {
						t.Errorf("modelo multi-rota deveria usar iterative, métrica %s usa %q", m.Name, m.Method)
					}
				}
			}
		})
	}
}

// O parser de métricas do Mercury quebra com '==' e com aritmética sobre #lugar.
func TestRestricoesDoParserDoMercury(t *testing.T) {
	for _, arquivo := range especificacoes(t) {
		t.Run(arquivo, func(t *testing.T) {
			graph, script := transpilar(t, arquivo)

			for _, m := range graph.Metrics {
				if strings.Contains(m.Expression, "==") {
					t.Errorf("métrica %s usa '==': %q", m.Name, m.Expression)
				}
			}

			// println com aritmética inline lança RuntimeException no Mercury.
			for _, linha := range strings.Split(script, "\n") {
				if !strings.Contains(linha, "println") {
					continue
				}
				if strings.ContainsAny(linha, "*+/") && strings.Contains(linha, "..") {
					t.Errorf("println com aritmética inline: %s", strings.TrimSpace(linha))
				}
			}
		})
	}
}

// O modelo gerado precisa continuar sendo escrito em disco sem erro, já que é a
// entrada do orquestrador que invoca o Mercury.
func TestScriptGeradoEhGravavel(t *testing.T) {
	_, script := transpilar(t, "samples/02_queue_bottleneck.yaml")

	destino := filepath.Join(t.TempDir(), "modelo.mry")
	if err := os.WriteFile(destino, []byte(script), 0644); err != nil {
		t.Fatalf("falha ao gravar o script: %v", err)
	}
}
