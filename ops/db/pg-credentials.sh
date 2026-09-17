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
# Perfil autoalojado (DEPLOY_PROFILE=selfhosted): postgres-primary no se publica en el host y su
# nombre solo existe en la red interna de Docker. Las herramientas cf_psql, cf_pg_dump y
# cf_pg_restore corren entonces en un contenedor efimero de la MISMA imagen que el Postgres en
# marcha, en su red, con verify-full contra la CA interna. Asi el cliente siempre tiene la version
# del servidor: un volcado -Fc de pg_dump 17 (el del host Debian 13) no lo lee el pg_restore 16 de
# la imagen, y restaurar con otra herramienta que la que respalda convierte la copia en una
# apuesta. En el perfil aws son los binarios del host, como hasta ahora.
#
# Sin `set -e`: se sourcea desde scripts con sus propias opciones.

_cf_db_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"

# El perfil del despliegue y las claves no secretas del .env: ops/maintenance/entorno-despliegue.sh
# (cf_read_env y CF_PERFIL_DESPLIEGUE).
# shellcheck source=/dev/null
if ! . "$_cf_db_dir/../maintenance/entorno-despliegue.sh"; then
  echo "pg-credentials: no se pudo leer la configuracion del despliegue" >&2
  return 1 2>/dev/null || exit 1
fi

# shellcheck source=/dev/null
if ! . "$_cf_db_dir/../security/secrets/load.sh"; then
  echo "pg-credentials: no se pudieron cargar los secretos" >&2
  return 1 2>/dev/null || exit 1
fi

if [[ "$CF_PERFIL_DESPLIEGUE" == selfhosted ]]; then
  # El nombre del servicio de compose es el que resuelve la red interna y el que lleva el SAN del
  # certificado (ops/security/internal-tls.sh): verify-full no admite otro.
  CF_PG_SERVICIO=postgres-primary
  PGHOST="$CF_PG_SERVICIO"
  PGPORT="${POSTGRES_DIRECT_PORT:-$(cf_read_env POSTGRES_DIRECT_PORT)}"
  PGSSLMODE=verify-full
  PGSSLROOTCERT=/run/core-force-mail/tls/publico/ca.crt
  export PGSSLMODE PGSSLROOTCERT
else
  PGHOST="${POSTGRES_DIRECT_HOST:-$(cf_read_env DB_UPSTREAM_HOST)}"
  [[ -z "$PGHOST" ]] && PGHOST="$(cf_read_env POSTGRES_HOST)"
  PGPORT="${POSTGRES_PORT:-$(cf_read_env POSTGRES_PORT)}"
fi
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

CF_PG_MONTAJES=()
if [[ "$CF_PERFIL_DESPLIEGUE" == selfhosted ]]; then
  # Proyecto de compose: el de COMPOSE_PROJECT_NAME o, como hace Compose, el directorio de la
  # aplicacion normalizado.
  _cf_proyecto="${COMPOSE_PROJECT_NAME:-$(cf_read_env COMPOSE_PROJECT_NAME)}"
  [[ -n "$_cf_proyecto" ]] || _cf_proyecto="$(basename "$APP_DIR" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9_-')"
  _cf_tls_dir="${INTERNAL_TLS_DIR:-$(cf_read_env INTERNAL_TLS_DIR)}"
  _cf_tls_dir="${_cf_tls_dir:-/opt/core-force-mail/tls}"

  CF_PG_CONTENEDOR="$(docker ps -q --filter "label=com.docker.compose.project=$_cf_proyecto" \
    --filter "label=com.docker.compose.service=$CF_PG_SERVICIO" --filter status=running 2>/dev/null | head -n 1)"
  if [[ -z "$CF_PG_CONTENEDOR" ]]; then
    echo "pg-credentials: $CF_PG_SERVICIO no esta en marcha en el proyecto de compose $_cf_proyecto (o sin acceso a docker)" >&2
    return 1 2>/dev/null || exit 1
  fi
  # La imagen por su identificador, no por su etiqueta: la que corre, aunque la etiqueta se haya
  # movido despues de un pull.
  CF_PG_IMAGEN="$(docker inspect -f '{{.Image}}' "$CF_PG_CONTENEDOR")"
  mapfile -t _cf_redes < <(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{println $k}}{{end}}' "$CF_PG_CONTENEDOR" | sed '/^$/d')
  if [[ ${#_cf_redes[@]} -ne 1 ]]; then
    echo "pg-credentials: $CF_PG_SERVICIO esta en ${#_cf_redes[@]} redes (${_cf_redes[*]}); se esperaba solo la interna" >&2
    return 1 2>/dev/null || exit 1
  fi
  CF_PG_RED="${_cf_redes[0]}"
  if [[ ! -r "$_cf_tls_dir/publico/ca.crt" ]]; then
    echo "pg-credentials: sin la CA interna en $_cf_tls_dir/publico/ca.crt; sin ella no se verifica a $CF_PG_SERVICIO" >&2
    return 1 2>/dev/null || exit 1
  fi
  CF_PG_CA_DIR="$_cf_tls_dir/publico"
  unset _cf_proyecto _cf_tls_dir _cf_redes
fi

# cf_pg_montar <directorio> [ro]: lo hace visible, en la misma ruta, a las herramientas que corren
# en contenedor (volcados, descargas). En el perfil aws no hace nada.
cf_pg_montar() {
  [[ "$CF_PERFIL_DESPLIEGUE" == selfhosted ]] || return 0
  local dir modo="${2:-rw}"
  dir="$(cd "$1" && pwd)" || return 1
  CF_PG_MONTAJES+=(-v "$dir:$dir:$modo")
}

# cf_pg_herramienta <psql|pg_dump|pg_restore> [argumentos]: la contrasena entra por el entorno
# (-e con solo el nombre), nunca por argumentos. Contenedor sin capacidades, de solo lectura, con
# el uid del llamador para que los volcados sean suyos.
cf_pg_herramienta() {
  if [[ "$CF_PERFIL_DESPLIEGUE" != selfhosted ]]; then
    "$@"
    return
  fi
  docker run --rm -i --network "$CF_PG_RED" --user "$(id -u):$(id -g)" \
    --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
    -e HOME=/tmp -e PGHOST -e PGPORT -e PGUSER -e PGPASSWORD -e PGSSLMODE -e PGSSLROOTCERT \
    -v "$CF_PG_CA_DIR:/run/core-force-mail/tls/publico:ro" "${CF_PG_MONTAJES[@]}" \
    --entrypoint "$1" "$CF_PG_IMAGEN" "${@:2}"
}
cf_psql() { cf_pg_herramienta psql "$@"; }
cf_pg_dump() { cf_pg_herramienta pg_dump "$@"; }
cf_pg_restore() { cf_pg_herramienta pg_restore "$@"; }

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
  cf_psql -d mail_registry -At -c \
    "SELECT db_name FROM organization.tenants
      WHERE status = 'active' AND db_name <> ''
      ORDER BY db_name"
}
