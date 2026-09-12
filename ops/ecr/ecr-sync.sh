#!/usr/bin/env bash
# Sincroniza a ECR el tag :latest de todas las imágenes propias presentes en el
# host (<proyecto>-<servicio>:latest). Autónomo: deriva la lista de las imágenes
# reales, sin depender del docker-compose.yml. Pensado para correr como root vía
# systemd (red de seguridad ante rebuilds hechos fuera de deploy-fast.sh).
set -uo pipefail

ECR_REGION="${ECR_REGION:-us-east-1}"
ECR_NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"
PROJECT="${COMPOSE_PROJECT:-app}"
DOCKER="${DOCKER:-docker}"

ACCOUNT="$(aws sts get-caller-identity --query Account --output text)"
REGISTRY="${ACCOUNT}.dkr.ecr.${ECR_REGION}.amazonaws.com"

aws ecr get-login-password --region "$ECR_REGION" \
  | $DOCKER login --username AWS --password-stdin "$REGISTRY" >/dev/null

ok=0; fail=0
while IFS= read -r img; do
  name="${img%:*}"               # app-gateway
  svc="${name#"${PROJECT}-"}"    # gateway
  dst="${REGISTRY}/${ECR_NAMESPACE}/${svc}:latest"
  if $DOCKER tag "$img" "$dst" && $DOCKER push "$dst" >/dev/null; then
    ok=$((ok + 1))
  else
    fail=$((fail + 1)); echo "! fallo: $svc"
  fi
done < <($DOCKER images --format '{{.Repository}}:{{.Tag}}' | grep -E "^${PROJECT}-[a-z0-9_-]+:latest$")

echo "[$(date -Is)] ECR sync (latest): ok=$ok fail=$fail"
