#!/usr/bin/env bash
# Configuración y utilidades compartidas para publicar las imágenes propias
# de la plataforma en Amazon ECR. No contiene secretos: la autenticación se resuelve
# con el rol IAM de la instancia EC2 (core-force-mail-ec2-role).

set -euo pipefail

ECR_REGION="${ECR_REGION:-us-east-1}"
ECR_NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"

# Cliente docker. En el EC2, el usuario "ubuntu" necesita sudo:
#   DOCKER="sudo docker" ./push.sh
DOCKER="${DOCKER:-docker}"

# Origen de la lista de servicios y nombre de proyecto Compose. Las imágenes
# locales se llaman "<proyecto>-<servicio>:latest" (proyecto por defecto: app).
# El compose vive en la raiz del repositorio, dos niveles por encima de este fichero.
COMPOSE_FILE="${COMPOSE_FILE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/docker-compose.yml}"
COMPOSE_PROJECT="${COMPOSE_PROJECT:-app}"

# La cuenta se descubre con STS (vía el rol), no se fija en código.
ECR_ACCOUNT_ID="${ECR_ACCOUNT_ID:-$(aws sts get-caller-identity --query Account --output text)}"
ECR_REGISTRY="${ECR_ACCOUNT_ID}.dkr.ecr.${ECR_REGION}.amazonaws.com"

# Servicios con sección build: en el compose = imágenes propias a publicar.
ecr_build_services() {
  awk '
    /^  [a-z0-9][a-z0-9_-]*:[[:space:]]*$/ { svc=$1; sub(/:.*/, "", svc); next }
    /^    build:/ { if (svc != "") { print svc; svc="" } }
  ' "$COMPOSE_FILE"
}

ecr_login() {
  aws ecr get-login-password --region "$ECR_REGION" \
    | $DOCKER login --username AWS --password-stdin "$ECR_REGISTRY"
}
