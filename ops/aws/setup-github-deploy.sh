#!/usr/bin/env bash
# Setup UNICO para que el boton "Desplegar" de GitHub Actions funcione.
#
# El job de deploy entra al servidor por SSM Run Command en vez de por SSH: el Security
# Group solo admite el puerto 22 desde la IP del administrador en /32 y los runners de
# GitHub salen por IPs arbitrarias. Por SSM no hay que abrir NINGUN puerto de entrada
# -el agente de la instancia abre la conexion hacia AWS-, asi que el 22 sigue cerrado.
#
# Hacen falta dos permisos, en dos roles distintos:
#   - el rol de la INSTANCIA, para que el agente se registre en SSM;
#   - el rol OIDC de GITHUB, para que el workflow pueda mandarle comandos.
#
# Idempotente: se puede repetir sin romper nada.
#
# Uso (con credenciales de administrador de AWS):
#   ops/aws/setup-github-deploy.sh [id-de-instancia]
set -euo pipefail

REGION="${AWS_REGION:-us-east-1}"
ROL_INSTANCIA="${DEPLOY_INSTANCE_ROLE:-core-force-mail-ec2-role}"
ROL_GITHUB="${GITHUB_OIDC_ROLE:-github-ecr-push}"
INSTANCIA="${1:-${DEPLOY_INSTANCE_ID:-}}"

ACC="$(aws sts get-caller-identity --query Account --output text)"
echo ">> cuenta $ACC, region $REGION"

if [[ -z "$INSTANCIA" ]]; then
  echo ">> sin id de instancia: se busca por el rol $ROL_INSTANCIA"
  INSTANCIA="$(aws ec2 describe-instances --region "$REGION" \
    --filters "Name=instance-state-name,Values=running" \
    --query "Reservations[].Instances[?IamInstanceProfile!=null]|[0][?contains(IamInstanceProfile.Arn, '$ROL_INSTANCIA')].InstanceId | [0]" \
    --output text 2>/dev/null || true)"
fi
[[ -n "$INSTANCIA" && "$INSTANCIA" != "None" ]] || {
  echo "no se pudo determinar la instancia: pasala como argumento" >&2; exit 1; }
echo ">> instancia $INSTANCIA"

# ── 1. El agente de la instancia tiene que poder registrarse en SSM ───────────
# AmazonSSMManagedInstanceCore es la politica gestionada por AWS para esto. Sin ella el
# agente corre pero nunca aparece como "Online", y send-command falla con
# InvalidInstanceId, que no dice en absoluto que el problema sea de permisos.
aws iam attach-role-policy --role-name "$ROL_INSTANCIA" \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
echo ">> $ROL_INSTANCIA: AmazonSSMManagedInstanceCore adjunta"

# ── 2. El rol de GitHub tiene que poder mandar comandos A ESA instancia ───────
# Acotado a la instancia y al documento AWS-RunShellScript: el rol no puede tocar ninguna
# otra maquina de la cuenta ni ejecutar otros documentos. Las lecturas de estado no
# admiten recurso, asi que van con "*"; no cambian nada.
POLITICA="$(mktemp)"; trap 'rm -f "$POLITICA"' EXIT
cat > "$POLITICA" <<JSON
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"ssm:SendCommand",
  "Resource":["arn:aws:ec2:${REGION}:${ACC}:instance/${INSTANCIA}",
              "arn:aws:ssm:${REGION}::document/AWS-RunShellScript"]},
 {"Effect":"Allow","Action":["ssm:GetCommandInvocation","ssm:ListCommandInvocations",
                             "ssm:DescribeInstanceInformation"],
  "Resource":"*"}]}
JSON
aws iam put-role-policy --role-name "$ROL_GITHUB" \
  --policy-name deploy-por-ssm --policy-document "file://$POLITICA"
echo ">> $ROL_GITHUB: politica deploy-por-ssm puesta"

# ── 3. Comprobacion ──────────────────────────────────────────────────────────
# Un cambio de permisos IAM tarda en propagar: el primer intento puede fallar aunque todo
# este bien puesto. Por eso se reintenta en vez de dar un veredicto a la primera.
echo ">> esperando a que la instancia aparezca como Online en SSM"
for i in $(seq 1 20); do
  ping="$(aws ssm describe-instance-information --region "$REGION" \
    --filters "Key=InstanceIds,Values=$INSTANCIA" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || true)"
  [[ "$ping" == "Online" ]] && break
  sleep 15
done

if [[ "${ping:-}" == "Online" ]]; then
  echo ">> LISTO. La instancia responde por SSM."
else
  echo ">> La instancia AUN no aparece Online (ping='${ping:-vacio}')."
  echo "   Suele ser propagacion de IAM. Si sigue asi en unos minutos, reinicia el agente:"
  echo "     sudo snap restart amazon-ssm-agent   (o: sudo systemctl restart amazon-ssm-agent)"
fi

cat <<TXT

Ultimo paso, en GitHub (Settings > Secrets and variables > Actions > Variables):

    DEPLOY_INSTANCE_ID = $INSTANCIA

Con eso el boton queda operativo: Actions > Release > Run workflow, marcando "deploy".
El secreto DEPLOY_SSH_KEY deja de usarlo el workflow; puede retirarse.
TXT
