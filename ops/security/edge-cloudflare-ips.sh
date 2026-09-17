#!/usr/bin/env bash
# Compara los rangos de Cloudflare versionados (selfhosted/edge/cloudflare-ips.txt) con los que
# publica Cloudflare, y los actualiza a peticion.
#
#   ops/security/edge-cloudflare-ips.sh --comprobar   # sale con 1 si difieren (no toca nada)
#   ops/security/edge-cloudflare-ips.sh --escribir    # reescribe la lista; commitear y desplegar
#
# Se corre en un puesto de trabajo, no en el servidor: la lista es parte del repositorio y el
# proxy de borde la lee al arrancar. Un rango nuevo de Cloudflare que falte deja fuera (403, o sin
# IP real) a los visitantes que entren por el; uno retirado que siga aqui daria confianza a quien
# ya no es Cloudflare. Todo lo descargado se valida como CIDR antes de escribir nada.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LISTA="$ROOT/selfhosted/edge/cloudflare-ips.txt"
URL_V4="${CLOUDFLARE_IPS_V4_URL:-https://www.cloudflare.com/ips-v4}"
URL_V6="${CLOUDFLARE_IPS_V6_URL:-https://www.cloudflare.com/ips-v6}"

case "${1:-}" in
  --comprobar | --escribir) MODO="$1" ;;
  *) echo "uso: $0 --comprobar | --escribir" >&2; exit 2 ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsS --max-time 30 "$URL_V4" >"$TMP/v4"
curl -fsS --max-time 30 "$URL_V6" >"$TMP/v6"

python3 - "$TMP/v4" "$TMP/v6" "$LISTA" "$MODO" <<'PY'
import datetime
import ipaddress
import sys

v4, v6, lista, modo = sys.argv[1:5]


def rangos(ruta, version):
    out = []
    for linea in open(ruta, encoding="utf-8"):
        linea = linea.strip()
        if not linea:
            continue
        red = ipaddress.ip_network(linea, strict=True)
        if red.version != version or red.is_private:
            raise SystemExit(f"edge-cloudflare-ips: rango inesperado en la descarga: {linea}")
        out.append(str(red))
    if not out:
        raise SystemExit(f"edge-cloudflare-ips: descarga IPv{version} vacia")
    return out


publicados = rangos(v4, 4) + rangos(v6, 6)
cabecera, actuales = [], []
for linea in open(lista, encoding="utf-8"):
    s = linea.strip()
    if s.startswith("#") or not s:
        if not actuales:
            cabecera.append(linea.rstrip("\n"))
        continue
    actuales.append(s)

sobran = sorted(set(actuales) - set(publicados))
faltan = sorted(set(publicados) - set(actuales))
if not sobran and not faltan:
    print("edge-cloudflare-ips: la lista versionada coincide con la publicada")
    sys.exit(0)
for r in faltan:
    print(f"  falta: {r}")
for r in sobran:
    print(f"  sobra: {r}")
if modo == "--comprobar":
    sys.exit(1)

hoy = datetime.date.today().isoformat()
cabecera = [l if not l.startswith("# Revisado el ") else
            f"# Revisado el {hoy}. Cloudflare los cambia rara vez y avisa con antelacion; comprobarlos con"
            for l in cabecera]
with open(lista, "w", encoding="utf-8") as fh:
    fh.write("\n".join(cabecera).rstrip("\n") + "\n")
    fh.write("\n".join(publicados) + "\n")
print(f"edge-cloudflare-ips: {lista} actualizada; commitear y desplegar (recrea el proxy de borde)")
PY
