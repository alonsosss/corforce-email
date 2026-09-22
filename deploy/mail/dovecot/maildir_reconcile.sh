#!/bin/bash
# Retira del volumen de Dovecot los maildir que ya no tienen buzon.
#
# Borrar un buzon por mail-directory quita su fila de mail.mailboxes, pero ningun servicio Go
# alcanza /var/vmail: sin esto el maildir queda en el disco y quien reciba despues la misma
# direccion hereda el correo del titular anterior. mailcow lo resolvia desde PHP por dockerapi;
# aqui lo hace este guion dentro del contenedor, con la misma conexion de solo lectura del userdb
# (rol mail_engine). Nunca borra: mueve a /var/vmail/_garbage/<epoch>_<dominio>_<local>, que
# maildir_gc.sh purga a los MAILDIR_GC_TIME minutos (la ventana para deshacer).
#
# Dos pasadas por ejecucion:
#   1. Marcas de baja (mail.mailbox_deletions, que mail-directory escribe en la transaccion del
#      borrado): el maildir se mueve si es anterior a la baja o, si el buzon se recreo con el mismo
#      nombre, anterior a esa alta; la marca se consume (DELETE) al atenderla.
#   2. Huerfanos: todo /var/vmail/<dominio>/<local> sin fila en mail.mailboxes y todo
#      /var/vmail/<dominio> sin fila en mail.domains. El disco se lista ANTES de consultar la base:
#      un maildir que Dovecot crea despues de la consulta no esta en la lista, y uno que si esta
#      tenia su fila antes de ella.
#
# Fail-closed: un fallo o una salida inesperada de psql aborta la pasada sin mover nada, y una
# consulta de buzones o de dominios vacia tambien (una base vacia por error vaciaria el servidor).
# Un directorio mas joven que MAILDIR_RECONCILE_GRACE minutos no se toca. Tope de movimientos por
# pasada (MAILDIR_RECONCILE_MAX_MOVES). Solo directorios regulares, nunca enlaces, y solo nombres
# con forma de dominio y de parte local: el resto se ignora y se anuncia. Registro por linea con
# nombre y tamano, nunca contenido. Se prueba sin docker en ops/scaffold/check-maildir-reconcile.sh
# (MAILDIR_RECONCILE_ROOT apunta a un arbol de prueba) y con Dovecot real en make e2e-mail.
set -uo pipefail

VMAIL=${MAILDIR_RECONCILE_ROOT:-/var/vmail}
GARBAGE="$VMAIL/_garbage"
GRACE=${MAILDIR_RECONCILE_GRACE:-10}
MAX_MOVES=${MAILDIR_RECONCILE_MAX_MOVES:-50}
# Dominios que nunca son de una empresa: el del usuario maestro, que limpia el entrypoint.
RESERVED_DOMAINS="platform.local"
DOMAIN_RE='^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
LOCAL_RE='^[a-z0-9_+-][a-z0-9._+-]*$'
UUID_RE='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'

log() { printf 'maildir_reconcile: %s\n' "$*"; }
abortar() { log "ABORTADA sin mover nada: $*"; exit 1; }

# Los maildir y _garbage son de vmail: como root (docker exec) se cambia de usuario.
if [[ $(id -u) -eq 0 && -x /usr/local/bin/gosu ]]; then
  exec /usr/local/bin/gosu vmail "$0" "$@"
fi

[[ $GRACE =~ ^[0-9]+$ ]] || abortar "MAILDIR_RECONCILE_GRACE debe ser un entero de minutos: '$GRACE'"
[[ $MAX_MOVES =~ ^[1-9][0-9]*$ ]] || abortar "MAILDIR_RECONCILE_MAX_MOVES debe ser un entero positivo: '$MAX_MOVES'"
[[ -d $VMAIL && -d $GARBAGE ]] || abortar "faltan $VMAIL o $GARBAGE"
for v in MAIL_DB_HOST MAIL_DB_USER MAIL_DB_NAME MAIL_DB_PASSWORD; do
  [[ -n ${!v:-} ]] || abortar "falta $v"
done

# Un solo barrido a la vez, y nunca mientras maildir_gc.sh purga _garbage (mismo cerrojo).
exec 9>"$GARBAGE/.reconcile.lock" || abortar "no se puede abrir el cerrojo"
if ! flock -n 9; then
  log "otra pasada o maildir_gc.sh en curso: se deja para la siguiente"
  exit 0
fi

consulta() {
  PGPASSWORD="$MAIL_DB_PASSWORD" PGCONNECT_TIMEOUT=10 \
    psql -X -q -At -F $'\t' -v ON_ERROR_STOP=1 -h "$MAIL_DB_HOST" -p "${MAIL_DB_PORT:-5432}" \
    -U "$MAIL_DB_USER" -d "$MAIL_DB_NAME" -c "$1"
}

# ── 1. Foto del disco, antes de la base ──────────────────────────────────────
DISK_DOMAINS=()
while IFS= read -r -d '' d; do DISK_DOMAINS+=("$d"); done < <(find "$VMAIL" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null)
DISK_MAILDIRS=()
while IFS= read -r -d '' d; do DISK_MAILDIRS+=("$d"); done < <(find "$VMAIL" -mindepth 2 -maxdepth 2 -type d -print0 2>/dev/null)

# ── 2. La base: buzones, dominios y marcas de baja ───────────────────────────
declare -A MAILBOX DOMAIN
MAILBOXES_OUT=$(consulta "SELECT domain, local_part FROM mail.mailboxes") || abortar "la consulta de buzones fallo"
n_mailboxes=0
while IFS=$'\t' read -r dom local; do
  [[ -z $dom && -z $local ]] && continue
  [[ $dom =~ $DOMAIN_RE && $local =~ $LOCAL_RE ]] || abortar "salida inesperada en la consulta de buzones"
  MAILBOX["$dom/$local"]=1
  n_mailboxes=$((n_mailboxes + 1))
done <<<"$MAILBOXES_OUT"
[[ $n_mailboxes -gt 0 ]] || abortar "la consulta de buzones no devolvio ninguno"

DOMAINS_OUT=$(consulta "SELECT domain FROM mail.domains") || abortar "la consulta de dominios fallo"
n_domains=0
while IFS= read -r dom; do
  [[ -z $dom ]] && continue
  [[ $dom =~ $DOMAIN_RE ]] || abortar "salida inesperada en la consulta de dominios"
  DOMAIN["$dom"]=1
  n_domains=$((n_domains + 1))
done <<<"$DOMAINS_OUT"
[[ $n_domains -gt 0 ]] || abortar "la consulta de dominios no devolvio ninguno"

MARKS_OUT=$(consulta "SELECT d.id, d.domain, d.local_part, extract(epoch FROM d.deleted_at)::bigint,
    coalesce(extract(epoch FROM m.created_at)::bigint, 0)
  FROM mail.mailbox_deletions d LEFT JOIN mail.mailboxes m ON m.username = d.username
  ORDER BY d.deleted_at LIMIT $MAX_MOVES") || abortar "la consulta de marcas de baja fallo"

# ── Utilidades ───────────────────────────────────────────────────────────────
moves=0
# reciente <dir>: mas joven que la gracia (mtime del propio directorio).
reciente() { [[ -n $(find "$1" -maxdepth 0 -mmin "-$GRACE" 2>/dev/null) ]]; }
# mover <dir> <nombre en _garbage> <motivo>: 0 movido, 1 no movido, 2 tope alcanzado.
mover() {
  local src=$1 dest="$GARBAGE/$(date +%s)_$2" size
  if [[ $moves -ge $MAX_MOVES ]]; then
    log "tope de $MAX_MOVES movimientos alcanzado: el resto queda para la siguiente pasada"
    return 2
  fi
  if [[ -e $dest ]]; then
    log "no se mueve ${src#"$VMAIL"/}: ya existe ${dest#"$VMAIL"/}"
    return 1
  fi
  size=$(du -sk "$src" 2>/dev/null | cut -f1)
  if ! mv "$src" "$dest"; then
    log "no se pudo mover ${src#"$VMAIL"/}"
    return 1
  fi
  moves=$((moves + 1))
  log "movido ${src#"$VMAIL"/} -> ${dest#"$VMAIL"/} (${size:-?} KiB, $3)"
}
consumir() { # consumir <id de marca> <motivo>
  if consulta "DELETE FROM mail.mailbox_deletions WHERE id = '$1'" >/dev/null; then
    log "marca $1 atendida: $2"
  else
    log "marca $1 no se pudo consumir (se reintenta en la siguiente pasada): $2"
  fi
}

# ── 3. Marcas de baja ────────────────────────────────────────────────────────
marks=0
while IFS=$'\t' read -r id dom local deleted created; do
  [[ -z $id ]] && continue
  [[ $id =~ $UUID_RE && $dom =~ $DOMAIN_RE && $local =~ $LOCAL_RE && $deleted =~ ^[0-9]+$ && $created =~ ^[0-9]+$ ]] \
    || abortar "salida inesperada en la consulta de marcas de baja"
  marks=$((marks + 1))
  dir="$VMAIL/$dom/$local"
  if [[ -L $dir || ! -d $dir ]]; then
    consumir "$id" "$dom/$local sin maildir en disco"
    continue
  fi
  ref=$deleted
  if [[ $created -gt 0 ]]; then
    if [[ $created -le $deleted ]]; then
      log "anomalia: $dom/$local existe con un alta anterior a su baja marcada; no se toca"
      continue
    fi
    ref=$created
  fi
  # Un maildir en el que todo es posterior a la referencia lo creo Dovecot despues: es del buzon
  # nuevo (o, sin buzon, lo atendera la pasada de huerfanos con su gracia).
  if [[ -z $(find "$dir" ! -newermt "@$ref" -print -quit 2>/dev/null) ]]; then
    consumir "$id" "el maildir de $dom/$local es posterior a la referencia @$ref"
    continue
  fi
  newer=0
  [[ $created -gt 0 ]] && newer=$(find "$dir" -type f -newermt "@$created" 2>/dev/null | wc -l)
  mover "$dir" "${dom}_${local}" "baja marcada en @$deleted"
  case $? in
    0) [[ $newer -gt 0 ]] && log "aviso: $newer ficheros posteriores al alta del buzon nuevo $dom/$local se fueron con el maildir anterior; recuperables en _garbage durante MAILDIR_GC_TIME"
       consumir "$id" "maildir de $dom/$local movido" ;;
    2) break ;;
  esac
done <<<"$MARKS_OUT"

# ── 4. Dominios huerfanos ────────────────────────────────────────────────────
for path in "${DISK_DOMAINS[@]}"; do
  [[ -d $path && ! -L $path ]] || continue
  dom=${path##*/}
  [[ $dom == _* || $dom == sieve* ]] && continue
  [[ " $RESERVED_DOMAINS " == *" $dom "* ]] && continue
  if [[ ! $dom =~ $DOMAIN_RE ]]; then
    log "ignorado (no tiene forma de dominio): $dom"
    continue
  fi
  [[ -n ${DOMAIN[$dom]:-} ]] && continue
  if reciente "$path"; then
    log "dominio sin fila $dom: creado hace menos de $GRACE min, se espera"
    continue
  fi
  mover "$path" "$dom" "dominio sin fila en mail.domains"
  [[ $? -eq 2 ]] && break
done

# ── 5. Buzones huerfanos ─────────────────────────────────────────────────────
for path in "${DISK_MAILDIRS[@]}"; do
  [[ -d $path && ! -L $path ]] || continue
  local_part=${path##*/}
  dom=${path%/*}; dom=${dom##*/}
  [[ -n ${DOMAIN[$dom]:-} ]] || continue
  if [[ ! $local_part =~ $LOCAL_RE ]]; then
    log "ignorado (no tiene forma de parte local): $dom/$local_part"
    continue
  fi
  [[ -n ${MAILBOX[$dom/$local_part]:-} ]] && continue
  if reciente "$path"; then
    log "maildir sin buzon $dom/$local_part: creado hace menos de $GRACE min, se espera"
    continue
  fi
  mover "$path" "${dom}_${local_part}" "sin fila en mail.mailboxes"
  [[ $? -eq 2 ]] && break
done

log "pasada terminada: $moves movidos a _garbage, $marks marcas de baja leidas, $n_mailboxes buzones y $n_domains dominios en la base"
