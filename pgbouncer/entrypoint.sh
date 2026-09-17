#!/bin/sh
# Renderiza pgbouncer.ini desde la plantilla inyectando el upstream por entorno
# y arranca pgbouncer. El endpoint del Postgres real no se fija en el repo:
#   - dev local: DB_UPSTREAM_HOST=postgres-primary (contenedor)
#   - producción en AWS: DB_UPSTREAM_HOST=<endpoint RDS>, CA por defecto (bundle de RDS)
#   - producción autoalojada: DB_UPSTREAM_HOST=postgres-primary y DB_UPSTREAM_CA_FILE con la CA
#     interna (docker-compose.selfhosted.yml)
set -eu

DB_UPSTREAM_HOST="${DB_UPSTREAM_HOST:-postgres-primary}"
DB_UPSTREAM_PORT="${DB_UPSTREAM_PORT:-5432}"
# Usuario de administracion y estadisticas de pgbouncer: el de la plataforma.
DB_ADMIN_USER="${DB_ADMIN_USER:-${POSTGRES_USER:-mail_admin}}"
ENVIRONMENT="${ENVIRONMENT:-}"

template="${PGBOUNCER_TEMPLATE:-/etc/pgbouncer/pgbouncer.ini.template}"
# La CA con la que se verifica la base. Por defecto el bundle publico de RDS, que viaja junto a
# la plantilla; un Postgres propio declara la CA que firma su certificado.
DB_UPSTREAM_CA_FILE="${DB_UPSTREAM_CA_FILE:-$(dirname "$template")/rds-global-bundle.pem}"
userlist="${PGBOUNCER_USERLIST:-/etc/pgbouncer/userlist.txt}"
# Se renderiza a una ruta escribible (la imagen puede correr como usuario
# no-root sin permiso de escritura en /etc/pgbouncer).
rendered="${PGBOUNCER_RENDERED:-/tmp/pgbouncer.ini}"
userlist_local="${PGBOUNCER_LOCAL_USERLIST:-/tmp/userlist.txt}"

local_env=false
case "$ENVIRONMENT" in development | test) local_env=true ;; esac

# TLS hacia la base. En desarrollo y pruebas el Postgres es el contenedor del compose, sin
# certificado; en cualquier otro entorno no se admite nada mas debil que verify-full.
if $local_env; then sslmode_por_defecto=disable; else sslmode_por_defecto=verify-full; fi
DB_UPSTREAM_SSLMODE="${DB_UPSTREAM_SSLMODE:-$sslmode_por_defecto}"
case "$DB_UPSTREAM_SSLMODE" in
  verify-full) ;;
  disable | allow | prefer | require | verify-ca)
    if ! $local_env; then
      echo "pgbouncer: DB_UPSTREAM_SSLMODE=$DB_UPSTREAM_SSLMODE solo se admite con ENVIRONMENT=development|test (ENVIRONMENT='$ENVIRONMENT')" >&2
      exit 1
    fi
    ;;
  *)
    echo "pgbouncer: DB_UPSTREAM_SSLMODE no valido: '$DB_UPSTREAM_SSLMODE'" >&2
    exit 1
    ;;
esac

# Con verificacion, una CA ausente o ilegible no impide arrancar: cada conexion a la base falla
# despues con un error de TLS que el cliente ve como caida de la base. Se comprueba aqui. La ruta
# va a la plantilla por sed, asi que solo se admiten caracteres de ruta corrientes.
case "$DB_UPSTREAM_CA_FILE" in
  '' | *[!A-Za-z0-9._/-]*)
    echo "pgbouncer: DB_UPSTREAM_CA_FILE no valido: '$DB_UPSTREAM_CA_FILE' (ruta absoluta con [A-Za-z0-9._/-])" >&2
    exit 1
    ;;
  /*) ;;
  *)
    echo "pgbouncer: DB_UPSTREAM_CA_FILE debe ser una ruta absoluta: '$DB_UPSTREAM_CA_FILE'" >&2
    exit 1
    ;;
esac
case "$DB_UPSTREAM_SSLMODE" in
  verify-ca | verify-full)
    if [ ! -r "$DB_UPSTREAM_CA_FILE" ] || ! grep -q 'BEGIN CERTIFICATE' "$DB_UPSTREAM_CA_FILE"; then
      echo "pgbouncer: DB_UPSTREAM_SSLMODE=$DB_UPSTREAM_SSLMODE sin una CA legible en DB_UPSTREAM_CA_FILE ($DB_UPSTREAM_CA_FILE)" >&2
      exit 1
    fi
    ;;
esac

# Credenciales de los clientes (auth_type = plain). El fichero lo genera
# ops/db/pgbouncer-userlist.sh desde el almacen de secretos. Sin el ningun servicio entra y el
# cliente solo ve un 08P01 que parece de la base, asi que se falla aqui con la causa.
auth_file="$userlist"
if [ -e "$userlist" ] && [ ! -r "$userlist" ]; then
  echo "pgbouncer: no se puede leer $userlist (modo 640 y grupo 70, el uid del pooler)" >&2
  exit 1
fi
if [ ! -s "$userlist" ]; then
  if $local_env && [ -n "${POSTGRES_PASSWORD:-}" ]; then
    # Solo desarrollo y pruebas: el rol de plataforma, en /tmp del contenedor y nunca en el
    # directorio montado desde el repositorio.
    umask 077
    clave=$(printf '%s' "$POSTGRES_PASSWORD" | sed 's/"/""/g')
    printf '"%s" "%s"\n' "$DB_ADMIN_USER" "$clave" >"$userlist_local"
    unset clave
    auth_file="$userlist_local"
    echo "pgbouncer: sin $userlist; con ENVIRONMENT=$ENVIRONMENT se usa el rol de plataforma ($DB_ADMIN_USER)" >&2
  else
    echo "pgbouncer: falta $userlist; generarlo con ops/db/pgbouncer-userlist.sh --write" >&2
    exit 1
  fi
fi

sed \
  -e "s|__DB_UPSTREAM_HOST__|${DB_UPSTREAM_HOST}|g" \
  -e "s|__DB_UPSTREAM_PORT__|${DB_UPSTREAM_PORT}|g" \
  -e "s|__DB_ADMIN_USER__|${DB_ADMIN_USER}|g" \
  -e "s|__DB_UPSTREAM_SSLMODE__|${DB_UPSTREAM_SSLMODE}|g" \
  -e "s|__DB_UPSTREAM_CA_FILE__|${DB_UPSTREAM_CA_FILE}|g" \
  -e "s|__AUTH_FILE__|${auth_file}|g" \
  "$template" >"$rendered"

exec pgbouncer "$rendered"
