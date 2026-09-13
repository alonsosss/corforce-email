#!/usr/bin/env bash
# Genera docker-compose.images.yml: override que fija image: de ECR para cada servicio
# propio (los que tienen build: en el compose). Con este override, el servidor levanta
# con `-f docker-compose.yml -f docker-compose.images.yml` y hace PULL en lugar de
# compilar. Requiere ECR_REGISTRY en el entorno
# (p. ej. <account>.dkr.ecr.us-east-1.amazonaws.com) y DEPLOY_TAG opcional (def latest).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$HERE/../../docker-compose.yml}" \
  ECR_ACCOUNT_ID="${ECR_ACCOUNT_ID:-0}" # evita llamada STS al solo listar servicios
OUT="${1:-$HERE/../../docker-compose.images.yml}"

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
echo "generado: $OUT ($(echo "$services" | wc -l) servicios)"
