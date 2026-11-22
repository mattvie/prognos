package ir

// Place representa um Lugar/Estado na Rede de Petri
type Place struct {
	Name   string
	Tokens int
}

// Transition representa um Evento (Temporizado ou Imediato) e as suas conexões
type Transition struct {
	Name       string
	IsTimed    bool
	DelayValue float64  // Tempo em segundos (ex: 0.085)
	Inputs     []string // Nomes dos Lugares de origem
	Outputs    []string // Nomes dos Lugares de destino
	Inhibitors []string // Nomes dos Lugares inibidores

	// InfiniteServer indica semântica de servidor infinito (ISS): a transição
	// atende em paralelo tantas requisições quantos forem os tokens no lugar de
	// entrada. É o que dá sentido a um pool de N instâncias, já que o Mercury
	// assume SingleServer por omissão e serializaria todo o atendimento.
	InfiniteServer bool
}

// Metric representa as fórmulas matemáticas que avaliam a rede
type Metric struct {
	Name       string
	Method     string
	Expression string
}

// SPNGraph é o Grafo completo em memória, independente da sintaxe do simulador
type SPNGraph struct {
	Places      []Place
	Transitions []Transition
	Metrics     []Metric

	// Routes guarda o nome sanitizado de cada rota mapeada (ex: "GET_export").
	// O Enforcer usa esta lista para saber onde injetar as políticas sem precisar
	// deduzir as rotas a partir dos prefixos dos nomes dos lugares.
	Routes []string

	// RouteCapacity é a capacidade K de cada rota declarada na especificação. Vale zero
	// quando o campo é omitido, e nesse caso o Enforcer aplica o seu padrão.
	RouteCapacity int

	// Dependencies guarda o nome sanitizado de cada recurso externo declarado
	// (ex: "postgresql_db"), já no formato usado nos identificadores da rede.
	Dependencies []string

	// ArrivalRates guarda a taxa de chegada de cada rota, em requisições por
	// segundo, indexada pelo nome sanitizado da rota. O relatório precisa dela
	// para aplicar a Lei de Little, que exige a vazão e não o intervalo entre
	// chegadas que a rede armazena no atraso da transição.
	ArrivalRates map[string]float64
}
