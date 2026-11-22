#!/usr/bin/env bash
# Varredura de dimensionamento: executa o transpilador repetidas vezes sobre a
# especificação do hospital, variando o número de instâncias da aplicação e a
# capacidade do pool de conexões, e consolida os resultados em CSV.
#
#   uso: scripts/varredura.sh <spec.yaml> <instancias...> -- <conexoes...>
#   ex.: scripts/varredura.sh samples/hospital.yaml 1 2 4 8 -- 20
set -euo pipefail

SPEC="${1:?informe a especificação}"; shift
INSTANCIAS=(); CONEXOES=()
alvo=INSTANCIAS
for arg in "$@"; do
    if [ "$arg" = "--" ]; then alvo=CONEXOES; continue; fi
    if [ "$alvo" = "INSTANCIAS" ]; then INSTANCIAS+=("$arg"); else CONEXOES+=("$arg"); fi
done
[ ${#CONEXOES[@]} -eq 0 ] && CONEXOES=($(grep -oP 'postgresql-db:\s*\K[0-9]+' "$SPEC"))

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
echo "instancias,conexoes,rota,mqs,mss,drop,tp,mrt,mwt,cpu_app,espera_dep,dentro_dep,utilizacao,saturacao,util_db,segundos"

for n in "${INSTANCIAS[@]}"; do
  for c in "${CONEXOES[@]}"; do
    sed -e "s/^  instances: .*/  instances: $n/" \
        -e "s/^    postgresql-db: .*/    postgresql-db: $c/" "$SPEC" > "$TMP/spec.yaml"

    ini=$(date +%s.%N)
    saida=$(go run main.go "$TMP/spec.yaml" 2>/dev/null) || { echo "# FALHOU n=$n c=$c" >&2; continue; }
    seg=$(echo "$(date +%s.%N) - $ini" | bc)

    # A utilização média do pool e a probabilidade de saturação são globais, e a
    # do recurso externo aparece na linha [DEP], quando a especificação declara um.
    util=$(echo "$saida" | grep -oE 'Utilizacao media[^:]*: [0-9.]+'   | grep -oE '[0-9.]+$')
    sat=$(echo "$saida"  | grep -oE 'Utilizacao \(0 a 1\)[^:]*: [0-9.]+' | grep -oE '[0-9.]+$')
    udb=$(echo "$saida"  | grep -oE '^\s+\[DEP\].*: [0-9.]+'           | grep -oE '[0-9.]+$')

    # Cada rota ocupa um bloco de linhas de tamanho variável, pois as parcelas de
    # dependência só existem quando a rota declara um recurso externo. Por isso o
    # registro é emitido ao encontrar o bloco seguinte, e não em uma linha fixa.
    echo "$saida" | awk -v n="$n" -v c="$c" -v u="$util" -v s="$sat" -v d="$udb" -v t="$seg" '
      function emite() {
          if (rota != "")
              printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%.1f\n",
                     n, c, rota, mqs, mss, drop, tp, mrt, mwt, cpu, esp, den, u, s, d, t
      }
      /\[ROTA\]/              { emite(); rota=$2; mqs=mss=drop=tp=mrt=mwt=cpu=esp=den="" }
      /Req\. na Fila/         { mqs=$(NF-1) }
      /Total no Sistema/      { mss=$(NF-1) }
      /Probabilidade de Drop/ { drop=$(NF-1) }
      /Vazao Efetiva/         { tp=$(NF-1) }
      /Tempo de Resposta/     { mrt=$(NF-1) }
      /Tempo de Espera/       { mwt=$(NF-1) }
      /Em processamento/      { cpu=$(NF-1) }
      /Espera por Dependencia/ { esp=$(NF-1) }
      /Dentro da Dependencia/ { den=$(NF-1) }
      END                     { emite() }'
  done
done
