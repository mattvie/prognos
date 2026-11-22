package backend

import (
	"bytes"
	"fmt"
	"strings"

	"prognos/pkg/ir"
)

// Paleta usada para colorir os arcos de cada recurso compartilhado. As cores
// evitam deliberadamente o vermelho, reservado aos arcos inibidores.
var coresDeRecurso = []string{
	"#1a6b3c", // verde
	"#8a5a00", // âmbar
	"#6a3d9a", // roxo
	"#16697a", // azul-petróleo
	"#8a1f5a", // vinho
	"#4a5f18", // oliva
}

const (
	corInibidor = "#b03030"
	corFluxo    = "#2b2b2b"
	corTraco    = "#3c3c3c"
	corLugar    = "#e8f0fe"
	corBordaLug = "#33507a"
	corGrupo    = "#b9c4d4"
	corGrupoDep = "#b03030"
)

// rotaDe devolve a rota a que um elemento pertence e o rótulo abreviado que
// será exibido. Todo elemento privativo de uma rota carrega o nome dela no
// identificador, convenção estabelecida em pkg/ir/builder.go. O nome da rota
// nem sempre está no fim: os estágios gerados pelo campo calls acrescentam um
// sufixo próprio depois dele, como em P_Busy_dep_ROTA_S2. Por isso o trecho é
// localizado e removido, e não apenas cortado do fim. Elementos que não casam
// com nenhuma rota são os recursos compartilhados, desenhados fora dos grupos.
func rotaDe(nome string, rotas []string) (rota, rotulo string) {
	melhor := ""
	for _, r := range rotas {
		trecho := "_" + r
		if strings.Contains(nome, trecho) && len(trecho) > len(melhor) {
			melhor, rota = trecho, r
		}
	}
	if melhor == "" {
		return "", nome
	}
	i := strings.Index(nome, melhor)
	return rota, nome[:i] + nome[i+len(melhor):]
}

// elemento reúne o que o desenho precisa saber sobre um nó da rede.
type elemento struct {
	nome     string
	rotulo   string
	lugar    bool
	tokens   int
	imediata bool
	dep      string // dependência externa a que pertence, se houver
}

// depDe descobre a qual recurso externo um elemento pertence, a partir do nome
// já despido do sufixo da rota. O pool do recurso não entra nessa conta: ele é
// potencialmente compartilhado por várias rotas e permanece fora dos grupos.
func depDe(rotulo string, deps []string) string {
	melhor := ""
	for _, d := range deps {
		if strings.Contains(rotulo, "_"+d) && len(d) > len(melhor) {
			melhor = d
		}
	}
	return melhor
}

// GenerateDOT traduz o grafo para a linguagem DOT seguindo a notação usual de
// Redes de Petri: lugares são círculos, transições imediatas são barras cheias
// e transições temporizadas são barras vazadas.
//
// Três decisões orientam o desenho. Os nomes ficam fora dos nós, como rótulos
// externos, porque os identificadores gerados são longos e inflariam os nós a
// ponto de tornar a figura ilegível. Os elementos de cada rota são agrupados,
// o que permite exibir apenas a parte do nome que os distingue. E cada lugar de
// recurso recebe uma cor própria, aplicada aos arcos que o consomem e o
// devolvem, de modo que os pontos de acoplamento possam ser seguidos mesmo
// quando cruzam o diagrama inteiro.
func GenerateDOT(graph *ir.SPNGraph) (string, error) {
	var buf bytes.Buffer

	// Um lugar que nasce marcado representa um recurso disponível; os demais são
	// posições de passagem da requisição. A distinção é estrutural e dispensa
	// qualquer consulta aos nomes dos lugares.
	corDoRecurso := make(map[string]string)
	for _, p := range graph.Places {
		if p.Tokens > 0 {
			corDoRecurso[p.Name] = coresDeRecurso[len(corDoRecurso)%len(coresDeRecurso)]
		}
	}

	grupos := make(map[string][]elemento)
	var compartilhados []elemento

	classificar := func(e elemento) {
		rota, rotulo := rotaDe(e.nome, graph.Routes)
		e.rotulo = rotulo
		if rota == "" {
			// Pools de recurso e demais elementos sem rota ficam soltos.
			compartilhados = append(compartilhados, e)
			return
		}
		// Pertencendo a um recurso externo, o nome dele também sai do rótulo: o
		// bloco pontilhado que envolve o trecho já o exibe no cabeçalho.
		if e.dep = depDe(rotulo, graph.Dependencies); e.dep != "" {
			i := strings.Index(e.rotulo, "_"+e.dep)
			e.rotulo = e.rotulo[:i] + e.rotulo[i+len(e.dep)+1:]
		}
		grupos[rota] = append(grupos[rota], e)
	}
	for _, p := range graph.Places {
		classificar(elemento{nome: p.Name, lugar: true, tokens: p.Tokens})
	}
	for _, t := range graph.Transitions {
		classificar(elemento{nome: t.Name, imediata: !t.IsTimed})
	}

	escreverNo := func(e elemento, ident string) {
		switch {
		case e.lugar:
			// A marcação inicial fica dentro do círculo; o nome, ao lado.
			marcacao := ""
			if e.tokens > 0 {
				marcacao = fmt.Sprintf("%d", e.tokens)
			}
			fmt.Fprintf(&buf, "%s%s [shape=circle, style=filled, fillcolor=\"%s\", color=\"%s\", penwidth=1.6, width=0.55, fixedsize=true, label=\"%s\", xlabel=\"%s\"];\n",
				ident, e.nome, corLugar, corBordaLug, marcacao, e.rotulo)
		case e.imediata:
			fmt.Fprintf(&buf, "%s%s [shape=box, style=filled, fillcolor=\"%s\", color=\"%s\", width=0.13, height=0.55, fixedsize=true, label=\"\", xlabel=\"%s\"];\n",
				ident, e.nome, corTraco, corTraco, e.rotulo)
		default:
			fmt.Fprintf(&buf, "%s%s [shape=box, style=filled, fillcolor=\"white\", color=\"%s\", penwidth=1.8, width=0.17, height=0.55, fixedsize=true, label=\"\", xlabel=\"%s\"];\n",
				ident, e.nome, corTraco, e.rotulo)
		}
	}

	buf.WriteString("digraph SPN {\n")
	buf.WriteString("  rankdir=LR;\n")
	buf.WriteString("  splines=spline;\n")
	buf.WriteString("  nodesep=0.75;\n")
	buf.WriteString("  ranksep=1.05;\n")
	buf.WriteString("  bgcolor=\"white\";\n")
	buf.WriteString("  pad=0.35;\n")
	buf.WriteString("  forcelabels=true;\n")
	buf.WriteString("  compound=true;\n")
	buf.WriteString("  node [fontname=\"Helvetica\", fontsize=10];\n")
	fmt.Fprintf(&buf, "  edge [fontname=\"Helvetica\", fontsize=9, arrowsize=0.75, color=\"%s\"];\n\n", corFluxo)

	buf.WriteString("  // --- RECURSOS COMPARTILHADOS ---\n")
	for _, e := range compartilhados {
		escreverNo(e, "  ")
	}

	buf.WriteString("\n  // --- SUBREDES DAS ROTAS ---\n")
	for _, rota := range graph.Routes {
		fmt.Fprintf(&buf, "  subgraph cluster_%s {\n", rota)
		fmt.Fprintf(&buf, "    label=\"%s\";\n", rota)
		buf.WriteString("    fontsize=12;\n")
		fmt.Fprintf(&buf, "    style=rounded; color=\"%s\"; margin=14;\n", corGrupo)

		// Primeiro o que é da rota propriamente dita.
		for _, e := range grupos[rota] {
			if e.dep == "" {
				escreverNo(e, "    ")
			}
		}

		// Depois, um bloco aninhado por recurso externo consultado. A moldura
		// pontilhada e a margem menor deixam claro que se trata de um trecho
		// contido na rota, e não de uma estrutura paralela a ela.
		for _, dep := range graph.Dependencies {
			var doDep []elemento
			for _, e := range grupos[rota] {
				if e.dep == dep {
					doDep = append(doDep, e)
				}
			}
			if len(doDep) == 0 {
				continue
			}
			fmt.Fprintf(&buf, "    subgraph cluster_%s_%s {\n", rota, dep)
			fmt.Fprintf(&buf, "      label=\"%s\";\n", dep)
			buf.WriteString("      fontsize=10;\n")
			// A margem é escalar de propósito: o Graphviz ignora a forma "x,y"
			// em clusters. Cada ponto acrescentado aqui alarga o bloco em dois
			// pontos de altura, e 12 o deixa com folga suficiente para não
			// parecer colado nos nós, sem competir com a moldura da rota.
			fmt.Fprintf(&buf, "      style=dotted; color=\"%s\"; penwidth=1.4; margin=12;\n", corGrupoDep)
			for _, e := range doDep {
				escreverNo(e, "      ")
			}
			buf.WriteString("    }\n")
		}

		buf.WriteString("  }\n")
	}

	buf.WriteString("\n  // --- ARCOS ---\n")
	for _, t := range graph.Transitions {
		for _, input := range t.Inputs {
			if cor, ok := corDoRecurso[input]; ok {
				fmt.Fprintf(&buf, "  %s -> %s [color=\"%s\"];\n", input, t.Name, cor)
				continue
			}
			fmt.Fprintf(&buf, "  %s -> %s;\n", input, t.Name)
		}

		for _, output := range t.Outputs {
			// A devolução de um recurso é um arco de retorno: liberá-lo da
			// hierarquia de níveis evita que o grafo seja esticado por ele.
			if cor, ok := corDoRecurso[output]; ok {
				fmt.Fprintf(&buf, "  %s -> %s [color=\"%s\", constraint=false];\n", t.Name, output, cor)
				continue
			}
			fmt.Fprintf(&buf, "  %s -> %s;\n", t.Name, output)
		}

		// O arco inibidor termina em círculo vazado, e não em seta, indicando
		// que a transição só dispara quando o lugar está vazio.
		for _, inhib := range t.Inhibitors {
			fmt.Fprintf(&buf, "  %s -> %s [arrowhead=odot, color=\"%s\", penwidth=1.7, constraint=false];\n", inhib, t.Name, corInibidor)
		}
	}

	buf.WriteString("}\n")
	return buf.String(), nil
}
