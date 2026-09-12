#!/usr/bin/env bash
# Etiqueta las imagenes locales (<proyecto>-<servicio>:latest) hacia ECR y las
# publica. Sin argumentos publica todos los servicios con build; con argumentos
# publica solo los indicados:  ./push.sh gateway identity
#
# Tag de publicacion: IMAGE_TAG (por defecto "latest"). Si se pasa un tag
# distinto, ademas se reetiqueta "latest" para que los servidores que hacen
# pull por defecto reciban la ultima version.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$DIR/lib.sh"

TAG="${IMAGE_TAG:-latest}"

services=("$@")
if [ "${#services[@]}" -eq 0 ]; then
  mapfile -t services < <(ecr_build_services)
fi

ecr_login

ok=0; fail=0; skipped=0
for svc in "${services[@]}"; do
  src="${COMPOSE_PROJECT}-${svc}:latest"
  dst="${ECR_REGISTRY}/${ECR_NAMESPACE}/${svc}:${TAG}"

  if ! $DOCKER image inspect "$src" >/dev/null 2>&1; then
    echo "! sin imagen local: $src (omitido)"; skipped=$((skipped + 1)); continue
  fi

  $DOCKER tag "$src" "$dst"
  if $DOCKER push "$dst" >/dev/null; then
    ok=$((ok + 1)); echo "+ $dst"
    if [ "$TAG" != "latest" ]; then
      latest="${ECR_REGISTRY}/${ECR_NAMESPACE}/${svc}:latest"
      $DOCKER tag "$src" "$latest"
      $DOCKER push "$latest" >/dev/null && echo "  + $latest"
    fi
  else
    fail=$((fail + 1)); echo "! fallo push: $dst"
  fi
done

echo "== ECR push: ok=$ok fail=$fail omitidos=$skipped tag=$TAG =="
[ "$fail" -eq 0 ]
