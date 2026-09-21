#!/usr/bin/env bash
# Guardarrail: el informe de vulnerabilidades de las imagenes de los motores
# (ops/security/escanear-motores.sh informe, plan de mejoras B3 y B4) cuenta bien, sin red ni docker.
#
# Por que: el informe decide si se abre una incidencia con plazo (critico en 72 horas). Uno que cuenta
# de menos deja un motor vulnerable sin aviso; uno que cuenta de mas (el mismo CVE dos veces) hace ruido
# hasta que nadie lo lee. Se prueba con resultados de Trivy fabricados: una critica y una alta con un
# CVE repetido en dos resultados (cuenta una vez), una imagen limpia, el recorte por imagen, y los dos
# errores que no deben pasar callados (un fichero que no es JSON y una carpeta sin resultados).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TOOL="$ROOT/ops/security/escanear-motores.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

command -v jq >/dev/null || { echo "  falta jq"; exit 1; }

vuln() { # <id> <paquete> <gravedad> <corregida>
  printf '{"VulnerabilityID":"%s","PkgName":"%s","InstalledVersion":"1.0","FixedVersion":"%s","Severity":"%s"}' "$1" "$2" "$4" "$3"
}
mkdir -p "$TMP/ok"
# postfix: una critica, una alta y la misma critica repetida en otro resultado.
cat > "$TMP/ok/trivy-postfix.json" <<JSON
{"Results":[
 {"Target":"debian","Vulnerabilities":[$(vuln CVE-2026-0001 libssl CRITICAL 1.1),$(vuln CVE-2026-0002 libc HIGH 2.0)]},
 {"Target":"otro","Vulnerabilities":[$(vuln CVE-2026-0001 libssl CRITICAL 1.1)]}
]}
JSON
# unbound: limpia (Trivy omite Vulnerabilities cuando no hay).
echo '{"Results":[{"Target":"alpine"}]}' > "$TMP/ok/trivy-unbound.json"
# dovecot: tres altas, para el recorte.
cat > "$TMP/ok/trivy-dovecot.json" <<JSON
{"Results":[{"Target":"alpine","Vulnerabilities":[$(vuln CVE-2026-0010 a HIGH 1),$(vuln CVE-2026-0011 b HIGH 1),$(vuln CVE-2026-0012 c HIGH 1)]}]}
JSON

sal="$(bash "$TOOL" informe "$TMP/ok" 2>&1)"
comprobar() { grep -qF -- "$1" <<< "$sal" && echo "  $2" || falla "$2: falta '$1'"; }
comprobar "CORREGIBLES: 1 4" "cuenta 1 critica y 4 altas (el CVE repetido cuenta una vez)"
comprobar '| `postfix` | 1 | 1 |' "fila de la imagen con critica y alta"
comprobar '| `unbound` | 0 | 0 |' "fila de la imagen limpia"
comprobar '| CRITICAL | CVE-2026-0001 | libssl | 1.0 | 1.1 |' "detalle de la critica con su version corregida"
comprobar '### postfix' "detalle solo de las imagenes con hallazgos"
if grep -qF '### unbound' <<< "$sal"; then falla "una imagen limpia no debe tener detalle"; else echo "  una imagen limpia no tiene detalle"; fi
n="$(grep -c '^| CRITICAL \|^| HIGH ' <<< "$sal")"
[[ "$n" -eq 5 ]] && echo "  el detalle lista cada hallazgo una vez (5 filas)" || falla "el detalle lista $n filas, deberian ser 5"

sal2="$(MAX_POR_IMAGEN=2 bash "$TOOL" informe "$TMP/ok" 2>&1)"
n2="$(awk '/^### dovecot/{f=1;next} /^###/{f=0} f && /^\| HIGH/' <<< "$sal2" | wc -l)"
[[ "$n2" -eq 2 ]] && echo "  MAX_POR_IMAGEN recorta el detalle por imagen" || falla "MAX_POR_IMAGEN=2 dejo $n2 filas de dovecot"
grep -qF "CORREGIBLES: 1 4" <<< "$sal2" && echo "  el recorte no cambia el recuento" || falla "el recorte cambio el recuento"

mkdir -p "$TMP/limpio"; echo '{"Results":[{"Target":"x"}]}' > "$TMP/limpio/trivy-a.json"
sal3="$(bash "$TOOL" informe "$TMP/limpio" 2>&1)"
grep -qF "CORREGIBLES: 0 0" <<< "$sal3" && grep -qF "Ninguna vulnerabilidad critica ni alta" <<< "$sal3" \
  && echo "  sin hallazgos: recuento cero y mensaje" || falla "sin hallazgos no dio CORREGIBLES: 0 0 y el mensaje"

mkdir -p "$TMP/malo"; echo 'no es json' > "$TMP/malo/trivy-a.json"
if sal4="$(bash "$TOOL" informe "$TMP/malo" 2>&1)"; then falla "un fichero que no es JSON paso callado"
elif grep -qF "no es un JSON de Trivy" <<< "$sal4"; then echo "  un fichero que no es JSON falla y lo dice"
else falla "un fichero que no es JSON fallo sin decir por que: $sal4"; fi
mkdir -p "$TMP/vacio"
if sal5="$(bash "$TOOL" informe "$TMP/vacio" 2>&1)"; then falla "una carpeta sin resultados paso callada"
elif grep -qF "no hay trivy-*.json" <<< "$sal5"; then echo "  una carpeta sin resultados falla y lo dice"
else falla "una carpeta sin resultados fallo sin decir por que: $sal5"; fi

echo ""
[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: el informe de vulnerabilidades de los motores cuenta cada CVE una vez y falla ante entradas rotas."
