package semantic

import (
	"fmt"
	"prognos/pkg/frontend"
)

// DefaultArrivalRate é a carga adotada quando a especificação não declara nem a
// taxa da rota nem a global. Equivale ao atraso de 0,2 s entre chegadas que era
// fixado internamente antes de o campo existir, o que preserva o comportamento
// das especificações que não o declaram.
const DefaultArrivalRate = 5.0

// NormalizedRoute facilita a vida da próxima etapa achatando a hierarquia do OpenAPI.
type NormalizedRoute struct {
	Method      string
	Path        string
	Performance *frontend.Performance
	// ArrivalRate é a taxa efetiva da rota, em requisições por segundo, já
	// resolvida entre o valor da rota, o global e o padrão.
	ArrivalRate float64
}

type ValidSpec struct {
	RawSpec      *frontend.OpenAPISpec
	Routes       []NormalizedRoute
	Dependencies []string // Lista única de recursos externos compartilhados
}

// ValidateSpec valida regras de negócio e checa integridade de dependências.
func ValidateSpec(spec *frontend.OpenAPISpec) (*ValidSpec, error) {
	// Fallback de segurança: Pelo menos 1 instância é necessária para não quebrar a matemática
	if spec.Service.Instances <= 0 {
		spec.Service.Instances = 1
	}

	valid := &ValidSpec{
		RawSpec:      spec,
		Routes:       make([]NormalizedRoute, 0),
		Dependencies: make([]string, 0),
	}

	// Mapa para garantir que não teremos dependências duplicadas na lista
	depMap := make(map[string]bool)

	for path, item := range spec.Paths {
		// Função auxiliar para evitar repetição de código para GET, POST, etc.
		checkAndAdd := func(method string, op *frontend.Operation) {
			if op != nil && op.Performance != nil {
				// Validação semântica estrita: o tempo tem que ser positivo
				if op.Performance.ProcessingTime.MeanMS <= 0 {
					fmt.Printf("   [Aviso] Rota %s %s tem tempo <= 0. Ignorando para simulação.\n", method, path)
					return
				}

				// A taxa da rota tem precedência sobre a global, e a global sobre
				// o padrão. Valor não positivo é tratado como não declarado, pois
				// uma taxa nula significaria uma rota que nunca recebe carga.
				taxa := op.Performance.ArrivalRate
				if taxa <= 0 {
					taxa = spec.Service.ArrivalRate
				}
				if taxa <= 0 {
					taxa = DefaultArrivalRate
				}

				valid.Routes = append(valid.Routes, NormalizedRoute{
					Method:      method,
					Path:        path,
					Performance: op.Performance,
					ArrivalRate: taxa,
				})

				// Coletar dependências únicas
				for _, dep := range op.Performance.Dependencies {
					if !depMap[dep.Service] {
						depMap[dep.Service] = true
						valid.Dependencies = append(valid.Dependencies, dep.Service)
					}
				}
			}
		}

		checkAndAdd("GET", item.Get)
		checkAndAdd("POST", item.Post)
		checkAndAdd("PUT", item.Put)
		checkAndAdd("PATCH", item.Patch)
		checkAndAdd("DELETE", item.Delete)
	}

	return valid, nil
}
