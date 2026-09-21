#!/usr/bin/env bash
# Comprobaciones de operacion del reparto de secretos (docs/adr/0007). Solo imprime NOMBRES de
# variables, nunca valores.
#
#   ops/security/secrets/verify-scope.sh contenedores [--proyecto app] [--proyecto-motores mail]
#       Lo que recibe cada contenedor EN MARCHA frente a su fila de reparto.tsv: variables de
#       secret-keys.txt con valor que su fila no le da (un contenedor que no se ha recreado desde que
#       cambio el reparto sigue con el fichero entero) y variables obligatorias de su fila vacias.
#       Sale con 1 si hay algo de eso. Necesita acceso a docker, no a los secretos.
#
#   ops/security/secrets/with-secrets.sh ops/security/secrets/verify-scope.sh entorno
#       Antes de recrear: que el valor que Compose interpola es el del fichero materializado. Compose
#       interpola contra el entorno que deja with-secrets.sh, que carga el fichero con el shell; un
#       valor con espacios, comillas, "$" o "#" llegaria al contenedor distinto de como esta en el
#       almacen (antes lo entregaba el env_file, que no pasa por el shell). Sale con 1 si alguno difiere.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_FILE="${SECRET_KEYS_FILE:-$SCRIPT_DIR/secret-keys.txt}"
REPARTO_FILE="${SECRET_REPARTO_FILE:-$SCRIPT_DIR/reparto.tsv}"
SECRETS_FILE="${SECRETS_ENV_FILE:-/dev/shm/core-force-mail/secrets.env}"

modo="${1:-}"
[[ -n "$modo" ]] && shift
PROYECTO=app
PROYECTO_MOTORES=mail
while [[ $# -gt 0 ]]; do
  case "$1" in
    --proyecto) PROYECTO="${2:?--proyecto necesita un valor}"; shift 2 ;;
    --proyecto-motores) PROYECTO_MOTORES="${2:?--proyecto-motores necesita un valor}"; shift 2 ;;
    *) echo "verify-scope: argumento desconocido: $1" >&2; exit 2 ;;
  esac
done

case "$modo" in
  entorno)
    [[ -f "$SECRETS_FILE" ]] || { echo "verify-scope: no existe $SECRETS_FILE (ejecutalo con with-secrets.sh)" >&2; exit 1; }
    dif=()
    while IFS= read -r linea; do
      [[ "$linea" == *=* && "$linea" != \#* ]] || continue
      k="${linea%%=*}"
      [[ "${!k-}" == "${linea#*=}" ]] || dif+=("$k")
    done <"$SECRETS_FILE"
    if [[ ${#dif[@]} -gt 0 ]]; then
      echo "verify-scope: el entorno de Compose difiere del fichero materializado en: ${dif[*]}" >&2
      echo "  El shell de load.sh interpreta el valor (espacios, comillas, \$ o #). Cambia el valor en el almacen" >&2
      echo "  por uno sin esos caracteres (openssl rand -hex 32 / -base64) antes de recrear los servicios." >&2
      exit 1
    fi
    echo "verify-scope: el entorno de Compose coincide con el fichero materializado."
    ;;
  contenedores)
    command -v docker >/dev/null || { echo "verify-scope: falta docker" >&2; exit 1; }
    KEYS_FILE="$KEYS_FILE" REPARTO_FILE="$REPARTO_FILE" PROYECTO="$PROYECTO" PROYECTO_MOTORES="$PROYECTO_MOTORES" python3 - <<'PY'
import os
import subprocess
import sys

def leer(f):
    return [l.rstrip("\n") for l in open(f, encoding="utf-8") if l.strip() and not l.startswith("#")]

claves = {}
for l in leer(os.environ["KEYS_FILE"]):
    claves[l.strip().rstrip("?")] = l.strip().endswith("?")
filas = {}
for l in leer(os.environ["REPARTO_FILE"]):
    c = l.split("\t")
    if c[0] != "operacion":
        filas.setdefault(c[0], set()).add(c[1])

def entorno(servicio):
    for proyecto in dict.fromkeys((os.environ["PROYECTO"], os.environ["PROYECTO_MOTORES"])):
        r = subprocess.run(
            ["docker", "ps", "-q", "--filter", f"label=com.docker.compose.project={proyecto}",
             "--filter", f"label=com.docker.compose.service={servicio}"],
            capture_output=True, text=True)
        ids = r.stdout.split()
        if not ids:
            continue
        r = subprocess.run(["docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", ids[0]],
                           capture_output=True, text=True)
        env = {}
        for l in r.stdout.splitlines():
            k, sep, v = l.partition("=")
            if sep:
                env[k] = v
        return env
    return None

malo = False
for servicio in sorted(filas):
    env = entorno(servicio)
    if env is None:
        print(f"  {servicio}: no esta en marcha")
        continue
    recibe = {k for k in claves if env.get(k, "") != ""}
    extra = sorted(recibe - filas[servicio])
    faltan = sorted(k for k in filas[servicio] if not claves[k] and env.get(k, "") == "")
    if extra or faltan:
        malo = True
        if extra:
            print(f"  {servicio}: recibe de mas: {', '.join(extra)}")
        if faltan:
            print(f"  {servicio}: le faltan (obligatorios): {', '.join(faltan)}")
    else:
        print(f"  {servicio}: OK ({len(recibe)} secretos, los de su fila)")
if malo:
    print("verify-scope: hay contenedores con secretos de mas o sin los suyos; recrealos con with-secrets.sh "
          "(docs/Operacion_Despliegue.md, 2).", file=sys.stderr)
    sys.exit(1)
PY
    ;;
  *)
    echo "uso: $0 contenedores [--proyecto app] [--proyecto-motores mail] | entorno" >&2
    exit 2
    ;;
esac
