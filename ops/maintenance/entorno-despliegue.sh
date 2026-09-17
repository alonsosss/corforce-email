#!/usr/bin/env bash
# Configuracion del despliegue para los trabajos que corren FUERA de un contenedor: el perfil y
# las claves NO secretas del .env de la aplicacion.
#
#   . ops/maintenance/entorno-despliegue.sh
#   cf_read_env BACKUP_DIR            # configuracion, nunca un secreto
#   [[ "$CF_PERFIL_DESPLIEGUE" == selfhosted ]] && ...
#
# Vive aparte porque lo necesitan tres cosas sin relacion entre si: el resolvedor de credenciales
# de Postgres, el respaldo de las bases y el de los volumenes de correo. Cada uno lo leia a su
# manera y el perfil se decidia dos veces.
#
# El perfil lo dice ops/maintenance/perfil-despliegue.sh, el mismo guion que usan los despliegues,
# para que no puedan divergir. Sin .env (un puesto de trabajo o una prueba) es aws, el camino de
# siempre.
#
# Para los SECRETOS existe el almacen (ops/security/secrets/load.sh) y, para los del respaldo,
# ops/backup/destino-externo.sh. Leerlos de aqui es lo que esos ficheros vienen a evitar.
#
# Sin `set -e`: se sourcea.

_cf_entorno_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="${APP_DIR:-/opt/core-force-mail/app}"

# cf_read_env <CLAVE>: el entorno manda; si no, la ultima asignacion del .env, como la lee Compose.
cf_read_env() {
  if [[ -n "${!1:-}" ]]; then
    printf '%s' "${!1}"
  elif [[ -f "$APP_DIR/.env" ]]; then
    sed -n -E "s/^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=(.*)$/\\2/p" "$APP_DIR/.env" | tail -n 1 |
      sed -E "s/^\"(.*)\"$/\\1/; s/^'(.*)'$/\\1/"
  fi
}

# shellcheck disable=SC2034  # la usan los guiones que sourcean este fichero
CF_PERFIL_DESPLIEGUE=aws
if [[ -f "$APP_DIR/.env" ]]; then
  # shellcheck disable=SC2034  # la usan los guiones que sourcean este fichero
  if ! CF_PERFIL_DESPLIEGUE="$(bash "$_cf_entorno_dir/perfil-despliegue.sh" --env "$APP_DIR/.env" --nombre)"; then
    return 1 2>/dev/null || exit 1
  fi
fi
