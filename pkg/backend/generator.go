package backend

import (
	"bytes"
	"fmt"
	"prognos/pkg/ir"
	"strings"
	"text/template"
)

// Este e o template do script .mry. Os blocos {{ }} serao substituidos pelo Go.
const mryTemplate = `// =========================================================================
// Script Mercury Gerado Automaticamente pelo Transpilador Go
// =========================================================================

SPN Model {

    // --- PLACES ---
{{- range .Places }}
    {{- if gt .Tokens 0 }}
    place {{ .Name }}(tokens={{ .Tokens }});
    {{- else }}
    place {{ .Name }};
    {{- end }}
{{- end }}

    // --- TRANSITIONS ---
{{- range .Transitions }}
    {{- if .IsTimed }}
    timedTransition {{ .Name }} (
        {{- if .Inputs }}
        inputs=[{{ join .Inputs ", " }}],
        {{- end }}
        {{- if .Outputs }}
        outputs=[{{ join .Outputs ", " }}],
        {{- end }}
        {{- if .Inhibitors }}
        inhibitors=[{{ join .Inhibitors ", " }}],
        {{- end }}
        {{- if .InfiniteServer }}
        serverType="InfiniteServer",
        {{- end }}
        delay={{ .DelayValue }}
    );
    {{- else }}
    immediateTransition {{ .Name }} (
        {{- if .Inputs }}
        inputs=[{{ join .Inputs ", " }}]{{ if or .Outputs .Inhibitors }},{{ end }}
        {{- end }}
        {{- if .Outputs }}
        outputs=[{{ join .Outputs ", " }}]{{ if .Inhibitors }},{{ end }}
        {{- end }}
        {{- if .Inhibitors }}
        inhibitors=[{{ join .Inhibitors ", " }}]
        {{- end }}
    );
    {{- end }}
{{- end }}

    // --- METRICS ---
{{- range .Metrics }}
    metric {{ .Name }} = stationaryAnalysis(method="{{ .Method }}", expression="{{ .Expression }}");
{{- end }}
}

// --- MAIN EXECUTION ---
main {
    println("=====================================================");
    println("  Resultados da Simulacao - Transpilador Go");
    println("=====================================================");
    
{{- range .Metrics }}
    res_{{ .Name }} = solve(Model, {{ .Name }});
    println("{{ .Name }} : " .. res_{{ .Name }});
{{- end }}
    
    println("=====================================================");
}
`

// GenerateMRY compila o grafo de memoria em uma string formatada em Mercury Script.
//
// Este modulo e puramente um tradutor: ele nao altera a topologia nem acrescenta
// metricas ao grafo. Tudo o que for politica de modelagem ja foi aplicado pelo
// pacote enforcer antes desta etapa.
func GenerateMRY(graph *ir.SPNGraph) (string, error) {
	// Funcao auxiliar para juntar listas (ex: [p1, p2]) com virgulas no template
	funcMap := template.FuncMap{
		"join": strings.Join,
	}

	tmpl, err := template.New("mry").Funcs(funcMap).Parse(mryTemplate)
	if err != nil {
		return "", fmt.Errorf("erro ao fazer parse do template: %w", err)
	}

	// Executa o template guardando o resultado num buffer de memoria
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, graph)
	if err != nil {
		return "", fmt.Errorf("erro ao executar o template: %w", err)
	}

	return buf.String(), nil
}
