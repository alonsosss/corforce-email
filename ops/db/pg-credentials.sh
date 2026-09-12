#!/usr/bin/env bash
# Resuelve las credenciales de Postgres para los trabajos que corren FUERA de un contenedor:
# respaldo, restauracion y aplicacion de migraciones.
#
#   . ops/db/pg-credentials.sh
#   psql -d mail_registry -c '...'      # PGHOST/PGPORT/PGUSER/PGPASSWORD ya exportadas
#
# Por que existe: tres scripts se buscaban la contrasena por su cuenta leyendo el .env de la
# aplicacion. El dia que las credenciales pasaron al almacen de secretos, el .env dejo de
# tenerlas y los tres se quedaron con la variable vacia. Nada apunto a la causa: el respaldo
# nocturno solo habria dicho "faltan credenciales". Con un unico resolvedor, mover la fuente
# de los secretos mueve a todos sus consumidores a la vez.
#
# Reparto de responsabilidades:
#   - La contrasena es un SECRETO y sale del almacen (ops/security/secrets).
#   - El host, el puerto y el usuario son CONFIGURACION y siguen viniendo del .env.
#
# Se conecta al servidor de base directo, no a pgbouncer: el pooler agrupa por transaccion y
# no es el camino para DDL ni para un volcado.
#
# Sin `set -e`: se sourcea desde scripts con sus propias opciones.

_cf_db_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"

# cf_read_env: lee una clave NO secreta del .env de la aplicacion. Para secretos existe el
# almacen; leerlos de aqui es lo que este fichero viene a evitar.
cf_read_env() {
  if [[ -f "$APP_DIR/.env" ]]; then
    grep -m1 "^$1=" "$APP_DIR/.env" 2>/dev/null | cut -d= -f2-
  else
    printf '%s' "${!1:-}"
  fi
}

# shellcheck source=/dev/null
if ! . "$_cf_db_dir/../security/secrets/load.sh"; then
  echo "pg-credentials: no se pudieron cargar los secretos" >&2
  return 1 2>/dev/null || exit 1
fi

PGHOST="${POSTGRES_DIRECT_HOST:-$(cf_read_env DB_UPSTREAM_HOST)}"
[[ -z "$PGHOST" ]] && PGHOST="$(cf_read_env POSTGRES_HOST)"
PGPORT="${POSTGRES_PORT:-$(cf_read_env POSTGRES_PORT)}"
PGUSER="${POSTGRES_USER:-$(cf_read_env POSTGRES_USER)}"
# La contrasena llega como POSTGRES_PASSWORD desde el almacen; psql y pg_dump la leen de
# PGPASSWORD.
PGPASSWORD="${PGPASSWORD:-${POSTGRES_PASSWORD:-}}"
: "${PGPORT:=5432}"
export PGHOST PGPORT PGUSER PGPASSWORD

if [[ -z "$PGHOST" || -z "$PGUSER" || -z "${PGPASSWORD:-}" ]]; then
  echo "pg-credentials: faltan credenciales de Postgres." >&2
  echo "  host y usuario salen de $APP_DIR/.env; la contrasena, del almacen de secretos." >&2
  echo "  Comprobar con: ops/security/secrets/fetch-secrets.sh" >&2
  return 1 2>/dev/null || exit 1
fi

# cf_tenant_databases: las bases de empresa, una por empresa.
#
# La lista sale del registro de empresas, NO de un patron sobre los nombres de base. Filtrar
# por "mail_%" colaba bases de prueba que nadie declaro como empresa -mail_doc_test,
# mail_view_test-: se respaldaban a diario y hacian fallar cualquier migracion, porque no
# tienen el esquema de un tenant.
#
# El registro es tambien lo que hace que una empresa nueva entre sola en el respaldo y en las
# migraciones el dia que se da de alta.
cf_tenant_databases() {
  psql -d mail_registry -At -c \
    "SELECT db_name FROM organization.tenants
      WHERE status = 'active' AND db_name <> ''
      ORDER BY db_name"
}
