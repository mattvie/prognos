# prognos | Transpilador OpenAPI para Redes de Petri Estocásticas

Oriundo do termo médico prognóstico, que é a previsão do curso provável de uma condição de saúde a partir de evidências. Este software faz precisamente isso: prevê como uma arquitetura de software ainda não implantada irá se comportar durante o uso. 

Este projeto realiza o mapeamento automatizado de especificações arquiteturais de APIs (OpenAPI) para modelos formais de Redes de Petri Estocásticas (SPN). Ele permite simular e prever o desempenho (gargalos, filas e uso de CPU) de sistemas distribuídos utilizando Golang e as simulações realizadas no software Mercury. 

> Nome da Trabalho: Mapeamento Automático de Especificações OpenAPI para Modelos Formais de Redes de Petri Estocásticas utilizando Golang e o Software Mercury

## Dependências

Para executar o projeto localmente, você precisará ter instalado em sua máquina:

- **Golang** (Compilação e orquestração)
- **Java 8** (*Requisito obrigatório do Mercury*)
- **Software Mercury** (O arquivo `Mercury.jar` deve estar presente no diretório Mercury, pois o CLI procura `Mercury/Mercury.jar`)
- **Graphviz / xdot** (Ferramentas para renderização do grafo visual `.dot`)

> OBS: Projeto realizado em um ambiente Linux.

> OBS2: A ferramenta Mercury é desenvolvida pelo grupo de pesquisa MoDCS. Para obter acesso, entre em contato através de https://www.modcs.org/ 

## Como Executar

O `prognos` requer um arquivo de especificação no formato OpenAPI enriquecido com as extensões personalizadas de desempenho (`x-service-performance` e `x-performance`).

1. Sem argumentos, o programa usa a especificação de exemplo `samples/openapi.yaml`:

    ```bash
    go run main.go
    ```

2. Para avaliar outra arquitetura, passe o caminho do arquivo como argumento. A pasta `samples/` traz as especificações usadas no trabalho:

    ```bash
    go run main.go samples/07_sequential_monolith.yaml
    ```

O programa fará a leitura da arquitetura, construirá o grafo matemático, invocará o simulador do Mercury via CLI e retornará um relatório:

      =====================================================
        Transpilador OpenAPI -> Mercury SPN | Inicializado 
      =====================================================
      [0/6] Arquivo alvo configurado: samples/07_sequential_monolith.yaml
      [1/6] Executando Parser do OpenAPI...
         -> Sucesso! API Carregada: Monolito Sequencial Estrito (v1.0.0)
         -> Instâncias do Serviço : 2
      [2/6] Executando Análise Semântica e Validação...
         -> Rotas Validadas para Simulação: 1
         -> Dependências Externas Encontradas: []
      [3/6] Construindo Representação Intermédia (Grafo SPN)...
         -> Lugares (Places) Criados      : 4
         -> Transições (Transitions) Criadas: 4
         -> Métricas (Metrics) Geradas    : 2
      [4/6] Aplicando Políticas de Otimização...
         -> Limite da Fila por Rota (K)     : 3
         -> Lugares (Places) Totais         : 5
         -> Transições (Transitions) Totais : 5
         -> Métricas (Metrics) Totais       : 6
      [5/6] Gerando Código do Script Mercury (.mry)...
         -> Script salvo com sucesso em 'modelo_gerado.mry'!
         -> Gerando visualização em Grafo (Graphviz)...
         -> Grafo salvo com sucesso em 'modelo_visual.dot'!
      [6/6] Invocando CLI do Mercury...

      =====================================================
               RELATORIO DE DESEMPENHO DA API              
      =====================================================
         [CPU]  Utilizacao (0 a 1)              : 0.8432
         [CPU]  Utilizacao media do pool (0 a 1): 0.9066

         [ROTA] post_report
           -> Req. na Fila (MQS)          : 0.5733 reqs
           -> Total no Sistema (MSS)      : 2.3866 reqs
           -> Probabilidade de Drop (Drop): 0.5733 (57.33%)
           -> Vazao Efetiva (TP)          : 2.1333 reqs/s
           -> Tempo de Resposta (MRT)     : 1.1188 s
           -> Tempo de Espera (MWT)       : 0.2688 s
           -> Em processamento            : 1.8133 reqs
      =====================================================


### Arquivos Gerados (Outputs)

Ao final da execução, a ferramenta gera dois arquivos na raiz do projeto:
* `modelo_gerado.mry`: O script nativo do Mercury contendo as declarações da SPN e as métricas.
* `modelo_visual.dot`: A representação topológica da SPN.

3. Para **visualizar a topologia** gerada de forma interativa, execute no terminal:

    ```bash
    xdot modelo_visual.dot
    ```

## Testes

Os testes cobrem os cinco primeiros módulos, que não dependem do Mercury: as políticas do otimizador, a construção da rede e a integração do pipeline sobre todas as especificações de `samples/`, verificando que nenhum modelo gerado viola as restrições que travariam o simulador.

```bash
go test ./...
```

## Varredura de dimensionamento

O script `scripts/varredura.sh` executa o `prognos` repetidas vezes sobre uma especificação, variando o número de instâncias da aplicação e a capacidade do pool de conexões com o banco de dados, e consolida os resultados em CSV. Foi com ele que se obtiveram as varreduras do estudo de caso hospitalar:

```bash
scripts/varredura.sh samples/hospital.yaml 1 2 4 8 -- 20 > instancias.csv
```

<!-- ## Documentação (Pasta `docs/`)

Para entender como construir novos modelos e como a ferramenta funciona de forma aprofundada, consulte a pasta `docs/`. Ela contém a fundamentação teórica e técnica do projeto:

* **`transpiler.md`**: Explica a arquitetura dos módulos do software escrito em Go (Parser, Validador, IR Builder, Otimizador, Gerador, Orquestrador).
* **`spn_models_guidelines.md`**: O dicionário das diretrizes de mapeamento. Explica como anotar o seu arquivo OpenAPI com as tags `x-performance` para simular requisições, concorrência e dependências.
* **`spn_mry_guidelines.md`**: Manual das restrições e mecânicas da sintaxe proprietária `.mry` executada pelo Mercury (prevenção de deadlocks, explosão de estados, etc.). -->