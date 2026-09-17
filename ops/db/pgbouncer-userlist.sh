#!/usr/bin/env bash
# Genera userlist.txt de PgBouncer a partir de las credenciales del almacen.
#
#   ops/security/secrets/with-secrets.sh ops/db/pgbouncer-userlist.sh --write
#   ops/security/secrets/with-secrets.sh ops/db/pgbouncer-userlist.sh --check
#
# Por que existe: PgBouncer autentica a los clientes contra userlist.txt (auth_type = plain)
# y TODA conexion de la plataforma pasa por el. Un rol que no figure ahi no entra, por mucho
# que Postgres lo acepte: el pooler lo rechaza antes. Hasta ahora el fichero se escribia a
# mano en el servidor, asi que cada credencial nueva -la de una celda, la de un servicio-
# quedaba fuera y su servicio no arrancaba, con un error que apunta a la base y no al pooler.
#
# Que escribe: una linea por rol de login que este despliegue usa, con la contrasena que hay
# en el entorno. Los roles salen de ops/db/service-credentials.json y de la configuracion de
# este despliegue (CELL_DB_NAME para los de celda), no de una lista aparte que se
# desincronice.
#
# Un rol cuya contrasena no esta en el entorno se OMITE y se avisa: es lo normal mientras el
# reparto por servicio avanza servicio a servicio. Lo que no se admite es lo contrario, un
# fichero con una credencial de un rol que ya no existe, y por eso se reescribe entero.
#
# --check no escribe: compara el fichero que hay con el que saldria y sale con 1 si difieren.
# Es lo que hay que correr despues de crear un rol y antes de recrear su servicio.
#
# El fichero lleva contrasenas EN CLARO, que es lo que PgBouncer necesita con auth_type =
# plain para poder autenticarse a su vez contra Postgres con SCRAM. Por eso se escribe con
# umask 077, modo 0640 y grupo 70 (el uid con el que corre el pooler en su imagen), en el
# directorio que docker-compose.yml monta de solo lectura. Alternativa que evitaria el texto
# plano: auth_query con un usuario que solo consulte verificadores; queda pendiente y esta
# anotada en docs/Modelo_de_Datos_y_Celdas.md, 5.1.
set -euo pipefail
LC_ALL=C
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MATRIZ="$SCRIPT_DIR/service-credentials.json"
DESTINO="${PGBOUNCER_USERLIST:-$ROOT/pgbouncer/userlist.txt}"

MODO=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --write) MODO=write; shift ;;
    --check) MODO=check; shift ;;
    --print) MODO=print; shift ;;
    --ensure) MODO=ensure; shift ;;
    *) echo "argumento desconocido: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$MODO" ]] || { echo "uso: $0 --write | --check | --print | --ensure" >&2; exit 2; }
[[ -f "$MATRIZ" ]] || { echo "no existe $MATRIZ" >&2; exit 2; }

# La contrasena NO se lee del .env: la fuente es el almacen (with-secrets.sh la deja en el
# entorno). ops/security/secrets/check-secret-sources.sh lo exige.
avisos=()
lineas=()

# anadir <rol> <variable de entorno con su contrasena> <para que>
anadir() {
  local rol="$1" var="$2" para="$3" clave="${!2:-}"
  if [[ -z "$clave" ]]; then
    avisos+=("$rol ($para): sin $var en el entorno; no entra en userlist.txt")
    return
  fi
  if [[ "$clave" == *'"'* ]]; then
    echo "userlist: la contrasena de $rol lleva una comilla doble y PgBouncer no la admite" >&2
    exit 1
  fi
  lineas+=("\"$rol\" \"$clave\"")
}

# 1. Credencial de plataforma: la usan los servicios del plano de registro y la consola de
#    administracion del pooler (ops/maintenance/pgbouncer-reconnect.sh).
anadir "${POSTGRES_USER:-mail_admin}" POSTGRES_PASSWORD "plataforma"

# 2. Credencial de la celda que sirve este despliegue, si lo es, y la de sus motores.
if [[ -n "${CELL_DB_NAME:-}" ]]; then
  anadir "${CELL_DB_USER:-${CELL_DB_NAME}_svc}" CELL_DB_PASSWORD "servicios de la celda"
  anadir "${CELL_DB_NAME}_engine" MAIL_DB_PASSWORD "motores de la celda"
fi

# 3. Enrutado y un rol por servicio de empresa, de la matriz.
anadir mail_router TENANT_ROUTER_DB_PASSWORD "enrutado de empresas"
while IFS='|' read -r svc esquema; do
  [[ -z "$svc" ]] && continue
  anadir "mail_svc_$esquema" "$(echo "$svc" | tr 'a-z-' 'A-Z_')_DB_PASSWORD" "servicio $svc"
done < <(python3 - "$MATRIZ" <<'PY'
import json, sys
for nombre, s in sorted(json.load(open(sys.argv[1], encoding="utf-8"))["servicios"].items()):
    if s.get("plane") == "tenant":
        print(f"{nombre}|{s['schema']}")
PY
)

contenido="$(printf '%s\n' \
  "; GENERADO por ops/db/pgbouncer-userlist.sh - no editar a mano." \
  "; Un rol por linea, con la contrasena del almacen de secretos." \
  "${lineas[@]}")"

# --ensure es lo que corre el despliegue antes de recrear. Sin fichero se genera: sin el,
# el pooler no arranca. Con uno distinto solo se avisa, porque reescribirlo a ciegas quitaria
# el rol cuya contrasena no este en este entorno y dejaria fuera a un servicio que hoy entra.
if [[ "$MODO" == ensure ]]; then
  if [[ ! -f "$DESTINO" ]]; then
    echo "userlist: falta $DESTINO; se genera" >&2
    MODO=write
  elif ! diff -q <(printf '%s\n' "$contenido") "$DESTINO" >/dev/null 2>&1; then
    echo "userlist: AVISO: $DESTINO no coincide con los roles y credenciales de este despliegue; no se reescribe." >&2
    echo "  Revisalo y regeneralo: ops/security/secrets/with-secrets.sh ops/db/pgbouncer-userlist.sh --write" >&2
    exit 0
  else
    echo "userlist: $DESTINO al dia"
    exit 0
  fi
fi

case "$MODO" in
  print)
    # Sin contrasenas: solo que roles saldrian. Sirve para revisar sin exponer nada.
    printf '%s\n' "${lineas[@]}" | sed 's/" ".*/"/'
    ;;
  check)
    if [[ ! -f "$DESTINO" ]]; then
      echo "userlist: falta $DESTINO; generalo con --write" >&2
      exit 1
    fi
    if ! diff -q <(printf '%s\n' "$contenido") "$DESTINO" >/dev/null 2>&1; then
      echo "userlist: $DESTINO no coincide con los roles y credenciales de este despliegue." >&2
      echo "  Roles que deberia tener:" >&2
      printf '%s\n' "${lineas[@]}" | sed 's/" ".*/"/;s/^/    /' >&2
      echo "  Regeneralo: ops/security/secrets/with-secrets.sh ops/db/pgbouncer-userlist.sh --write" >&2
      exit 1
    fi
    echo "userlist: al dia (${#lineas[@]} roles)"
    ;;
  write)
    umask 077
    mkdir -p "$(dirname "$DESTINO")"
    tmp="$(mktemp "$DESTINO.XXXXXX")"
    trap 'rm -f "$tmp"' EXIT
    printf '%s\n' "$contenido" > "$tmp"
    # El pooler corre con el uid 70 de su imagen y lee el fichero por el grupo.
    chgrp 70 "$tmp" 2>/dev/null || avisos+=("no se pudo poner el grupo 70 a $DESTINO; hazlo a mano o el pooler no lo leera")
    chmod 640 "$tmp"
    mv -f "$tmp" "$DESTINO"
    trap - EXIT
    echo "userlist: $DESTINO con ${#lineas[@]} roles"
    echo "  el pooler lo relee al arrancar: ops/security/secrets/with-secrets.sh docker compose up -d --force-recreate pgbouncer"
    ;;
esac

for a in "${avisos[@]}"; do echo "  aviso: $a" >&2; done
