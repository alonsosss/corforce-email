#!/usr/bin/env bash
# Guardarrail: la proporcion entre codigo de prueba y codigo Go no baja (docs/Plan_Estrategico_Mejoras_Correo.md, B7).
#
# Por que: el codigo propio es lo que mantiene esta plataforma; si el codigo crece mas rapido que sus
# pruebas, cada cambio posterior se hace a ciegas. La proporcion se mide en lineas no vacias de
# *_test.go frente a las de los demas .go, por servicio y para pkg/, y se compara con
# ops/scaffold/test-ratio-floors.txt: un trinquete que se sube cuando un servicio mejora y no se baja
# sin una razon escrita. Un servicio que no figura en el fichero (uno nuevo) debe cumplir el suelo por
# defecto, de modo que nace con pruebas.
#
# Y demuestra que muerde, con un arbol de prueba: un servicio sin pruebas, uno por debajo de su suelo,
# uno nuevo por debajo del suelo por defecto y un total por debajo del suyo fallan; uno que cumple, no.
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FLOORS="$ROOT/ops/scaffold/test-ratio-floors.txt"
DEFAULT_FLOOR="0.50"
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

# medir <raiz>: una linea "unidad codigo pruebas" por servicio y por pkg.
medir() {
  local root="$1" dir unit
  for dir in "$root"/services/*/ "$root"/pkg/; do
    [[ -d "$dir" ]] || continue
    unit="${dir#"$root"/}"; unit="${unit%/}"
    find "$dir" -name '*.go' -not -path '*/vendor/*' -print0 | xargs -0 -r awk '
      FNR == 1 { test = (FILENAME ~ /_test\.go$/) }
      NF { if (test) t++; else c++ }
      END { printf "%s %d %d\n", unit, c + 0, t + 0 }' unit="$unit"
  done
}

# verificar <raiz> <suelos>: imprime cada incumplimiento y devuelve 1 si hay alguno.
verificar() {
  local root="$1" floors="$2"
  medir "$root" | awk -v floors="$floors" -v default_floor="$DEFAULT_FLOOR" '
    BEGIN {
      while ((getline line < floors) > 0) {
        if (line ~ /^#/ || line !~ /[^ ]/) continue
        split(line, f, " "); suelo[f[1]] = f[2] + 0
      }
    }
    {
      unit = $1; code = $2; tests = $3
      ratio = (code > 0) ? tests / code : 1
      tc += code; tt += tests
      min = (unit in suelo) ? suelo[unit] : default_floor
      if (code > 0 && ratio + 0.0001 < min) {
        printf "%s: %.2f lineas de prueba por linea de codigo, el suelo es %.2f%s\n", unit, ratio, min, (unit in suelo) ? "" : " (por defecto, no figura en test-ratio-floors.txt)"
        bad = 1
      }
    }
    END {
      total = (tc > 0) ? tt / tc : 1
      if ("TOTAL" in suelo && total + 0.0001 < suelo["TOTAL"]) {
        printf "TOTAL: %.2f, el suelo es %.2f\n", total, suelo["TOTAL"]
        bad = 1
      }
      exit bad
    }'
}

echo "  Arbol real:"
if [[ ! -f "$FLOORS" ]]; then falla "falta $FLOORS"; else
  out="$(verificar "$ROOT" "$FLOORS")" || { while IFS= read -r l; do falla "$l"; done <<<"$out"; }
  [[ $FAIL -eq 0 ]] && echo "  OK: ningun servicio ni pkg baja de su suelo de pruebas"
fi

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
mk() { # mk <ruta> <lineas de codigo> <lineas de prueba>
  mkdir -p "$TMP/$1"
  seq "$2" | sed 's/^/x := /' > "$TMP/$1/code.go"
  [[ "$3" -gt 0 ]] && seq "$3" | sed 's/^/x := /' > "$TMP/$1/code_test.go"
  return 0
}
mk services/sano 100 80; mk pkg 100 90
printf 'TOTAL 0.70\nservices/sano 0.70\npkg 0.70\n' > "$TMP/floors"

echo "  Mutaciones (cada una tiene que fallar con su mensaje):"
esperar_falla() { # esperar_falla <nombre> <mensaje esperado>
  local out; out="$(verificar "$TMP" "$TMP/floors" 2>&1)"
  if [[ $? -eq 0 ]]; then falla "$1: no fallo"; elif [[ "$out" != *"$2"* ]]; then falla "$1: fallo sin '$2' ($out)"; else echo "    OK  $1"; fi
}
if verificar "$TMP" "$TMP/floors" >/dev/null; then echo "    OK  un arbol que cumple no falla"; else falla "un arbol que cumple fallo"; fi

mk services/sinpruebas 100 0
esperar_falla "un servicio con codigo y sin pruebas" "services/sinpruebas: 0.00"
rm -rf "$TMP/services/sinpruebas"

mk services/nuevo 100 30
esperar_falla "un servicio nuevo bajo el suelo por defecto" "services/nuevo: 0.30"
mk services/nuevo 100 60
if verificar "$TMP" "$TMP/floors" >/dev/null; then echo "    OK  un servicio nuevo con pruebas suficientes no falla"; else falla "un servicio nuevo suficiente fallo"; fi
rm -rf "$TMP/services/nuevo"

mk services/sano 100 60
esperar_falla "un servicio por debajo de su suelo" "services/sano: 0.60 lineas de prueba por linea de codigo, el suelo es 0.70"
mk services/sano 100 80

printf 'TOTAL 0.95\nservices/sano 0.70\npkg 0.70\n' > "$TMP/floors"
esperar_falla "un total por debajo del suyo" "TOTAL: 0.85, el suelo es 0.95"

echo ""
[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: la proporcion entre pruebas y codigo esta protegida."
