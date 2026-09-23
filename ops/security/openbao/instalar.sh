#!/usr/bin/env bash
# Instala o actualiza OpenBao, el almacen de secretos de la plataforma (docs/adr/0011).
#
#   ops/security/openbao/instalar.sh
#
# Se ejecuta en el servidor como el usuario que despliega, nunca como root. Idempotente: la
# primera vez genera la llave de desbloqueo, levanta el contenedor, lo inicializa, aplica la
# configuracion del repositorio (montaje, auditoria, politicas y roles) y revoca el token root;
# las siguientes solo actualizan la imagen y reaplican la configuracion con la credencial de
# administracion. No toca los secretos: pasarlos desde el almacen gpg es migrar.sh.
#
# Variables (todas con valor por defecto):
#   OPENBAO_BASE      datos y configuracion renderizada (/opt/core-force-mail/openbao)
#   SECRETS_DIR       directorio de secretos del servidor (/opt/core-force-mail/secrets)
#   OPENBAO_PUERTO    puerto de loopback de la API (8200); el de cluster es el siguiente
#   OPENBAO_PROYECTO, OPENBAO_CONTENEDOR  nombres de Compose (los cambia solo la prueba)
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SECRETS_SCRIPTS="$(cd "$DIR/../secrets" && pwd)"
OPENBAO_BASE="${OPENBAO_BASE:-/opt/core-force-mail/openbao}"
SECRETS_DIR="${SECRETS_DIR:-/opt/core-force-mail/secrets}"
OPENBAO_PUERTO="${OPENBAO_PUERTO:-8200}"
PUERTO_CLUSTER=$((OPENBAO_PUERTO + 1))
LLAVE_DIR="$SECRETS_DIR/openbao-llave"
LLAVE="$LLAVE_DIR/desbloqueo.key"
export OPENBAO_CRED_DIR="${OPENBAO_CRED_DIR:-$SECRETS_DIR/openbao}"
OPENBAO_PROYECTO="${OPENBAO_PROYECTO:-core-force-openbao}"
export OPENBAO_CONTENEDOR="${OPENBAO_CONTENEDOR:-core-force-openbao}"
export OPENBAO_ADDR="http://127.0.0.1:$OPENBAO_PUERTO"

die() { echo "openbao/instalar: $*" >&2; exit 1; }

[[ "$(id -u)" -ne 0 ]] || die "no se ejecuta como root: los datos y la llave son del usuario que despliega"
for bin in docker python3 openssl; do
  command -v "$bin" >/dev/null || die "falta $bin"
done
[[ "$OPENBAO_PUERTO" =~ ^[0-9]+$ ]] || die "OPENBAO_PUERTO no es un numero"

umask 077
install -d -m 0700 "$OPENBAO_BASE" "$OPENBAO_BASE/datos" "$OPENBAO_BASE/config" "$SECRETS_DIR" "$LLAVE_DIR" "$OPENBAO_CRED_DIR"

# La llave se genera UNA vez. Sin ella los datos existentes no se descifran nunca: si hay datos y
# falta la llave, generar otra no arregla nada y taparia el problema.
if [[ ! -f "$LLAVE" ]]; then
  if [[ -n "$(ls -A "$OPENBAO_BASE/datos")" ]]; then
    die "hay datos en $OPENBAO_BASE/datos pero falta $LLAVE: repon la llave desde su copia fuera del servidor"
  fi
  openssl rand -out "$LLAVE.tmp" 32
  chmod 600 "$LLAVE.tmp"
  mv -f "$LLAVE.tmp" "$LLAVE"
  printf 'llave-%s\n' "$(date -u +%Y%m%d)" >"$LLAVE_DIR/id"
  echo "openbao/instalar: llave de desbloqueo nueva en $LLAVE"
fi
[[ "$(stat -c '%s' "$LLAVE")" -eq 32 ]] || die "$LLAVE no tiene 32 bytes"
[[ -s "$LLAVE_DIR/id" ]] || die "falta $LLAVE_DIR/id (identificador de la llave)"
id_llave="$(cat "$LLAVE_DIR/id")"
[[ "$id_llave" =~ ^[a-z0-9-]+$ ]] || die "identificador de llave inesperado en $LLAVE_DIR/id"

sed -e "s/__PUERTO__/$OPENBAO_PUERTO/g" -e "s/__PUERTO_CLUSTER__/$PUERTO_CLUSTER/g" \
  -e "s/__ID_LLAVE__/$id_llave/g" "$DIR/config.hcl.tmpl" >"$OPENBAO_BASE/config/config.hcl.tmp"
mv -f "$OPENBAO_BASE/config/config.hcl.tmp" "$OPENBAO_BASE/config/config.hcl"

# El proyecto de OpenBao es la fuente de los secretos del resto: no interpola ninguno y no puede
# arrancar a traves de with-secrets.sh, que depende de el.
OPENBAO_UID="$(id -u)" OPENBAO_GID="$(id -g)" OPENBAO_PUERTO="$OPENBAO_PUERTO" \
  OPENBAO_DATOS="$OPENBAO_BASE/datos" OPENBAO_CONFIG="$OPENBAO_BASE/config" OPENBAO_LLAVE="$LLAVE_DIR" \
  docker compose -p "$OPENBAO_PROYECTO" -f "$DIR/docker-compose.yml" up -d --remove-orphans

for _ in $(seq 1 60); do
  if python3 - "$OPENBAO_ADDR" <<'PY' 2>/dev/null; then break; fi
import sys, urllib.request, urllib.error
try:
    urllib.request.urlopen(sys.argv[1] + "/v1/sys/health", timeout=2)
except urllib.error.HTTPError:
    pass
PY
  sleep 1
done

primera=0
[[ -f "$OPENBAO_CRED_DIR/administracion.secret_id" ]] || primera=1
python3 "$SECRETS_SCRIPTS/openbao.py" inicializar "$DIR/politicas"

# Tras reaplicar la configuracion, desbloqueado por su cuenta: es lo que garantiza que un reinicio
# del servidor no deja la plataforma sin poder desplegar.
docker restart "$OPENBAO_CONTENEDOR" >/dev/null
for _ in $(seq 1 60); do
  python3 "$SECRETS_SCRIPTS/openbao.py" salud >/dev/null 2>&1 && break
  sleep 1
done
python3 "$SECRETS_SCRIPTS/openbao.py" salud || die "OpenBao no se desbloqueo solo tras reiniciarlo"

if [[ $primera -eq 1 ]]; then
  cat <<EOF

openbao/instalar: inicializado. Copia FUERA del servidor (gestor de contrasenas del equipo):
  1. La llave de desbloqueo:  base64 -w0 $LLAVE
     Sin ella, un respaldo de OpenBao no se puede leer en otra maquina.
  2. La clave de recuperacion: cat $OPENBAO_CRED_DIR/recuperacion.key
     Permite generar un token root si se pierden las credenciales de administracion.
     Una vez copiada, borrala del servidor: shred -u $OPENBAO_CRED_DIR/recuperacion.key
Siguiente paso: ops/security/openbao/migrar.sh
EOF
fi
