#!/usr/bin/env bash
# Ejecuta un comando con los secretos de produccion cargados en el entorno.
#
#   ops/security/secrets/with-secrets.sh docker compose up -d --no-deps gateway
#
# Hacen falta las dos vias: el fichero de secretos alimenta a los contenedores via env_file,
# pero la interpolacion de ${VAR} dentro de docker-compose.yml la resuelve Compose contra
# el entorno del proceso, no contra env_file. Cargarlos aqui cubre ambas.
#
# La resolucion vive en load.sh, que es tambien lo que sourcean los scripts que necesitan las
# credenciales para si mismos: respaldo, restauracion y aplicacion de migraciones. Una sola
# implementacion, para que cambiar de donde salen los secretos mueva todos los caminos a la
# vez y no solo los del despliegue.
#
# Es el unico camino del servidor hacia docker compose (check-secret-sources.sh lo exige), asi
# que aqui se niega tambien a desplegar sobre un .env que no declara production o staging.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "$SCRIPT_DIR/require-server-environment.sh"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/load.sh"

exec "$@"
