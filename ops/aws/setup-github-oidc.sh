#!/usr/bin/env bash
# Setup UNICO para .github/workflows/release.yml: rol IAM que GitHub Actions asume via
# OIDC (sin secretos estaticos) con permiso de push a ECR. Ejecutar UNA vez con
# credenciales admin de la cuenta (CloudShell de la consola AWS es lo mas simple); correrlo
# de nuevo deja la confianza y la politica como dice este archivo. Luego, en GitHub,
# Settings > Secrets and variables > Actions > Variables:
#   - AWS_ECR_ROLE_ARN = <arn que imprime este script>
# Hasta entonces el workflow detecta los servicios y no publica. El despliegue desde
# GitHub necesita ademas ops/aws/setup-github-deploy.sh (variable DEPLOY_INSTANCE_ID).
# Tienen que coincidir con release.yml: AWS_REGION y ECR_NAMESPACE con ECR_REGION y ECR_NS,
# RELEASE_BRANCH con la rama de on.push y DEPLOY_ENVIRONMENT con el environment del job
# deploy.
set -euo pipefail
REPO="${1:-alonsosss/corforce-email}"
RAMA="${RELEASE_BRANCH:-main}"
ENTORNO="${DEPLOY_ENVIRONMENT:-production}"
ROL="${GITHUB_OIDC_ROLE:-github-ecr-push}"
REGION="${AWS_REGION:-us-east-1}"
NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"

# Cada valor entra en la condicion de confianza o en un ARN: un comodin colado por una
# variable ampliaria quien puede asumir el rol.
valida() { [[ "$2" =~ $3 ]] || { echo "FALLA: $1 no es valido: '$2'" >&2; exit 1; }; }
valida repositorio "$REPO" '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
valida RELEASE_BRANCH "$RAMA" '^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$'
valida DEPLOY_ENVIRONMENT "$ENTORNO" '^[A-Za-z0-9._-]+$'
valida GITHUB_OIDC_ROLE "$ROL" '^[A-Za-z0-9+=,.@_-]{1,64}$'
valida AWS_REGION "$REGION" '^[a-z]{2}(-[a-z]+)+-[0-9]+$'
valida ECR_NAMESPACE "$NAMESPACE" '^[a-z0-9]+([._/-][a-z0-9]+)*$'

ACC=$(aws sts get-caller-identity --query Account --output text)
valida cuenta "$ACC" '^[0-9]{12}$'

# Directorio privado y efimero: una ruta fija en /tmp la puede preparar antes otro
# usuario de la maquina.
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# Proveedor OIDC de GitHub (idempotente: falla si existe, se ignora)
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com 2>/dev/null || echo "(proveedor OIDC ya existe)"

# Quien asume el rol, por el claim sub del token de GitHub
# (https://docs.github.com/en/actions/reference/security/oidc):
#   repo:<dueno>/<repo>:ref:refs/heads/<rama>   job sin environment: build-push de un push a
#                                               main o de un workflow_dispatch desde main
#   repo:<dueno>/<repo>:environment:<entorno>   job con environment: deploy
# StringEquals y no StringLike: ni otra rama, ni un pull_request, ni otro environment. El
# claim de un job con environment no lleva la rama: release.yml solo despliega tras publicar
# desde main, pero otra ejecucion que declare ese environment obtendria el mismo claim, y eso
# solo lo cierra la regla de ramas del environment en GitHub.
cat > "$tmp/trust.json" <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
 "Principal":{"Federated":"arn:aws:iam::${ACC}:oidc-provider/token.actions.githubusercontent.com"},
 "Action":"sts:AssumeRoleWithWebIdentity",
 "Condition":{"StringEquals":{
  "token.actions.githubusercontent.com:aud":"sts.amazonaws.com",
  "token.actions.githubusercontent.com:sub":["repo:${REPO}:ref:refs/heads/${RAMA}","repo:${REPO}:environment:${ENTORNO}"]}}}]}
EOF
python3 -m json.tool "$tmp/trust.json" >/dev/null

# Crear un rol que ya existe falla y le deja la confianza anterior: se reescribe, para que
# acotarla aqui llegue tambien a la cuenta donde ya estaba.
if aws iam get-role --role-name "$ROL" >/dev/null 2>&1; then
  aws iam update-assume-role-policy --role-name "$ROL" --policy-document "file://$tmp/trust.json"
  echo "(rol $ROL ya existia: confianza reescrita)"
else
  aws iam create-role --role-name "$ROL" --assume-role-policy-document "file://$tmp/trust.json" >/dev/null
  echo "(rol $ROL creado)"
fi

cat > "$tmp/ecr.json" <<EOF
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},
 {"Effect":"Allow","Action":["ecr:BatchCheckLayerAvailability","ecr:PutImage",
  "ecr:InitiateLayerUpload","ecr:UploadLayerPart","ecr:CompleteLayerUpload",
  "ecr:BatchGetImage","ecr:GetDownloadUrlForLayer","ecr:CreateRepository","ecr:DescribeRepositories"],
  "Resource":"arn:aws:ecr:${REGION}:${ACC}:repository/${NAMESPACE}/*"}]}
EOF
aws iam put-role-policy --role-name "$ROL" \
  --policy-name ecr-push-core-force-mail --policy-document "file://$tmp/ecr.json"

echo "LISTO. Variable de repo AWS_ECR_ROLE_ARN = arn:aws:iam::${ACC}:role/${ROL}"
