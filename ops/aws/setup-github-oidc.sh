#!/usr/bin/env bash
# Setup UNICO para .github/workflows/release.yml: rol IAM que GitHub Actions asume via
# OIDC (sin secretos estaticos) con permiso de push a ECR. Ejecutar UNA vez con
# credenciales admin de la cuenta (CloudShell de la consola AWS es lo mas simple). Luego,
# en GitHub, Settings > Secrets and variables > Actions > Variables:
#   - AWS_ECR_ROLE_ARN = <arn que imprime este script>
# Hasta entonces el workflow detecta los servicios y no publica. El despliegue desde
# GitHub necesita ademas ops/aws/setup-github-deploy.sh (variable DEPLOY_INSTANCE_ID).
# AWS_REGION y ECR_NAMESPACE tienen que coincidir con ECR_REGION y ECR_NS de release.yml.
set -euo pipefail
REPO="${1:-alonsosss/corforce-email}"
REGION="${AWS_REGION:-us-east-1}"
NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"
ACC=$(aws sts get-caller-identity --query Account --output text)

# Directorio privado y efimero: una ruta fija en /tmp la puede preparar antes otro
# usuario de la maquina.
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# Proveedor OIDC de GitHub (idempotente: falla si existe, se ignora)
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com 2>/dev/null || echo "(proveedor OIDC ya existe)"

cat > "$tmp/trust.json" <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
 "Principal":{"Federated":"arn:aws:iam::${ACC}:oidc-provider/token.actions.githubusercontent.com"},
 "Action":"sts:AssumeRoleWithWebIdentity",
 "Condition":{"StringEquals":{"token.actions.githubusercontent.com:aud":"sts.amazonaws.com"},
  "StringLike":{"token.actions.githubusercontent.com:sub":"repo:${REPO}:*"}}}]}
EOF
aws iam create-role --role-name github-ecr-push \
  --assume-role-policy-document "file://$tmp/trust.json" 2>/dev/null || echo "(rol ya existe)"

cat > "$tmp/ecr.json" <<EOF
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},
 {"Effect":"Allow","Action":["ecr:BatchCheckLayerAvailability","ecr:PutImage",
  "ecr:InitiateLayerUpload","ecr:UploadLayerPart","ecr:CompleteLayerUpload",
  "ecr:BatchGetImage","ecr:GetDownloadUrlForLayer","ecr:CreateRepository","ecr:DescribeRepositories"],
  "Resource":"arn:aws:ecr:${REGION}:${ACC}:repository/${NAMESPACE}/*"}]}
EOF
aws iam put-role-policy --role-name github-ecr-push \
  --policy-name ecr-push-core-force-mail --policy-document "file://$tmp/ecr.json"

echo "LISTO. Variable de repo AWS_ECR_ROLE_ARN = arn:aws:iam::${ACC}:role/github-ecr-push"
