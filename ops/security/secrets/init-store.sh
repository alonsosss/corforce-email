#!/usr/bin/env bash
# Crea el almacen cifrado de secretos y, si hace falta, su frase.
#
# Uso:
#   ops/security/secrets/init-store.sh                        # almacen vacio {}
#   ops/security/secrets/init-store.sh --from-env .env            # simulacion: delega en push-secrets.sh
#   ops/security/secrets/init-store.sh --from-env .env --apply    # migra de verdad
#
# La frase (SECRETS_STORE_PASSPHRASE_FILE): si el fichero ya existe (0600, del usuario que
# ejecuta) se reutiliza tal cual. Si no existe, se genera con `openssl rand -base64 48` (el mismo
# criterio que BACKUP_ENCRYPTION_PASSPHRASE) y se escribe ahi.
#
# ES EL UNICO ACCESO A LOS SECRETOS DE LA PLATAFORMA: sin ella el almacen es irrecuperable, ni
# siquiera con acceso root al servidor. Hay que copiarla a un gestor de contrasenas del equipo o
# a otro sitio FUERA de este servidor, y fuera de cualquier respaldo cifrado con esta misma
# frase: si el servidor y ese respaldo se pierden juntos, la copia de la frase dentro del propio
# respaldo no sirve de nada. No se imprime la frase, solo la ruta del fichero que la contiene.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./store.sh
. "$SCRIPT_DIR/store.sh"

FROM_ENV=""
APPLY=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from-env) FROM_ENV="${2:?--from-env necesita la ruta de un .env}"; shift 2 ;;
    --apply) APPLY="--apply"; shift ;;
    *) echo "opcion desconocida: $1" >&2; exit 1 ;;
  esac
done
command -v gpg >/dev/null || { echo "init-store: falta gpg (paquete gnupg)" >&2; exit 1; }
command -v openssl >/dev/null || { echo "init-store: falta openssl" >&2; exit 1; }

mkdir -p "$(dirname "$STORE_PASSPHRASE_FILE")"
if [[ -f "$STORE_PASSPHRASE_FILE" ]]; then
  _store_frase >/dev/null || exit 1
  echo "init-store: reutilizando la frase existente en $STORE_PASSPHRASE_FILE"
else
  umask 077
  openssl rand -base64 48 > "$STORE_PASSPHRASE_FILE"
  chmod 600 "$STORE_PASSPHRASE_FILE"
  cat >&2 <<EOF
init-store: frase nueva generada en $STORE_PASSPHRASE_FILE (0600, $(id -un)).

  ESTA FRASE ES EL UNICO ACCESO A LOS SECRETOS DE LA PLATAFORMA (DOVECOT_MASTER_PASS,
  MAIL_ENCRYPTION_KEY, JWT_SIGNING_KEY, POSTGRES_PASSWORD y las demas). Sin ella el almacen es
  irrecuperable, ni siquiera con acceso root a este servidor.

  Copiala AHORA a un gestor de contrasenas del equipo o a otro sitio FUERA de este servidor.
  NO la guardes solo dentro de un respaldo cifrado con esta misma frase: si el servidor y ese
  respaldo se pierden juntos, esa copia tampoco se puede leer. Necesitas un canal aparte.
EOF
fi

if [[ -f "$STORE_FILE" ]]; then
  echo "init-store: ya existe $STORE_FILE; no se toca (usa push-secrets.sh o add-secret.sh para modificarlo)"
  exit 0
fi

if [[ -n "$FROM_ENV" ]]; then
  echo "init-store: delegando en push-secrets.sh para migrar $FROM_ENV"
  exec "$SCRIPT_DIR/push-secrets.sh" "$FROM_ENV" $APPLY
fi

printf '{}' | store_escribir_json
echo "init-store: almacen vacio creado en $STORE_FILE"
