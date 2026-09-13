#!/usr/bin/env bash
# Un servidor solo levanta contenedores si su .env declara ENVIRONMENT=production o staging.
#
# development y test relajan controles de los servicios (Redis en claro, credencial de
# plataforma en los servicios de celda, clave de firma efimera en identity:
# docs/Operacion_Despliegue.md, 1) y .env.example trae development para el entorno local.
# Un servidor montado a partir de ese fichero no fallaria en nada: quedaria abierto sin que
# nada lo dijera. with-secrets.sh, por donde pasa todo docker compose del servidor, ejecuta
# esto antes de materializar ningun secreto.
#
# Se juzga el valor que recibiran los contenedores: la ultima asignacion de ENVIRONMENT en el
# .env que Compose les pasa como env_file, no el entorno de quien despliega, que no les
# llega. Solo vale la forma exacta ENVIRONMENT=production o ENVIRONMENT=staging, con comillas
# opcionales: con espacios, un comentario o un CR al final el servicio recibiria otro texto
# y no lo reconoceria como produccion.
set -euo pipefail

env_file="${WITH_SECRETS_ENV_FILE:-${APP_DIR:-.}/.env}"

rechazar() {
  echo "despliegue rechazado: $1" >&2
  echo "  Un servidor declara en $env_file exactamente ENVIRONMENT=production o ENVIRONMENT=staging" >&2
  echo "  (lo escribe ops/server-template/bootstrap.sh). development y test son solo para el" >&2
  echo "  entorno local: make dev, make e2e y make test-integration." >&2
  exit 1
}

[[ -f "$env_file" ]] || rechazar "no existe $env_file"

linea="$(grep -E '^[[:space:]]*(export[[:space:]]+)?ENVIRONMENT[[:space:]]*=' "$env_file" | tail -n 1 || true)"
[[ -n "$linea" ]] || rechazar "$env_file no declara ENVIRONMENT"

case "$linea" in
  ENVIRONMENT=production | ENVIRONMENT=staging | \
  ENVIRONMENT=\"production\" | ENVIRONMENT=\"staging\" | \
  ENVIRONMENT=\'production\' | ENVIRONMENT=\'staging\') ;;
  *) rechazar "ENVIRONMENT no es production ni staging: $(printf '%q' "$linea")" ;;
esac
