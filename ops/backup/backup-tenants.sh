#!/usr/bin/env bash
# Respaldo por empresa: un archivo por base de datos, no un volcado del servidor.
#
# El diseño es una base por empresa, así que el respaldo tiene que respetarlo: restaurar
# a una empresa de una foto del servidor entero significa devolver a TODAS las demás al
# pasado. Un archivo por base es lo que permite recuperar a una sola sin tocar al resto.
# Entran las tres clases: el registro (mail_registry), cada celda (mail_cell_*) y cada
# empresa (mail_tenant_*).
#
# Verifica cada volcado antes de darlo por bueno: un archivo que pg_restore no puede leer
# ocupa disco pero no es un respaldo, y eso solo se descubre el día que hace falta.
#
# Uso:
#   ops/backup/backup-tenants.sh                 # todas las bases
#   ops/backup/backup-tenants.sh mail_tenant_demo # una sola
#
# Credenciales: ops/db/pg-credentials.sh (contrasena del almacen, resto del .env). En el perfil
# autoalojado las herramientas de Postgres corren en contenedor con verify-full (ver ese guion).
# Variables propias (entorno o .env):
#   BACKUP_DIR          destino local (por defecto /opt/core-force-mail/backups)
#   BACKUP_KEEP_DAYS    días de retención local (por defecto 3)
#   BACKUP_S3_*         copia externa opcional: ops/backup/destino-externo.sh
set -uo pipefail
umask 077

APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"

# Las credenciales salen del resolvedor comun: la contrasena del almacen de secretos, el
# host y el usuario del .env. Leerlas aqui a mano fue lo que dejo este respaldo sin
# credenciales el dia que el .env dejo de tenerlas.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/report-metric.sh"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../db/pg-credentials.sh" || { cf_publicar_metrica respaldo 1 0; exit 1; }
# shellcheck source=/dev/null
. "$SCRIPT_DIR/destino-externo.sh"

BACKUP_DIR="${BACKUP_DIR:-$(cf_read_env BACKUP_DIR)}"
BACKUP_DIR="${BACKUP_DIR:-/opt/core-force-mail/backups}"
KEEP_DAYS="${BACKUP_KEEP_DAYS:-$(cf_read_env BACKUP_KEEP_DAYS)}"
KEEP_DAYS="${KEEP_DAYS:-3}"

abortar() {
  echo "FALLA: $*" >&2
  cf_publicar_metrica respaldo 1 0
  exit 1
}

[[ "$KEEP_DAYS" =~ ^[0-9]+$ && "$KEEP_DAYS" -ge 1 ]] || abortar "BACKUP_KEEP_DAYS debe ser un entero de 1 en adelante"
mkdir -p "$BACKUP_DIR" || abortar "no se pudo crear $BACKUP_DIR"
# Una copia externa mal configurada no puede dejar al servidor sin la local: se respalda en disco,
# no se sube nada y la corrida termina en fallo para que se vea.
externo_mal=0
if ! cf_externo_cargar; then
  externo_mal=1
  CF_S3_BUCKET=""
fi

# Un solo respaldo a la vez, y la verificacion espera a que termine: leeria un volcado a medias.
# Este tambien ESPERA, como el de buzones y la verificacion. Con -n abortaba en cuanto encontraba el
# cerrojo tomado, y eso pasa siempre que los dos temporizadores se disparan juntos: al instalarlos,
# tras un reinicio o tras una caida (Persistent=true recupera los turnos perdidos a la vez). El
# respaldo de buzones tomaba el cerrojo y el de las bases fallaba ese turno entero (visto en
# produccion el 2026-09-23 al pasar a cada seis horas).
exec 8>"$BACKUP_DIR/.respaldo.lock"
flock -w 7200 8 || abortar "otro respaldo o verificacion sigue en curso sobre $BACKUP_DIR tras dos horas"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dest="$BACKUP_DIR/$stamp"
mkdir -m 0700 "$dest" || abortar "no se pudo crear $dest"
cf_pg_montar "$dest" || abortar "no se pudo preparar $dest para las herramientas de Postgres"

if [[ $# -gt 0 ]]; then
  dbs=("$@")
else
  # A proposito NO se usa cf_tenant_databases: el respaldo se queda con todo lo que parezca
  # nuestro, incluido mail_registry y cualquier base que exista sin estar declarada como
  # empresa. Respaldar de mas cuesta disco; respaldar de menos se descubre el dia que hace
  # falta. Las migraciones si van por el registro, porque ahi aplicar de mas es un error.
  lista="$(cf_psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d postgres -At -v ON_ERROR_STOP=1 \
    -c "SELECT datname FROM pg_database WHERE datname LIKE 'mail\_%' AND NOT datistemplate ORDER BY datname")" ||
    { rmdir "$dest" 2>/dev/null; abortar "no se pudo listar las bases"; }
  mapfile -t dbs < <(sed '/^$/d' <<<"$lista")
fi

if [[ ${#dbs[@]} -eq 0 ]]; then
  rmdir "$dest" 2>/dev/null
  abortar "no se encontró ninguna base que respaldar"
fi
if [[ $# -eq 0 && ! " ${dbs[*]} " =~ " mail_registry " ]]; then
  echo "  AVISO: no aparece mail_registry entre las bases; sin el registro no se opera la plataforma" >&2
fi

echo "Respaldo $stamp -> $dest (${#dbs[@]} bases)${CF_PERFIL_DESPLIEGUE:+, perfil $CF_PERFIL_DESPLIEGUE}"
fail=$externo_mal
fail_local=0
for db in "${dbs[@]}"; do
  [[ "$db" =~ ^[a-z0-9_]+$ ]] || { echo "  FALLA: nombre de base inesperado: $(printf '%q' "$db")"; fail=1; fail_local=1; continue; }
  file="$dest/$db.dump"
  if ! cf_pg_dump -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$db" -Fc -f "$file" 2>"$file.err"; then
    echo "  FALLA: $db no se pudo volcar"; sed 's/^/      /' "$file.err" | head -3
    rm -f "$file"
    fail=1; fail_local=1; continue
  fi
  rm -f "$file.err"
  # La herramienta en contenedor crea el fichero con su propia mascara: se deja solo para su dueno.
  chmod 0600 "$file" || { echo "  FALLA: no se pudo restringir $file"; fail=1; fail_local=1; continue; }

  # Un volcado que pg_restore no sabe leer no es un respaldo. Se comprueba aquí, no el
  # día de la emergencia: el índice y, leyendo el fichero entero, que los datos descomprimen.
  objects="$(cf_pg_restore --list "$file" 2>/dev/null | grep -c ';' || true)"
  if [[ "${objects:-0}" -lt 10 ]] || ! cf_pg_restore -f /dev/null "$file" 2>/dev/null; then
    echo "  FALLA: $db produjo un volcado ilegible o vacío (${objects:-0} entradas)"
    mv -f "$file" "$file.ilegible" 2>/dev/null
    fail=1; fail_local=1; continue
  fi

  # La suma se guarda junto al volcado: restore-tenant.sh la comprueba antes de restaurar, y un
  # fichero dañado despues en disco se detecta sin leerlo con pg_restore.
  (cd "$dest" && sha256sum "$db.dump" >"$db.dump.sha256") || { echo "  FALLA: $db sin suma de verificacion"; fail=1; fail_local=1; continue; }

  size="$(du -h "$file" | cut -f1)"
  echo "  OK $db ($size, $objects objetos)"

  if cf_externo_activo; then
    origen="$file"
    clave="postgres/$db/$stamp.dump"
    if cf_externo_cifra; then
      if ! cf_cifrar "$file" "$file.gpg"; then
        echo "  AVISO: $db quedó respaldado en disco pero no se pudo cifrar; no sale del servidor"
        fail=1; continue
      fi
      origen="$file.gpg"
      clave="$clave.gpg"
    fi
    if ! cf_externo_subir "$origen" "$clave"; then
      echo "  AVISO: $db quedó respaldado en disco pero no subió a s3://$CF_S3_BUCKET"
      fail=1
    fi
    [[ "$origen" == "$file.gpg" ]] && rm -f "$file.gpg"
  fi
done

# El almacen de secretos va en la misma corrida que las bases: restaurar unas sin las llaves que
# descifran sus credenciales de terceros no sirve. La instantanea sale cifrada por OpenBao y solo
# se lee con la llave de desbloqueo, que se guarda fuera del servidor (docs/adr/0011).
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../security/secrets/store.sh"
if [[ $# -eq 0 && "$STORE_BACKEND" == openbao ]]; then
  snap="$dest/openbao.snap"
  if python3 "$STORE_OPENBAO" instantanea "$snap" 2>"$snap.err" &&
    (cd "$dest" && sha256sum openbao.snap >openbao.snap.sha256); then
    rm -f "$snap.err"
    echo "  OK almacen de secretos (instantanea de OpenBao, $(du -h "$snap" | cut -f1))"
    if cf_externo_activo; then
      origen="$snap"
      clave="openbao/$stamp.snap"
      if cf_externo_cifra; then
        origen="$snap.gpg"
        clave="$clave.gpg"
        cf_cifrar "$snap" "$origen" || origen=""
      fi
      if [[ -z "$origen" ]]; then
        echo "  AVISO: la instantanea de OpenBao no se pudo cifrar; no sale del servidor"; fail=1
      elif ! cf_externo_subir "$origen" "$clave"; then
        echo "  AVISO: la instantanea de OpenBao no subio a s3://$CF_S3_BUCKET"; fail=1
      fi
      rm -f "$snap.gpg"
    fi
  else
    echo "  FALLA: no se pudo sacar la instantanea de OpenBao"; sed 's/^/      /' "$snap.err" | head -3
    rm -f "$snap" "$snap.err"
    fail=1; fail_local=1
  fi
fi

# La CONFIGURACION del servidor, en la misma corrida. Sin ella los datos vuelven pero no se sabe
# como arrancarlos: que celda es, que dominio sirve, a que apunta cada servicio. Son ficheros que
# nadie versiona porque describen ESTE servidor, y hasta que el ensayo de recuperacion lo senalo no
# salian de el (ops/backup/ensayo-recuperacion.sh).
#
# Cifrado OBLIGATORIO, aunque el destino sea la propia cuenta de AWS: el .env lleva todavia algun
# secreto (el token entre servicios, la clave del agente de cola) y no puede viajar en claro.
if [[ $# -eq 0 ]]; then
  cfg="$dest/config.tar.gz"
  cfg_dir="$(mktemp -d)"
  n_cfg=0
  # Hoy los motores usan el .env de la plataforma (deploy-mail.sh: --env-file $DEPLOY_PATH/.env), asi
  # que normalmente hay uno solo; el segundo se recoge si algun servidor llega a tener el suyo.
  for f in "$APP_DIR/.env" "${MAIL_DEPLOY_PATH:-/opt/core-force-mail/mail-src}/deploy/mail/.env"; do
    [[ -r "$f" ]] || continue
    # El nombre dice de donde sale, para poder reponerlo sin adivinar.
    destino_cfg="$cfg_dir/$(basename "$(dirname "$f")").env"
    [[ -e "$destino_cfg" ]] && destino_cfg="$cfg_dir/motores-$(basename "$(dirname "$f")").env"
    cp -p "$f" "$destino_cfg" && n_cfg=$((n_cfg + 1))
  done
  if [[ $n_cfg -eq 0 ]]; then
    echo "  AVISO: no se pudo leer ningun .env; la configuracion no se respalda"; fail=1
  elif tar -czf "$cfg" -C "$cfg_dir" . && (cd "$dest" && sha256sum config.tar.gz >config.tar.gz.sha256); then
    chmod 600 "$cfg"
    echo "  OK configuracion ($n_cfg fichero(s), $(du -h "$cfg" | cut -f1))"
    if cf_externo_activo; then
      if ! cf_externo_cifra; then
        echo "  FALLA: la configuracion no sale del servidor sin cifrar: lleva secretos"; fail=1
      elif ! cf_cifrar "$cfg" "$cfg.gpg"; then
        echo "  AVISO: la configuracion no se pudo cifrar; no sale del servidor"; fail=1
      elif ! cf_externo_subir "$cfg.gpg" "config/$stamp.tar.gz.gpg"; then
        echo "  AVISO: la configuracion no subio a s3://$CF_S3_BUCKET"; fail=1
      fi
      rm -f "$cfg.gpg"
    fi
  else
    echo "  AVISO: no se pudo empaquetar la configuracion"; fail=1
  fi
  rm -rf "$cfg_dir"
fi

if [[ $externo_mal -eq 1 ]]; then
  echo "AVISO: copia externa mal configurada (FALLA de arriba); solo hay copia local"
elif ! cf_externo_activo; then
  echo "AVISO: BACKUP_S3_BUCKET sin definir; solo hay copia local, que se pierde con el servidor"
fi

# Retención local. Solo tras una corrida sin fallos locales: si hoy no se pudo volcar, los
# respaldos de ayer son los unicos buenos. Nunca borra el respaldo de esta corrida.
if [[ $fail_local -eq 0 ]]; then
  find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '[0-9]*T[0-9]*Z' -mtime "+$KEEP_DAYS" \
    ! -path "$dest" -exec rm -rf {} + 2>/dev/null || true
else
  echo "AVISO: no se aplica la retención local porque esta corrida tuvo fallos"
fi

cf_publicar_metrica respaldo "$fail" "${#dbs[@]}"

if [[ $fail -ne 0 ]]; then
  echo "RESPALDO INCOMPLETO: revisa las líneas FALLA/AVISO de arriba" >&2
  exit 1
fi
echo "RESPALDO OK: ${#dbs[@]} bases en $dest${CF_S3_BUCKET:+ y en s3://$CF_S3_BUCKET/postgres/}"
