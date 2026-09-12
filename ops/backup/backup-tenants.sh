#!/usr/bin/env bash
# Respaldo por empresa: un archivo por base de datos, no un volcado del servidor.
#
# El diseño es una base por empresa, así que el respaldo tiene que respetarlo: restaurar
# a una empresa de una foto del servidor entero significa devolver a TODAS las demás al
# pasado. Un archivo por base es lo que permite recuperar a una sola sin tocar al resto.
#
# Verifica cada volcado antes de darlo por bueno: un archivo que pg_restore no puede leer
# ocupa disco pero no es un respaldo, y eso solo se descubre el día que hace falta.
#
# Uso:
#   ops/backup/backup-tenants.sh                 # todas las bases
#   ops/backup/backup-tenants.sh mail_tenant_demo # una sola
#
# Credenciales: ops/db/pg-credentials.sh (contrasena del almacen, resto del .env).
# Variables propias:
#   BACKUP_DIR          destino local (por defecto /opt/core-force-mail/backups)
#   BACKUP_S3_BUCKET    bucket de copia externa; sin él solo queda la copia local
#   BACKUP_KEEP_DAYS    días de retención local (por defecto 3; el histórico vive en S3)
set -uo pipefail

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"
KEEP_DAYS="${BACKUP_KEEP_DAYS:-3}"

# Las credenciales salen del resolvedor comun: la contrasena del almacen de secretos, el
# host y el usuario del .env. Leerlas aqui a mano fue lo que dejo este respaldo sin
# credenciales el dia que el .env dejo de tenerlas.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../db/pg-credentials.sh" || exit 1
# shellcheck source=/dev/null
. "$SCRIPT_DIR/report-metric.sh"
BUCKET="${BACKUP_S3_BUCKET:-$(cf_read_env BACKUP_S3_BUCKET)}"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dest="$BACKUP_DIR/$stamp"
mkdir -p "$dest" || { echo "FALLA: no se pudo crear $dest" >&2; exit 1; }

if [[ $# -gt 0 ]]; then
  dbs=("$@")
else
  # A proposito NO se usa cf_tenant_databases: el respaldo se queda con todo lo que parezca
  # nuestro, incluido mail_registry y cualquier base que exista sin estar declarada como
  # empresa. Respaldar de mas cuesta disco; respaldar de menos se descubre el dia que hace
  # falta. Las migraciones si van por el registro, porque ahi aplicar de mas es un error.
  mapfile -t dbs < <(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d postgres -At \
    -c "SELECT datname FROM pg_database WHERE datname LIKE 'mail_%' AND NOT datistemplate ORDER BY datname")
fi

if [[ ${#dbs[@]} -eq 0 ]]; then
  echo "FALLA: no se encontró ninguna base que respaldar" >&2
  exit 1
fi

echo "Respaldo $stamp -> $dest (${#dbs[@]} bases)"
fail=0
for db in "${dbs[@]}"; do
  [[ -z "$db" ]] && continue
  file="$dest/$db.dump"
  if ! pg_dump -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$db" -Fc -f "$file" 2>"$file.err"; then
    echo "  FALLA: $db no se pudo volcar"; sed 's/^/      /' "$file.err" | head -3
    fail=1; continue
  fi
  rm -f "$file.err"

  # Un volcado que pg_restore no sabe leer no es un respaldo. Se comprueba aquí, no el
  # día de la emergencia.
  objects="$(pg_restore --list "$file" 2>/dev/null | grep -c ';' || true)"
  if [[ "${objects:-0}" -lt 10 ]]; then
    echo "  FALLA: $db produjo un volcado ilegible o vacío ($objects entradas)"
    fail=1; continue
  fi

  size="$(du -h "$file" | cut -f1)"
  echo "  OK $db ($size, $objects objetos)"

  if [[ -n "$BUCKET" ]]; then
    if ! aws s3 cp "$file" "s3://$BUCKET/postgres/$db/$stamp.dump" --only-show-errors; then
      echo "  AVISO: $db quedó respaldado en disco pero no subió a S3"
      fail=1
    fi
  fi
done

[[ -z "$BUCKET" ]] && echo "AVISO: BACKUP_S3_BUCKET sin definir; solo hay copia local, que se pierde con el servidor"

# Retención local: el histórico vive en S3. Nunca borra el respaldo de esta corrida.
if [[ "$KEEP_DAYS" =~ ^[0-9]+$ ]]; then
  find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -mtime "+$KEEP_DAYS" \
    ! -path "$dest" -exec rm -rf {} + 2>/dev/null || true
fi

cf_publicar_metrica respaldo "$fail" "${#dbs[@]}"

if [[ $fail -ne 0 ]]; then
  echo "RESPALDO INCOMPLETO: revisa las líneas FALLA/AVISO de arriba" >&2
  exit 1
fi
echo "RESPALDO OK: ${#dbs[@]} bases en $dest${BUCKET:+ y en s3://$BUCKET/postgres/}"
