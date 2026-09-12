#!/usr/bin/env bash
# Crea (idempotente) un repositorio ECR por cada servicio con build y le aplica
# la lifecycle policy. Reejecutable sin efectos: create-repository ignora el
# error "ya existe". Procesa en paralelo para acotar el tiempo total.
set -uo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$DIR/lib.sh"

export ECR_NAMESPACE ECR_REGION
export ECR_LIFECYCLE="$DIR/lifecycle-policy.json"

ensure_repo() {
  local svc="$1" repo="${ECR_NAMESPACE}/$1"
  if aws ecr create-repository \
      --repository-name "$repo" \
      --image-tag-mutability MUTABLE \
      --image-scanning-configuration scanOnPush=true \
      --region "$ECR_REGION" >/dev/null 2>&1; then
    echo "+ creado: $repo"
  fi
  # El motivo del fallo se imprime: casi siempre es AccessDenied, no una politica
  # mal escrita, y sin el mensaje el operador busca el error donde no esta. Un
  # repo sin lifecycle acumula imagenes sin limite y eso no da sintoma, solo
  # factura; ver "Lifecycle policy" en el README.
  local err
  if ! err=$(aws ecr put-lifecycle-policy \
    --repository-name "$repo" \
    --lifecycle-policy-text "file://$ECR_LIFECYCLE" \
    --region "$ECR_REGION" 2>&1 >/dev/null); then
    echo "! lifecycle fallo: $repo -- ${err##*: }"
  fi
}
export -f ensure_repo

ecr_build_services | xargs -P 10 -I {} bash -c 'ensure_repo "$@"' _ {}
echo "== repos ECR procesados (namespace=$ECR_NAMESPACE region=$ECR_REGION) =="
