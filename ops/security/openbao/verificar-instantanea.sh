#!/usr/bin/env bash
# Comprueba que una instantanea de OpenBao se puede restaurar con la llave de desbloqueo del
# servidor y que devuelve los secretos (docs/adr/0011, "Respaldo").
#
#   ops/security/openbao/verificar-instantanea.sh <fichero.snap>
#
# Levanta una instancia desechable (otro puerto, datos en un directorio temporal), restaura ahi
# y lee el documento con la credencial de despliegue. Nunca toca la instancia de produccion: el
# cliente se niega a restaurar sobre una instancia ya inicializada. Lo llama verify-restore.sh.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SECRETS_SCRIPTS="$(cd "$DIR/../secrets" && pwd)"
SECRETS_DIR="${SECRETS_DIR:-/opt/core-force-mail/secrets}"
LLAVE_DIR="$SECRETS_DIR/openbao-llave"
export OPENBAO_CRED_DIR="${OPENBAO_CRED_DIR:-$SECRETS_DIR/openbao}"
SNAP="${1:?uso: verificar-instantanea.sh <fichero.snap>}"

[[ -f "$SNAP" ]] || { echo "verificar-instantanea: no existe $SNAP" >&2; exit 1; }
[[ -f "$LLAVE_DIR/desbloqueo.key" && -s "$LLAVE_DIR/id" ]] || { echo "verificar-instantanea: falta la llave en $LLAVE_DIR" >&2; exit 1; }

imagen="$(sed -n -E 's/^[[:space:]]*image:[[:space:]]*(openbao\/[^[:space:]]+).*/\1/p' "$DIR/docker-compose.yml")"
[[ -n "$imagen" ]] || { echo "verificar-instantanea: no se encontro la imagen en docker-compose.yml" >&2; exit 1; }

puerto="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
cluster="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
tmp="$(mktemp -d)"
nombre="cf-openbao-verificacion-$$"
trap 'docker rm -f "$nombre" >/dev/null 2>&1; rm -rf "$tmp"' EXIT
mkdir -m 0700 "$tmp/datos" "$tmp/config"
# La plantilla fija el cluster en el puerto siguiente; aqui se sustituyen los dos por libres.
sed -e "s/127.0.0.1:__PUERTO_CLUSTER__/127.0.0.1:$cluster/g" -e "s/__PUERTO__/$puerto/g" \
  -e "s/__ID_LLAVE__/$(cat "$LLAVE_DIR/id")/g" "$DIR/config.hcl.tmpl" >"$tmp/config/config.hcl"

docker run -d --name "$nombre" --network host --user "$(id -u):$(id -g)" --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --tmpfs /tmp:size=16m \
  -v "$tmp/datos:/openbao/data" -v "$tmp/config:/openbao/config:ro" -v "$LLAVE_DIR:/openbao/llave:ro" \
  "$imagen" server -config=/openbao/config/config.hcl >/dev/null

export OPENBAO_ADDR="http://127.0.0.1:$puerto"
for _ in $(seq 1 60); do
  python3 -c 'import sys, urllib.request; urllib.request.urlopen(sys.argv[1] + "/v1/sys/seal-status", timeout=2)' \
    "$OPENBAO_ADDR" 2>/dev/null && break
  sleep 1
done
if ! python3 -c 'import sys, urllib.request; urllib.request.urlopen(sys.argv[1] + "/v1/sys/seal-status", timeout=2)' "$OPENBAO_ADDR" 2>/dev/null; then
  echo "verificar-instantanea: la instancia desechable no respondio; su registro:" >&2
  docker logs "$nombre" 2>&1 | tail -15 >&2
  exit 1
fi
python3 "$SECRETS_SCRIPTS/openbao.py" verificar-instantanea "$SNAP"
