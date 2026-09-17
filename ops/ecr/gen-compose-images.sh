#!/usr/bin/env bash
# Genera los dos overrides de imagen de la plataforma, uno por transporte, para cada servicio
# propio (los que tienen build: en el compose):
#
#   docker-compose.images.yml       ECR: el servidor hace PULL en lugar de compilar. Requiere
#                                   ECR_REGISTRY en el entorno (p. ej.
#                                   <account>.dkr.ecr.us-east-1.amazonaws.com) y DEPLOY_TAG
#                                   opcional (def latest).
#   docker-compose.images.save.yml  transporte save (sin AWS): la imagen la construye el PC y
#                                   viaja con docker save | ssh docker load, asi que no hay
#                                   registro del que tirar (pull_policy: never) y la etiqueta es
#                                   obligatoria: es el commit desplegado, lo que permite la
#                                   guardia de retroceso, la verificacion de la imagen y el
#                                   rollback por etiqueta. Antes este camino levantaba
#                                   app-<svc>:latest y no se podia saber que codigo corria.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$HERE/../../docker-compose.yml}" \
  ECR_ACCOUNT_ID="${ECR_ACCOUNT_ID:-0}" # evita llamada STS al solo listar servicios
OUT="${1:-$HERE/../../docker-compose.images.yml}"
OUT_SAVE="${2:-$HERE/../../docker-compose.images.save.yml}"

services=$(awk '
  /^  [a-z0-9][a-z0-9_-]*:[[:space:]]*$/ { svc=$1; sub(/:.*/, "", svc); next }
  /^    build:/ { if (svc != "") { print svc; svc="" } }
' "$COMPOSE_FILE")

{
  echo "# GENERADO por ops/ecr/gen-compose-images.sh - no editar a mano."
  echo "# Uso: docker compose -f docker-compose.yml -f docker-compose.images.yml pull <svc> && up -d"
  echo "services:"
  for s in $services; do
    printf '  %s:\n    image: ${ECR_REGISTRY:?definir en .env}/core-force-mail/%s:${DEPLOY_TAG:-latest}\n' "$s" "$s"
  done
} > "$OUT"

{
  echo "# GENERADO por ops/ecr/gen-compose-images.sh - no editar a mano."
  echo "# Uso (transporte save, sin registro): DEPLOY_TAG=<commit> docker compose -f docker-compose.yml \\"
  echo "#      -f docker-compose.images.save.yml up -d --no-build <svc>"
  echo "services:"
  for s in $services; do
    printf '  %s:\n    image: core-force-mail/%s:${DEPLOY_TAG:?lo fija scripts/deploy-ecr.sh}\n    pull_policy: never\n' "$s" "$s"
  done
} > "$OUT_SAVE"
echo "generado: $OUT y $OUT_SAVE ($(echo "$services" | wc -l) servicios)"
