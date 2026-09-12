#!/usr/bin/env bash
# Setup UNICO para D-3: rol IAM que GitHub Actions asume via OIDC (sin secretos
# estaticos) con permiso de push a ECR. Ejecutar UNA vez con credenciales admin de la
# cuenta (CloudShell de la consola AWS es lo mas simple). Luego, en GitHub:
#   Settings > Secrets and variables > Actions:
#     - Variable AWS_ECR_ROLE_ARN = <arn que imprime este script>
#     - Secret  DEPLOY_SSH_KEY    = contenido de ~/.ssh/core-force-mail-prod.pem
#   Settings > Environments > "production" > Required reviewers (el clic de aprobacion).
set -euo pipefail
REPO="${1:-alonsosss/corforce-email}"
ACC=$(aws sts get-caller-identity --query Account --output text)

# Proveedor OIDC de GitHub (idempotente: falla si existe, se ignora)
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com 2>/dev/null || echo "(proveedor OIDC ya existe)"

cat > /tmp/trust.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
 "Principal":{"Federated":"arn:aws:iam::${ACC}:oidc-provider/token.actions.githubusercontent.com"},
 "Action":"sts:AssumeRoleWithWebIdentity",
 "Condition":{"StringEquals":{"token.actions.githubusercontent.com:aud":"sts.amazonaws.com"},
  "StringLike":{"token.actions.githubusercontent.com:sub":"repo:${REPO}:*"}}}]}
EOF
aws iam create-role --role-name github-ecr-push \
  --assume-role-policy-document file:///tmp/trust.json 2>/dev/null || echo "(rol ya existe)"

cat > /tmp/ecr.json <<EOF
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},
 {"Effect":"Allow","Action":["ecr:BatchCheckLayerAvailability","ecr:PutImage",
  "ecr:InitiateLayerUpload","ecr:UploadLayerPart","ecr:CompleteLayerUpload",
  "ecr:BatchGetImage","ecr:GetDownloadUrlForLayer","ecr:CreateRepository","ecr:DescribeRepositories"],
  "Resource":"arn:aws:ecr:us-east-1:${ACC}:repository/core-force/*"}]}
EOF
aws iam put-role-policy --role-name github-ecr-push \
  --policy-name ecr-push-core-force --policy-document file:///tmp/ecr.json

echo "LISTO. Variable de repo AWS_ECR_ROLE_ARN = arn:aws:iam::${ACC}:role/github-ecr-push"
