#!/usr/bin/env bash
# Reconcilia la salida por Amazon SES de un ambiente con ops/aws/ses-mail.yaml: los dos
# configuration sets (transaccional y marketing), el topic SNS cifrado de sus eventos y la
# suscripcion HTTPS al endpoint publico de transactional.
#
# Idempotente: es una pila de CloudFormation; correrlo de nuevo solo aplica la diferencia.
# Se corre con credenciales de administrador de la cuenta del ambiente, nunca con el rol
# de la instancia.
#
# Uso:
#   SES_EVENTS_URL=https://api.ejemplo.com/api/v1/public/transactional/ses-events \
#     ops/aws/setup-ses.sh prod            # aplica
#   ... ops/aws/setup-ses.sh prod --check  # crea el conjunto de cambios, lo muestra y lo borra
#
# Opcionales: SES_TRACKING_DOMAIN (subdominio propio de seguimiento de marketing, con su
# CNAME y certificado ya publicados), SES_MARKETING_POOL (pool de IP dedicadas existente),
# SES_TRANSACTIONAL_TLS y SES_MARKETING_TLS (OPTIONAL | REQUIRE).
#
# Antes de enviar: el dominio de cada empresa debe estar verificado en SES (DKIM y MAIL
# FROM) y la cuenta fuera del sandbox. Eso no lo crea esta pila.
set -euo pipefail

ENVIRONMENT="${1:-}"
CHECK=0
[[ "${2:-}" == "--check" ]] && CHECK=1
case "$ENVIRONMENT" in dev|staging|prod) ;; *)
  echo "Uso: $0 <dev|staging|prod> [--check]" >&2; exit 2 ;;
esac

: "${SES_EVENTS_URL:?falta SES_EVENTS_URL (URL https de la ruta publica ses-events)}"
[[ "$SES_EVENTS_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9/._-]+$ ]] || {
  echo "FALLA: SES_EVENTS_URL debe ser https y sin parametros: $SES_EVENTS_URL" >&2; exit 2; }
for v in SES_TRACKING_DOMAIN SES_MARKETING_POOL; do
  [[ "${!v:-}" =~ ^[A-Za-z0-9._-]*$ ]] || { echo "FALLA: $v con caracteres no validos" >&2; exit 2; }
done

command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-$(aws configure get region 2>/dev/null || true)}}"
[[ -n "$REGION" ]] || { echo "FALLA: sin region (AWS_REGION)" >&2; exit 1; }
ACC="$(aws sts get-caller-identity --query Account --output text)" || {
  echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STACK="cfm-${ENVIRONMENT}-ses-mail"
PARAMS=(
  "Environment=$ENVIRONMENT"
  "EventsEndpointUrl=$SES_EVENTS_URL"
  "MarketingTrackingDomain=${SES_TRACKING_DOMAIN:-}"
  "MarketingSendingPoolName=${SES_MARKETING_POOL:-}"
  "TransactionalTlsPolicy=${SES_TRANSACTIONAL_TLS:-OPTIONAL}"
  "MarketingTlsPolicy=${SES_MARKETING_TLS:-OPTIONAL}"
)

echo "Cuenta $ACC / region $REGION / pila $STACK"

if [[ $CHECK -eq 1 ]]; then
  echo "(modo --check: no se aplica nada)"
  aws cloudformation deploy --region "$REGION" --stack-name "$STACK" \
    --template-file "$HERE/ses-mail.yaml" --parameter-overrides "${PARAMS[@]}" \
    --tags Project=core-force-mail "Environment=$ENVIRONMENT" \
    --no-execute-changeset --no-fail-on-empty-changeset
  cs="$(aws cloudformation list-change-sets --region "$REGION" --stack-name "$STACK" \
    --query 'Summaries[?Status==`CREATE_COMPLETE`]|[-1].ChangeSetName' --output text 2>/dev/null || true)"
  if [[ -n "$cs" && "$cs" != "None" ]]; then
    aws cloudformation describe-change-set --region "$REGION" --stack-name "$STACK" --change-set-name "$cs" \
      --query 'Changes[].ResourceChange.[Action,LogicalResourceId,ResourceType,Replacement]' --output table
    aws cloudformation delete-change-set --region "$REGION" --stack-name "$STACK" --change-set-name "$cs"
  else
    echo "Sin cambios."
  fi
  exit 0
fi

aws cloudformation deploy --region "$REGION" --stack-name "$STACK" \
  --template-file "$HERE/ses-mail.yaml" --parameter-overrides "${PARAMS[@]}" \
  --tags Project=core-force-mail "Environment=$ENVIRONMENT" --no-fail-on-empty-changeset

echo
echo "Valores para el almacen de configuracion de transactional:"
aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query 'Stacks[0].Outputs[].[Description,OutputValue]' --output text
echo
echo "La suscripcion HTTPS queda pendiente hasta que transactional la confirme al recibir"
echo "el primer mensaje de SNS; compruebalo con:"
echo "  aws sns list-subscriptions-by-topic --region $REGION --topic-arn <EventsTopicArn>"
