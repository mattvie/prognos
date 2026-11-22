package orchestrator

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings" // Adicionado para normalizar as chaves das métricas
)

// SimulationResult captura os dados que saíram do terminal do Mercury após o parse de RegEx.
type SimulationResult struct {
	Success bool
	Outputs map[string]float64
}

// ExecuteMercury salva o arquivo temporário, invoca o Java CLI e captura os resultados.
func ExecuteMercury(mryScriptContent string) (*SimulationResult, error) {
	result := &SimulationResult{
		Success: false,
		Outputs: make(map[string]float64),
	}

	// 1. O arquivo já foi salvo como "modelo_gerado.mry" pela Peça 5 no main.go
	cmd := exec.Command("java", "-jar", "Mercury/Mercury.jar", "-cli", "modelo_gerado.mry")

	// Capturamos as saídas Stdout e Stderr (para caso o Mercury trave)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("falha ao rodar o Mercury (Java). Erro: %v\nStderr: %s", err, stderr.String())
	}

	rawOutput := stdout.String()

	// 2. Análise da Saída (Regex)
	re := regexp.MustCompile(`([a-zA-Z0-9_]+)\s*:\s*([0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)`)

	matches := re.FindAllStringSubmatch(rawOutput, -1)

	if len(matches) == 0 {
		return nil, fmt.Errorf("o Mercury rodou, mas nenhuma métrica foi encontrada na saída.\nSaída crua: %s", rawOutput)
	}

	for _, match := range matches {
		if len(match) == 3 {
			metricName := strings.ToLower(match[1])
			metricValueStr := match[2]

			// Converte a string decimal "0.45" para float64 0.45
			var floatVal float64
			fmt.Sscanf(metricValueStr, "%f", &floatVal)

			result.Outputs[metricName] = floatVal
		}
	}

	result.Success = true
	return result, nil
}
