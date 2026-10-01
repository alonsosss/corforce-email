#!/usr/bin/env bash
# Todo envoltorio de http.ResponseWriter deja llegar al escritor real.
#
# Por que: un tipo que incrusta http.ResponseWriter para leer el codigo de respuesta esconde las
# interfaces del escritor que envuelve. http.ResponseController (lo que usa httputil.ReverseProxy
# para vaciar el bufer) solo las alcanza con un metodo Unwrap. Sin el, una respuesta en flujo se
# queda retenida hasta que el servicio la cierra. Paso el 2026-09-27: el guardia de sondeo del
# gateway envolvio toda la API y los avisos SSE del webmail dejaron de llegar, sin ningun error.
#
# Comprueba que cada tipo de services/ y pkg/ (fuera de las pruebas) que incrusta
# http.ResponseWriter tiene en su paquete `func (... T) Unwrap() http.ResponseWriter`. Y demuestra
# que muerde con un arbol de prueba: un envoltorio sin Unwrap falla y uno con Unwrap pasa.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

# revisar <raiz> <carpetas...>: escribe un FALLA por envoltorio sin Unwrap; sale 1 si hay alguno.
revisar() {
  local raiz="$1" fallo=0 fichero tipo dir; shift
  while IFS=$'\t' read -r fichero tipo; do
    [[ -n "$tipo" ]] || continue
    dir="$(dirname "$fichero")"
    if ! grep -qE "^func \([[:alnum:]_]+ \*?$tipo\) Unwrap\(\) http\.ResponseWriter" "$dir"/*.go 2>/dev/null; then
      echo "  FALLA: ${fichero#"$raiz"/}: $tipo incrusta http.ResponseWriter sin Unwrap() http.ResponseWriter"
      fallo=1
    fi
  done < <(
    for d in "$@"; do [[ -d "$raiz/$d" ]] && find "$raiz/$d" -name '*.go' ! -name '*_test.go' -print0; done |
      xargs -0 -r awk '
        /^type [[:alnum:]_]+ struct \{/ { tipo = $2; dentro = 1; next }
        dentro && /^\}/ { dentro = 0; next }
        dentro && /^[[:space:]]+\*?http\.ResponseWriter[[:space:]]*(\/\/.*)?$/ { printf "%s\t%s\n", FILENAME, tipo }
      '
  )
  return $fallo
}

echo "== Envoltorios de http.ResponseWriter con Unwrap =="
FAIL=0
revisar "$ROOT" services pkg || FAIL=1

# Mutaciones: el guardarrail tiene que morder.
mkdir -p "$TMP/services/x"
cat >"$TMP/services/x/w.go" <<'EOF'
package x

import "net/http"

type grabador struct {
	http.ResponseWriter
	status int
}
EOF
if revisar "$TMP" services >"$TMP/salida"; then
  echo "  FALLA: el guardarrail no detecta un envoltorio sin Unwrap"; FAIL=1
elif ! grep -q "grabador incrusta http.ResponseWriter" "$TMP/salida"; then
  echo "  FALLA: el guardarrail no dice que tipo falla"; FAIL=1
fi
cat >>"$TMP/services/x/w.go" <<'EOF'

func (g *grabador) Unwrap() http.ResponseWriter { return g.ResponseWriter }
EOF
if ! revisar "$TMP" services >"$TMP/salida"; then
  echo "  FALLA: el guardarrail rechaza un envoltorio con Unwrap"; cat "$TMP/salida"; FAIL=1
fi

[[ $FAIL -eq 0 ]] && echo "  OK: todo envoltorio de http.ResponseWriter alcanza el escritor real; la mutacion se detecta."
exit $FAIL
