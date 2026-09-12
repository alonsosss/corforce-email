#!/usr/bin/env bash
# Reconcilia la auditoria y la deteccion de la cuenta: CloudTrail, GuardDuty y el bloqueo
# de acceso publico de S3 a nivel de cuenta.
#
# Existe porque el 2026-09-05 se comprobo con el usuario raiz que la cuenta no tenia
# NINGUNA de las tres cosas en ninguna region: solo el historial de 90 dias de eventos de
# gestion que AWS guarda solo, sin eventos de datos, sin validacion de integridad y que
# desaparece a los tres meses. Despues de un incidente, "que paso" no tenia respuesta.
#
# Idempotente: correrlo de nuevo deja todo como dice este archivo y no duplica nada. Como
# setup-iam.sh, el rol de la instancia NO puede ejecutarlo -si pudiera, comprometer la
# instancia seria poder apagar la auditoria- y se corre con credenciales de administrador.
#
# Uso:
#   ops/aws/setup-auditoria.sh                  # aplica trail y bloqueo publico
#   ops/aws/setup-auditoria.sh --check          # solo informa, no toca nada
#   GUARDDUTY=1 ops/aws/setup-auditoria.sh      # ademas activa GuardDuty (ver abajo)
#
# Coste: el primer trail de eventos de gestion no se cobra; el almacenamiento en S3 es
# de centavos a este volumen. Los eventos de DATOS del bucket de respaldos se cobran por
# cada cien mil, y ese bucket recibe unas decenas de objetos al dia.
#
# GuardDuty es OPT-IN (GUARDDUTY=1) y por defecto este script NO lo toca, ni para activar
# ni para apagar. Se activo y se retiro el mismo dia (2026-09-05) por decision del
# responsable: mientras el sistema esta en desarrollo activo se prefiere no sumar un coste
# recurrente aun sin cifra real. Cuando se active: 4 USD por millon de eventos de
# CloudTrail y 1 USD/GB de flujos VPC y DNS analizados (con descuento por volumen); la
# prueba gratuita es de treinta dias por cuenta, y ya consumio parte ese dia. La cifra real
# la da 'aws guardduty get-usage-statistics' pasados unos dias.
set -euo pipefail

CHECK=0
[[ "${1:-}" == "--check" ]] && CHECK=1
GUARDDUTY="${GUARDDUTY:-0}"

REGION="${AWS_REGION:-us-east-1}"
TRAIL="${TRAIL_NAME:-core-force-auditoria}"

command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
ACC="$(aws sts get-caller-identity --query Account --output text)" || {
  echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }

# El bucket de respaldos es el unico cuyos accesos a OBJETOS se registran: es la forma de
# saber si alguien los lee o los descarga, que es la pregunta que mas importa tras un
# incidente. Los demas buckets generan demasiados eventos por lo poco que aportarian.
: "${AUDIT_BUCKET:=cf-auditoria-${ACC}}"
: "${BACKUP_BUCKET:=cf-backups-${ACC}}"
: "${RETENCION_DIAS:=400}"   # algo mas de un ano: cubre un ejercicio fiscal con margen

echo "Cuenta $ACC / region $REGION / trail $TRAIL / bucket $AUDIT_BUCKET"
[[ $CHECK -eq 1 ]] && echo "(modo --check: no se aplica nada)"
echo

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# Muestra el estado de un punto y, si no esta al dia y no es --check, ejecuta el cierre.
paso() {
  local nombre="$1" aldia="$2"; shift 2
  if [[ "$aldia" == "1" ]]; then
    printf '  %-40s al dia\n' "$nombre"; return 0
  fi
  if [[ $CHECK -eq 1 ]]; then
    printf '  %-40s FALTA\n' "$nombre"; return 0
  fi
  "$@"
  printf '  %-40s aplicado\n' "$nombre"
}

# --- 1. Bloqueo de acceso publico a nivel de CUENTA -----------------------------------
# Los buckets actuales ya lo tienen cada uno; a nivel de cuenta cubre tambien a cualquier
# bucket futuro creado con prisa. No afecta a los que ya lo tienen.
pab_ok=0
if aws s3control get-public-access-block --account-id "$ACC" --region "$REGION" \
     --query 'PublicAccessBlockConfiguration' --output json 2>/dev/null \
   | python3 -c 'import json,sys
raw=sys.stdin.read().strip()
d=json.loads(raw) if raw else {}
sys.exit(0 if all(d.get(k) for k in ("BlockPublicAcls","IgnorePublicAcls","BlockPublicPolicy","RestrictPublicBuckets")) else 1)'; then
  pab_ok=1
fi
paso "bloqueo publico S3 (cuenta)" "$pab_ok" \
  aws s3control put-public-access-block --account-id "$ACC" --region "$REGION" \
    --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

# --- 2. Bucket de auditoria -------------------------------------------------------------
# Cerrado al publico, versionado (un log borrado sigue existiendo como version), cifrado,
# con caducidad, y solo por TLS. CloudTrail escribe con su propio principal de servicio y
# la politica lo acota a ESTE trail: otro trail de otra cuenta no podria escribir aqui.
bucket_ok=0
aws s3api head-bucket --bucket "$AUDIT_BUCKET" 2>/dev/null && bucket_ok=1
crear_bucket() {
  if [[ "$REGION" == "us-east-1" ]]; then
    aws s3api create-bucket --bucket "$AUDIT_BUCKET" --region "$REGION" >/dev/null
  else
    aws s3api create-bucket --bucket "$AUDIT_BUCKET" --region "$REGION" \
      --create-bucket-configuration "LocationConstraint=$REGION" >/dev/null
  fi
}
paso "bucket $AUDIT_BUCKET" "$bucket_ok" crear_bucket

# Los ajustes del bucket se reconcilian siempre (son idempotentes y baratos), salvo en --check.
if [[ $CHECK -eq 0 ]]; then
  aws s3api put-public-access-block --bucket "$AUDIT_BUCKET" \
    --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
  aws s3api put-bucket-versioning --bucket "$AUDIT_BUCKET" --versioning-configuration Status=Enabled
  aws s3api put-bucket-encryption --bucket "$AUDIT_BUCKET" --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'
  aws s3api put-bucket-lifecycle-configuration --bucket "$AUDIT_BUCKET" --lifecycle-configuration "{
    \"Rules\":[{\"ID\":\"caducidad\",\"Status\":\"Enabled\",\"Filter\":{\"Prefix\":\"\"},
      \"Expiration\":{\"Days\":$RETENCION_DIAS},
      \"NoncurrentVersionExpiration\":{\"NoncurrentDays\":30},
      \"AbortIncompleteMultipartUpload\":{\"DaysAfterInitiation\":7}}]}" >/dev/null
  cat > "$tmp/bucket-policy.json" <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"CloudTrailCompruebaAcl","Effect":"Allow",
  "Principal":{"Service":"cloudtrail.amazonaws.com"},
  "Action":"s3:GetBucketAcl","Resource":"arn:aws:s3:::${AUDIT_BUCKET}",
  "Condition":{"StringEquals":{"aws:SourceArn":"arn:aws:cloudtrail:${REGION}:${ACC}:trail/${TRAIL}"}}},
 {"Sid":"CloudTrailEscribe","Effect":"Allow",
  "Principal":{"Service":"cloudtrail.amazonaws.com"},
  "Action":"s3:PutObject","Resource":"arn:aws:s3:::${AUDIT_BUCKET}/AWSLogs/${ACC}/*",
  "Condition":{"StringEquals":{"s3:x-amz-acl":"bucket-owner-full-control",
                               "aws:SourceArn":"arn:aws:cloudtrail:${REGION}:${ACC}:trail/${TRAIL}"}}},
 {"Sid":"SoloTLS","Effect":"Deny","Principal":"*","Action":"s3:*",
  "Resource":["arn:aws:s3:::${AUDIT_BUCKET}","arn:aws:s3:::${AUDIT_BUCKET}/*"],
  "Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}
EOF
  aws s3api put-bucket-policy --bucket "$AUDIT_BUCKET" --policy "file://$tmp/bucket-policy.json"
  printf '  %-40s reconciliados\n' "ajustes del bucket"
fi

# --- 3. Trail multi-region con validacion de integridad ---------------------------------
# Multi-region: un atacante que use la credencial en otra region tambien queda registrado.
# Validacion: cada fichero lleva un resumen firmado; un log manipulado se detecta.
trail_ok=0
if aws cloudtrail get-trail --name "$TRAIL" --region "$REGION" --query 'Trail.[IsMultiRegionTrail,LogFileValidationEnabled]' --output text 2>/dev/null \
   | grep -q '^True.True$'; then
  trail_ok=1
fi
crear_trail() {
  if aws cloudtrail get-trail --name "$TRAIL" --region "$REGION" >/dev/null 2>&1; then
    aws cloudtrail update-trail --name "$TRAIL" --region "$REGION" --s3-bucket-name "$AUDIT_BUCKET" \
      --is-multi-region-trail --enable-log-file-validation --include-global-service-events >/dev/null
  else
    aws cloudtrail create-trail --name "$TRAIL" --region "$REGION" --s3-bucket-name "$AUDIT_BUCKET" \
      --is-multi-region-trail --enable-log-file-validation --include-global-service-events >/dev/null
  fi
}
paso "trail $TRAIL (multi-region, validado)" "$trail_ok" crear_trail

logging_ok=0
aws cloudtrail get-trail-status --name "$TRAIL" --region "$REGION" --query IsLogging --output text 2>/dev/null | grep -q True && logging_ok=1
paso "trail registrando" "$logging_ok" aws cloudtrail start-logging --name "$TRAIL" --region "$REGION"

# Eventos de datos SOLO del bucket de respaldos: lecturas y escrituras de objetos.
selectores_ok=0
if aws cloudtrail get-event-selectors --trail-name "$TRAIL" --region "$REGION" --output json 2>/dev/null \
   | grep -q "arn:aws:s3:::${BACKUP_BUCKET}/"; then
  selectores_ok=1
fi
poner_selectores() {
  aws cloudtrail put-event-selectors --trail-name "$TRAIL" --region "$REGION" --advanced-event-selectors "[
    {\"Name\":\"Gestion\",\"FieldSelectors\":[{\"Field\":\"eventCategory\",\"Equals\":[\"Management\"]}]},
    {\"Name\":\"ObjetosDeRespaldo\",\"FieldSelectors\":[
      {\"Field\":\"eventCategory\",\"Equals\":[\"Data\"]},
      {\"Field\":\"resources.type\",\"Equals\":[\"AWS::S3::Object\"]},
      {\"Field\":\"resources.ARN\",\"StartsWith\":[\"arn:aws:s3:::${BACKUP_BUCKET}/\"]}]}]" >/dev/null
}
paso "eventos de datos del bucket de respaldos" "$selectores_ok" poner_selectores

# --- 4. GuardDuty ---------------------------------------------------------------------------
# Sin agentes: analiza CloudTrail, los flujos de VPC y las consultas DNS que AWS ya ve.
# Detecta lo que ninguna alerta propia puede: una credencial usada desde otro pais, la
# instancia hablando con una IP de mineria o de mando y control, escaneos de puertos.
gd_ok=0
det="$(aws guardduty list-detectors --region "$REGION" --query 'DetectorIds[0]' --output text 2>/dev/null || true)"
if [[ -n "$det" && "$det" != "None" ]]; then
  aws guardduty get-detector --detector-id "$det" --region "$REGION" --query Status --output text 2>/dev/null | grep -q ENABLED && gd_ok=1
fi
activar_guardduty() {
  if [[ -n "$det" && "$det" != "None" ]]; then
    aws guardduty update-detector --detector-id "$det" --region "$REGION" --enable >/dev/null
  else
    aws guardduty create-detector --region "$REGION" --enable --finding-publishing-frequency FIFTEEN_MINUTES >/dev/null
  fi
}
if [[ "$GUARDDUTY" == "1" ]]; then
  paso "GuardDuty en $REGION" "$gd_ok" activar_guardduty
else
  printf '  %-40s %s\n' "GuardDuty en $REGION" "$([[ $gd_ok -eq 1 ]] && echo 'activo (no gestionado sin GUARDDUTY=1)' || echo 'no activado (opt-in: GUARDDUTY=1)')"
fi

echo
if [[ $CHECK -eq 1 ]]; then
  echo "Nada aplicado. Corre sin --check para dejar la auditoria como dice este archivo."
else
  echo "Listo. Los primeros ficheros del trail tardan unos minutos en aparecer en s3://$AUDIT_BUCKET/AWSLogs/$ACC/."
  if [[ "$GUARDDUTY" == "1" ]]; then
    echo "La estimacion de coste de GuardDuty sale pasados unos dias con:"
    echo "  aws guardduty get-usage-statistics --detector-id <id> --usage-statistic-type SUM_BY_DATA_SOURCE --usage-criteria DataSources=CLOUD_TRAIL,DNS_LOGS,FLOW_LOGS"
  fi
fi
