#!/usr/bin/env bash
# Respaldo de los volumenes de correo de deploy/mail: los buzones y las claves de mail_crypt.
#
#   ops/backup/backup-mail-volumes.sh
#
# Que se respalda y por que (docs/Operacion_Despliegue.md, 6):
#   - crypt-vol: la clave global de mail_crypt de Dovecot (ecprivkey.pem y ecpubkey.pem). Dovecot
#     cifra cada mensaje y cada adjunto con ella: sin esta clave los buzones respaldados son
#     ilegibles. Se comprueba que la privada corresponde a la publica.
#   - vmail-vol: los buzones (Maildir). Se archiva en caliente: Maildir escribe en tmp/ y renombra,
#     asi que ningun fichero queda a medias, pero un mensaje que cambia de carpeta o de marcas
#     durante el archivado puede faltar en ESTA copia (esta en la siguiente). tar lo avisa con el
#     codigo 1, que aqui es un aviso y no un fallo. Se excluye _garbage (lo borrado).
# El resto de volumenes del proyecto no se respalda: se regenera o no tiene valor tras un desastre
# (indices, cola, firmas de ClamAV, certificados, Redis de los motores).
#
# Cada archivo se lee entero antes de darlo por bueno y deja su suma .sha256. La copia externa es
# la de ops/backup/destino-externo.sh; crypt-vol solo sale del servidor cifrado, tambien a AWS: es
# la clave que protege todo el correo.
#
# Variables (entorno o .env):
#   BACKUP_DIR              raiz local; el correo va en <BACKUP_DIR>/correo/<sello>
#   BACKUP_KEEP_DAYS        dias de retencion local (por defecto 3)
#   MAIL_COMPOSE_PROJECT    proyecto de compose de deploy/mail (por defecto mail)
#   BACKUP_MAIL_VOLUMES     volumenes, sin prefijo (por defecto "crypt-vol vmail-vol")
#   BACKUP_ARCHIVE_IMAGE    imagen con GNU tar (por defecto debian bookworm-slim fijada por digest)
set -uo pipefail
umask 077

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/report-metric.sh"

TRABAJO=respaldo_correo
CF_BACKUP_ARCHIVE_IMAGE="${BACKUP_ARCHIVE_IMAGE:-debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171}"

abortar() {
  echo "FALLA: $*" >&2
  cf_publicar_metrica "$TRABAJO" 1 0
  exit 1
}

# Solo configuracion: este trabajo no necesita credenciales de base.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../maintenance/entorno-despliegue.sh" || abortar "no se pudo leer la configuracion del despliegue"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/destino-externo.sh"

BACKUP_DIR="$(cf_read_env BACKUP_DIR)"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"
KEEP_DAYS="$(cf_read_env BACKUP_KEEP_DAYS)"
KEEP_DAYS="${KEEP_DAYS:-3}"
PROYECTO="$(cf_read_env MAIL_COMPOSE_PROJECT)"
PROYECTO="${PROYECTO:-mail}"
VOLUMENES="$(cf_read_env BACKUP_MAIL_VOLUMES)"
VOLUMENES="${VOLUMENES:-crypt-vol vmail-vol}"

[[ "$KEEP_DAYS" =~ ^[0-9]+$ && "$KEEP_DAYS" -ge 1 ]] || abortar "BACKUP_KEEP_DAYS debe ser un entero de 1 en adelante"
[[ "$PROYECTO" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || abortar "MAIL_COMPOSE_PROJECT no es un nombre de proyecto de compose"
command -v docker >/dev/null || abortar "sin docker no se leen los volumenes"
command -v openssl >/dev/null || abortar "sin openssl no se comprueba la clave de mail_crypt"

externo_mal=0
if ! cf_externo_cargar; then
  externo_mal=1
  CF_S3_BUCKET=""
fi

mkdir -p "$BACKUP_DIR/correo" || abortar "no se pudo crear $BACKUP_DIR/correo"
exec 8>"$BACKUP_DIR/.respaldo.lock"
flock -w 7200 8 || abortar "otro respaldo sigue en curso tras dos horas"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dest="$BACKUP_DIR/correo/$stamp"
mkdir -m 0700 "$dest" || abortar "no se pudo crear $dest"

# comprobar_clave_crypt <archivo>: la privada de mail_crypt existe y corresponde a la publica.
comprobar_clave_crypt() {
  local tmp rc=0
  tmp="$(mktemp -d)" || return 1
  if ! tar -xzf "$1" -C "$tmp" --no-same-owner ./ecprivkey.pem ./ecpubkey.pem 2>/dev/null; then
    echo "  FALLA: el archivo no trae ecprivkey.pem y ecpubkey.pem"
    rc=1
  elif [[ "$(openssl pkey -in "$tmp/ecprivkey.pem" -pubout 2>/dev/null)" != "$(openssl pkey -pubin -in "$tmp/ecpubkey.pem" 2>/dev/null)" ||
    ! -s "$tmp/ecpubkey.pem" ]]; then
    echo "  FALLA: la clave privada de mail_crypt no corresponde a la publica (o no se lee)"
    rc=1
  fi
  rm -rf "$tmp"
  return $rc
}

echo "Respaldo de correo $stamp -> $dest (proyecto $PROYECTO: $VOLUMENES)"
fail=$externo_mal
fail_local=0
n=0
for vol in $VOLUMENES; do
  [[ "$vol" =~ ^[a-z0-9][a-z0-9_.-]*$ ]] || { echo "  FALLA: volumen con nombre inesperado: $(printf '%q' "$vol")"; fail=1; fail_local=1; continue; }
  completo="${PROYECTO}_$vol"
  if ! docker volume inspect "$completo" >/dev/null 2>&1; then
    echo "  FALLA: no existe el volumen $completo (MAIL_COMPOSE_PROJECT=$PROYECTO)"
    fail=1; fail_local=1; continue
  fi
  archivo="$dest/$vol.tar.gz"
  # Solo lectura, sin red y con la unica capacidad de leer ficheros de otros usuarios (vmail y
  # dovecot). El archivo sale por la salida estandar: lo crea el usuario del respaldo, no root.
  docker run --rm --network none --read-only --cap-drop ALL --cap-add DAC_READ_SEARCH \
    --security-opt no-new-privileges -v "$completo:/volumen:ro" "$CF_BACKUP_ARCHIVE_IMAGE" \
    tar --numeric-owner --warning=no-file-changed --warning=no-file-removed --exclude=./_garbage \
    -czf - -C /volumen . >"$archivo" 2>"$archivo.err"
  rc=$?
  if [[ $rc -eq 1 ]]; then
    echo "  aviso: $vol cambio durante el archivado; lo que se movio en ese instante esta en la proxima copia"
  elif [[ $rc -ne 0 ]]; then
    echo "  FALLA: $vol no se pudo archivar (codigo $rc)"; sed 's/^/      /' "$archivo.err" | head -3
    rm -f "$archivo"
    fail=1; fail_local=1; continue
  fi
  rm -f "$archivo.err"

  entradas="$(tar -tzf "$archivo" 2>/dev/null | wc -l)"
  if ! gzip -t "$archivo" 2>/dev/null || [[ "$entradas" -lt 2 ]]; then
    echo "  FALLA: $vol produjo un archivo ilegible o vacio ($entradas entradas)"
    mv -f "$archivo" "$archivo.ilegible"
    fail=1; fail_local=1; continue
  fi
  if [[ "$vol" == crypt-vol ]] && ! comprobar_clave_crypt "$archivo"; then
    mv -f "$archivo" "$archivo.ilegible"
    fail=1; fail_local=1; continue
  fi
  (cd "$dest" && sha256sum "$vol.tar.gz" >"$vol.tar.gz.sha256") || { echo "  FALLA: $vol sin suma"; fail=1; fail_local=1; continue; }
  n=$((n + 1))
  echo "  OK $vol ($(du -h "$archivo" | cut -f1), $entradas entradas)"

  if cf_externo_activo; then
    if [[ "$vol" == crypt-vol ]] && ! cf_externo_cifra; then
      echo "  FALLA: crypt-vol no sale del servidor sin cifrar (falta BACKUP_ENCRYPTION_PASSPHRASE)"
      fail=1; continue
    fi
    origen="$archivo"
    clave="correo/$vol/$stamp.tar.gz"
    if cf_externo_cifra; then
      cf_cifrar "$archivo" "$archivo.gpg" || { echo "  AVISO: $vol no se pudo cifrar; no sale del servidor"; fail=1; continue; }
      origen="$archivo.gpg"
      clave="$clave.gpg"
    fi
    cf_externo_subir "$origen" "$clave" || { echo "  AVISO: $vol quedo en disco pero no subio a s3://$CF_S3_BUCKET"; fail=1; }
    [[ "$origen" == "$archivo.gpg" ]] && rm -f "$archivo.gpg"
  fi
done

if [[ $fail_local -eq 0 ]]; then
  find "$BACKUP_DIR/correo" -mindepth 1 -maxdepth 1 -type d -name '[0-9]*T[0-9]*Z' -mtime "+$KEEP_DAYS" \
    ! -path "$dest" -exec rm -rf {} + 2>/dev/null || true
else
  echo "AVISO: no se aplica la retencion local porque esta corrida tuvo fallos"
fi

cf_publicar_metrica "$TRABAJO" "$fail" "$n"
if [[ $fail -ne 0 ]]; then
  echo "RESPALDO DE CORREO INCOMPLETO: revisa las lineas FALLA/AVISO de arriba" >&2
  exit 1
fi
echo "RESPALDO DE CORREO OK: $n volumenes en $dest${CF_S3_BUCKET:+ y en s3://$CF_S3_BUCKET/correo/}"
