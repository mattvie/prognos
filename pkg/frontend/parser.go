package frontend

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// OpenAPISpec representa a raiz do documento YAML
type OpenAPISpec struct {
	OpenAPI string              `yaml:"openapi"`
	Info    Info                `yaml:"info"`
	Service ServicePerformance  `yaml:"x-service-performance"`
	Paths   map[string]PathItem `yaml:"paths"`
}

type Info struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
}

type ServicePerformance struct {
	Instances  int    `yaml:"instances"`
	WorkerType string `yaml:"worker_type"`
	// RouteCapacity é a capacidade K de cada rota: o número máximo de requisições
	// admitidas e ainda não respondidas, somadas as que aguardam e as que estão em
	// atendimento. Zero significa
	// não declarado, caso em que o enforcer aplica o seu valor padrão.
	RouteCapacity int `yaml:"route_capacity"`
	// ArrivalRate é a taxa de chegada, em requisições por segundo, adotada pelas
	// rotas que não declararem a sua própria. Zero significa não declarada.
	ArrivalRate float64 `yaml:"arrival_rate"`
	// DependencyPools declara a capacidade de cada recurso externo, indexada pelo
	// nome usado em dependencies[].service. A declaração é global porque o pool
	// pertence ao recurso, e não à rota que o consome: duas rotas que citem o
	// mesmo recurso descrevem a mesma capacidade.
	DependencyPools map[string]int `yaml:"dependency_pools"`
}

type PathItem struct {
	Get    *Operation `yaml:"get,omitempty"`
	Post   *Operation `yaml:"post,omitempty"`
	Put    *Operation `yaml:"put,omitempty"`
	Patch  *Operation `yaml:"patch,omitempty"`
	Delete *Operation `yaml:"delete,omitempty"`
}

type Operation struct {
	Summary     string       `yaml:"summary"`
	Performance *Performance `yaml:"x-performance,omitempty"`
}

type Performance struct {
	ProcessingTime ProcessingTime `yaml:"processing_time"`
	Dependencies   []Dependency   `yaml:"dependencies,omitempty"`
	// ArrivalRate é a carga da rota, em requisições por segundo. Cada rota tem a
	// sua, pois é justamente a diferença de demanda entre elas que caracteriza o
	// perfil de tráfego do sistema. Zero recai sobre o valor global.
	ArrivalRate float64 `yaml:"arrival_rate"`
}

type ProcessingTime struct {
	Distribution string `yaml:"distribution"`
	MeanMS       int    `yaml:"mean_ms"`
	StdDevMS     int    `yaml:"std_dev_ms"`
}

type Dependency struct {
	Service string `yaml:"service"`
	Calls   int    `yaml:"calls"`
	// MeanMS é o custo de uma única chamada a este recurso. Fica no escopo da
	// rota porque uma consulta simples e uma transação pesada sobre o mesmo banco de dados
	// custam tempos diferentes. Zero recai sobre o padrão do construtor.
	MeanMS int `yaml:"mean_ms"`
}

// ParseOpenAPI lê o arquivo do disco e transforma nas structs estruturadas acima.
func ParseOpenAPI(filepath string) (*OpenAPISpec, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler o arquivo %s: %w", filepath, err)
	}

	var spec OpenAPISpec
	err = yaml.Unmarshal(data, &spec)
	if err != nil {
		return nil, fmt.Errorf("falha ao decodificar o YAML: %w", err)
	}

	return &spec, nil
}
