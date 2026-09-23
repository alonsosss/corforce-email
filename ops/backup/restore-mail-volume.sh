#!/usr/bin/env bash
# Restaura un volumen de correo (buzones o claves de mail_crypt) o de la plataforma (minio-data, los
# objetos de MinIO) desde su archivo.
#
# Por defecto restaura a un volumen NUEVO (`<proyecto>_<volumen>_restore_<fecha>`), que es lo que
# hace falta casi siempre: sacar un buzon o comprobar que la copia sirve sin tocar el correo vivo.
# Sobrescribir el volumen que usan los motores exige --force, que ningun contenedor lo tenga
# montado (Dovecot escribiendo encima de una restauracion deja el buzon a medias) y confirmacion
# escrita.
#
# Uso:
#   ops/backup/restore-mail-volume.sh vmail-vol                       # ultimo archivo local
#   ops/backup/restore-mail-volume.sh crypt-vol /ruta/crypt-vol.tar.gz
#   ops/backup/restore-mail-volume.sh vmail-vol s3://<bucket>/correo/vmail-vol/<sello>.tar.gz.gpg
#   ops/backup/restore-mail-volume.sh vmail-vol --into mail_prueba
#   ops/backup/restore-mail-volume.sh vmail-vol --force               # SOBRESCRIBE el vivo
#   ops/backup/restore-mail-volume.sh minio-data --force              # con minio parado
#
# Comprueba la suma .sha256 antes de extraer. Tras restaurar los buzones sobre el volumen vivo hay
# que reconstruir los indices (estan en otro volumen, que no se respalda):
#   docker exec dovecot-mail doveadm force-resync -A '*'
set -uo pipefail
umask 077

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }
[[ $# -lt 1 ]] && usage

VOL="$1"; shift
ARCHIVO=""
DESTINO=""
FORCE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --into) DESTINO="${2:-}"; shift 2 || usage ;;
    --force) FORCE=1; shift ;;
    -h|--help) usage ;;
    *) ARCHIVO="$1"; shift ;;
  esac
done
[[ "$VOL" =~ ^[a-z0-9][a-z0-9_.-]*$ ]] || { echo "FALLA: nombre de volumen inesperado: $(printf '%q' "$VOL")" >&2; exit 1; }
[[ -z "$DESTINO" || "$DESTINO" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || { echo "FALLA: nombre de destino inesperado: $(printf '%q' "$DESTINO")" >&2; exit 1; }

# shellcheck source=/dev/null
. "$SCRIPT_DIR/../maintenance/entorno-despliegue.sh" || exit 1
# shellcheck source=/dev/null
. "$SCRIPT_DIR/destino-externo.sh"

BACKUP_DIR="$(cf_read_env BACKUP_DIR)"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"
PROYECTO="$(cf_read_env MAIL_COMPOSE_PROJECT)"
PROYECTO="${PROYECTO:-mail}"
# Los volumenes de la plataforma (BACKUP_PLATFORM_VOLUMES, o minio-data) son del proyecto de la
# plataforma, no del de los motores.
VOLUMENES_APP="$(cf_read_env BACKUP_PLATFORM_VOLUMES)"
PARAR="docker compose -p $PROYECTO -f deploy/mail/docker-compose.mail.yml stop dovecot-mail postfix-mail"
if [[ " ${VOLUMENES_APP:-minio-data} " == *" $VOL "* ]]; then
  PROYECTO="$(cf_read_env COMPOSE_PROJECT_NAME)"
  PROYECTO="${PROYECTO:-app}"
  PARAR="ops/security/secrets/with-secrets.sh docker compose \$(ops/maintenance/perfil-despliegue.sh --compose) stop minio"
fi
[[ "$PROYECTO" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || { echo "FALLA: proyecto de compose inesperado: $(printf '%q' "$PROYECTO")" >&2; exit 1; }
IMAGEN="${BACKUP_ARCHIVE_IMAGE:-debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171}"

tmp=""
cleanup() { [[ -n "$tmp" ]] && rm -rf "$tmp"; }
trap cleanup EXIT

if [[ -z "$ARCHIVO" ]]; then
  ARCHIVO="$(find "$BACKUP_DIR/correo" -mindepth 2 -maxdepth 2 -type f -name "$VOL.tar.gz" -printf '%T@ %p\n' 2>/dev/null | sort -rn | head -1 | cut -d' ' -f2-)"
  [[ -n "$ARCHIVO" ]] || { echo "FALLA: no hay archivo local de $VOL en $BACKUP_DIR/correo" >&2; exit 1; }
  echo "Archivo mas reciente: $ARCHIVO"
elif [[ "$ARCHIVO" == s3://* ]]; then
  cf_externo_cargar || exit 1
  if ! mkdir -p "$BACKUP_DIR/correo" || ! tmp="$(mktemp -d "$BACKUP_DIR/correo/descarga-XXXXXX")"; then
    echo "FALLA: no se pudo preparar la descarga en $BACKUP_DIR/correo" >&2
    exit 1
  fi
  bajado="$tmp/$(basename "$ARCHIVO")"
  cf_externo_bajar "$ARCHIVO" "$bajado" || { echo "FALLA: no se pudo bajar $ARCHIVO" >&2; exit 1; }
  if [[ "$bajado" == *.gpg ]]; then
    cf_descifrar "$bajado" "${bajado%.gpg}" || { echo "FALLA: $ARCHIVO no descifra: frase distinta o archivo dañado" >&2; exit 1; }
    rm -f "$bajado"
    bajado="${bajado%.gpg}"
  fi
  ARCHIVO="$bajado"
fi
[[ -f "$ARCHIVO" ]] || { echo "FALLA: no existe $ARCHIVO" >&2; exit 1; }
ARCHIVO="$(cd "$(dirname "$ARCHIVO")" && pwd)/$(basename "$ARCHIVO")"

if [[ -f "$ARCHIVO.sha256" ]]; then
  if ! (cd "$(dirname "$ARCHIVO")" && sha256sum --status -c "$(basename "$ARCHIVO").sha256"); then
    echo "FALLA: $ARCHIVO no coincide con su suma: el archivo esta dañado y no se restaura" >&2
    exit 1
  fi
  echo "Suma de verificación correcta: $ARCHIVO"
else
  echo "  aviso: $ARCHIVO no tiene suma .sha256 (descargado o de un respaldo anterior)"
fi
gzip -t "$ARCHIVO" 2>/dev/null || { echo "FALLA: $ARCHIVO no es un gzip legible" >&2; exit 1; }

if [[ $FORCE -eq 1 ]]; then
  DESTINO="${DESTINO:-${PROYECTO}_$VOL}"
else
  DESTINO="${DESTINO:-${PROYECTO}_${VOL}_restore_$(date -u +%Y%m%d%H%M)}"
fi

if docker volume inspect "$DESTINO" >/dev/null 2>&1; then
  if [[ $FORCE -ne 1 ]]; then
    echo "FALLA: el volumen $DESTINO ya existe. Usa --into con otro nombre, o --force para reemplazarlo." >&2
    exit 1
  fi
  usuarios="$(docker ps -q --filter "volume=$DESTINO" | wc -l)"
  if [[ "$usuarios" -ne 0 ]]; then
    echo "FALLA: $usuarios contenedores en marcha montan $DESTINO. Paralos antes:" >&2
    echo "  $PARAR" >&2
    exit 1
  fi
  echo
  echo "  Vas a REEMPLAZAR el contenido de '$DESTINO'. Lo que tenga ahora se pierde."
  echo "  Origen: $ARCHIVO"
  read -r -p "  Escribe el nombre del volumen para confirmar: " confirm
  [[ "$confirm" == "$DESTINO" ]] || { echo "Cancelado."; exit 1; }
else
  docker volume create "$DESTINO" >/dev/null || { echo "FALLA: no se pudo crear el volumen $DESTINO" >&2; exit 1; }
fi

echo "Restaurando $ARCHIVO -> volumen $DESTINO ..."
# Como root y con los uid/gid del archivo (vmail 5000, dovecot 401, minio 10001): leen por uid.
if ! docker run --rm -i --network none --security-opt no-new-privileges -v "$DESTINO:/volumen" \
  "$IMAGEN" tar --numeric-owner -xzp -f - -C /volumen <"$ARCHIVO"; then
  echo "FALLA: la extracción en $DESTINO no termino bien" >&2
  exit 1
fi

esperadas="$(tar -tzf "$ARCHIVO" | wc -l)"
restauradas="$(docker run --rm --network none -v "$DESTINO:/volumen:ro" "$IMAGEN" \
  sh -c 'find /volumen -mindepth 1 | wc -l')"
echo "Contenido restaurado en $DESTINO: $restauradas entradas (el archivo trae $esperadas)"
if [[ "$restauradas" -lt $((esperadas - 1)) ]]; then
  echo "FALLA: faltan entradas en el volumen restaurado" >&2
  exit 1
fi
echo "RESTAURACIÓN OK: $DESTINO"
[[ "$VOL" == vmail-vol ]] && echo "Recuerda reconstruir los indices: docker exec dovecot-mail doveadm force-resync -A '*'"
exit 0
