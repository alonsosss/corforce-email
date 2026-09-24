#!/usr/bin/env bash
# Reconcilia la proteccion de los buckets de la plataforma: versionado, caducidad de
# versiones antiguas y candado de retencion sobre los respaldos.
#
# Crea el bucket de respaldos si no existe (con el candado activado, que conviene desde el alta),
# con el acceso publico bloqueado, cifrado por defecto, solo TLS y el ciclo de vida de
# ops/backup/README.md. El de medios no se crea: en el perfil autoalojado los medios viven en MinIO
# y, si el bucket no existe, se omite. Aqui se declara lo que un bucket creado a mano no trae y cuya
# falta no da sintoma hasta el dia que hace falta: sin versionado, un objeto borrado o sobrescrito
# desaparece sin vuelta atras, y sin candado una credencial con permiso de borrado podria dejar la
# plataforma sin respaldos.
# Idempotente: correrlo de nuevo deja los buckets como dice este archivo.
#
# Coste: ninguno apreciable. El versionado solo guarda copias de lo que se sobrescribe o
# borra, y las versiones no actuales caducan a los 30 dias. El candado no añade
# almacenamiento mientras los respaldos se conserven mas que su retencion (180 dias por
# ciclo de vida, ops/backup/README.md).
#
# Los nombres son las mismas variables que usan ops/aws/setup-iam.sh y los respaldos:
# MEDIA_S3_BUCKET y BACKUP_S3_BUCKET, por defecto derivados de la cuenta.
#
# Uso:
#   ops/aws/setup-buckets.sh            # aplica
#   ops/aws/setup-buckets.sh --check    # solo informa
set -euo pipefail

CHECK=0
[[ "${1:-}" == "--check" ]] && CHECK=1

command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
ACC="$(aws sts get-caller-identity --query Account --output text)" || {
  echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }

# Prefijo cfm-: la cuenta la comparte otro proyecto con buckets cf-*, que nada de aqui debe tocar.
MEDIA_BUCKET="${MEDIA_S3_BUCKET:-cfm-media-${ACC}}"
BACKUP_BUCKET="${BACKUP_S3_BUCKET:-cfm-backups-${ACC}}"
REGION="${AWS_REGION:-us-east-1}"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# Retencion del candado sobre cada respaldo nuevo. Modo GOBERNANZA, no cumplimiento: el
# usuario raiz puede saltarselo con s3:BypassGovernanceRetention si de verdad hace falta,
# pero el rol del servidor no (no tiene ese permiso ni s3:DeleteObject en respaldos). Es
# exactamente el caso que protege: alguien con el servidor tomado no puede borrar lo que
# permitiria recuperarse. En cumplimiento ni el raiz podria, y un error de retencion se
# pagaria en almacenamiento sin remedio.
: "${BACKUP_LOCK_DAYS:=30}"

echo "Cuenta $ACC"
[[ $CHECK -eq 1 ]] && echo "(modo --check: no se aplica nada)"
echo

paso() {
  local nombre="$1" aldia="$2"; shift 2
  if [[ "$aldia" == "1" ]]; then printf '  %-48s al dia\n' "$nombre"; return 0; fi
  if [[ $CHECK -eq 1 ]]; then printf '  %-48s FALTA\n' "$nombre"; return 0; fi
  "$@"
  printf '  %-48s aplicado\n' "$nombre"
}

versionado_ok() {
  aws s3api get-bucket-versioning --bucket "$1" --query Status --output text 2>/dev/null | grep -q '^Enabled$'
}
activar_versionado() {
  aws s3api put-bucket-versioning --bucket "$1" --versioning-configuration Status=Enabled
}

# Versiones no actuales caducan a los 30 dias; subidas a medias se abortan a los 7. Las
# demas reglas del bucket (la caducidad de los respaldos a 180 dias, por ejemplo) se
# conservan: solo se añade o se ajusta la de versiones no actuales.
ciclo_ok() {
  aws s3api get-bucket-lifecycle-configuration --bucket "$1" --output json 2>/dev/null \
    | grep -q '"NoncurrentDays": 30'
}
poner_ciclo() {
  local b="$1" actual regla
  actual="$(aws s3api get-bucket-lifecycle-configuration --bucket "$b" --output json 2>/dev/null || echo '{"Rules":[]}')"
  regla='{"ID":"versiones-antiguas","Status":"Enabled","Filter":{"Prefix":""},
          "NoncurrentVersionExpiration":{"NoncurrentDays":30},
          "AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}'
  python3 - "$actual" "$regla" > "$tmp/ciclo.json" <<'PY'
import json, sys
actual = json.loads(sys.argv[1]); nueva = json.loads(sys.argv[2])
reglas = [r for r in actual.get("Rules", []) if r.get("ID") != nueva["ID"]]
# Si otra regla ya trata las versiones no actuales, se respeta y no se duplica.
if any("NoncurrentVersionExpiration" in r for r in reglas):
    for r in reglas:
        if "NoncurrentVersionExpiration" in r:
            r["NoncurrentVersionExpiration"] = {"NoncurrentDays": 30}
else:
    reglas.append(nueva)
print(json.dumps({"Rules": reglas}))
PY
  aws s3api put-bucket-lifecycle-configuration --bucket "$b" --lifecycle-configuration "file://$tmp/ciclo.json" >/dev/null
}

existe() { aws s3api head-bucket --bucket "$1" >/dev/null 2>&1; }

# --- Medios: lo que suben los usuarios. Sin versionado, un borrado o una sobrescritura
# por error (o por un atacante con el rol) es definitivo.
if existe "$MEDIA_BUCKET"; then
  v=0; versionado_ok "$MEDIA_BUCKET" && v=1
  paso "medios: versionado" "$v" activar_versionado "$MEDIA_BUCKET"
  c=0; ciclo_ok "$MEDIA_BUCKET" && c=1
  paso "medios: versiones antiguas caducan (30 d)" "$c" poner_ciclo "$MEDIA_BUCKET"
else
  printf '  %-48s no existe (medios en MinIO); se omite\n' "medios: $MEDIA_BUCKET"
fi

# --- Respaldos: alta, acceso, cifrado, solo TLS, retencion, versionado y candado.
crear_respaldos() {
  local extra=()
  [[ "$REGION" != us-east-1 ]] && extra+=(--create-bucket-configuration "LocationConstraint=$REGION")
  aws s3api create-bucket --bucket "$BACKUP_BUCKET" --region "$REGION" --object-lock-enabled-for-bucket \
    "${extra[@]}" >/dev/null
}
e=0; existe "$BACKUP_BUCKET" && e=1
paso "respaldos: bucket $BACKUP_BUCKET" "$e" crear_respaldos
if [[ $CHECK -eq 1 && $e -eq 0 ]]; then
  echo "  (el resto se comprueba cuando el bucket exista)"
else
bloqueo_ok() {
  aws s3api get-public-access-block --bucket "$BACKUP_BUCKET" --output json 2>/dev/null | python3 -c '
import json, sys
c = json.load(sys.stdin)["PublicAccessBlockConfiguration"]
sys.exit(0 if all(c.get(k) for k in ("BlockPublicAcls", "IgnorePublicAcls", "BlockPublicPolicy", "RestrictPublicBuckets")) else 1)'
}
poner_bloqueo() {
  aws s3api put-public-access-block --bucket "$BACKUP_BUCKET" --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
}
b=0; bloqueo_ok && b=1
paso "respaldos: acceso publico bloqueado" "$b" poner_bloqueo

cifrado_ok() {
  aws s3api get-bucket-encryption --bucket "$BACKUP_BUCKET" --output json 2>/dev/null | grep -q '"SSEAlgorithm"'
}
poner_cifrado() {
  aws s3api put-bucket-encryption --bucket "$BACKUP_BUCKET" --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'
}
x=0; cifrado_ok && x=1
paso "respaldos: cifrado en reposo" "$x" poner_cifrado

tls_ok() {
  aws s3api get-bucket-policy --bucket "$BACKUP_BUCKET" --query Policy --output text 2>/dev/null | grep -q '"SoloTLS"'
}
poner_tls() {
  aws s3api put-bucket-policy --bucket "$BACKUP_BUCKET" --policy "{\"Version\":\"2012-10-17\",\"Statement\":[
    {\"Sid\":\"SoloTLS\",\"Effect\":\"Deny\",\"Principal\":\"*\",\"Action\":\"s3:*\",
     \"Resource\":[\"arn:aws:s3:::$BACKUP_BUCKET\",\"arn:aws:s3:::$BACKUP_BUCKET/*\"],
     \"Condition\":{\"Bool\":{\"aws:SecureTransport\":\"false\"}}}]}"
}
t=0; tls_ok && t=1
paso "respaldos: solo por TLS" "$t" poner_tls

# Retencion de ops/backup/README.md: a STANDARD_IA a los 30 dias y caducidad a los 180. Se anade
# junto a las demas reglas del bucket, como la de versiones no actuales.
retencion_ok() {
  aws s3api get-bucket-lifecycle-configuration --bucket "$BACKUP_BUCKET" --output json 2>/dev/null \
    | grep -q '"ID": "retencion-respaldos"'
}
poner_retencion() {
  local actual
  actual="$(aws s3api get-bucket-lifecycle-configuration --bucket "$BACKUP_BUCKET" --output json 2>/dev/null || echo '{"Rules":[]}')"
  python3 - "$actual" > "$tmp/retencion.json" <<'PY'
import json, sys
actual = json.loads(sys.argv[1])
reglas = [r for r in actual.get("Rules", []) if r.get("ID") != "retencion-respaldos"]
reglas.append({"ID": "retencion-respaldos", "Status": "Enabled", "Filter": {"Prefix": ""},
               "Transitions": [{"Days": 30, "StorageClass": "STANDARD_IA"}],
               "Expiration": {"Days": 180}})
print(json.dumps({"Rules": reglas}))
PY
  aws s3api put-bucket-lifecycle-configuration --bucket "$BACKUP_BUCKET" --lifecycle-configuration "file://$tmp/retencion.json" >/dev/null
}
r=0; retencion_ok && r=1
paso "respaldos: retencion (IA a 30 d, caducan a 180 d)" "$r" poner_retencion

v=0; versionado_ok "$BACKUP_BUCKET" && v=1
paso "respaldos: versionado" "$v" activar_versionado "$BACKUP_BUCKET"
c=0; ciclo_ok "$BACKUP_BUCKET" && c=1
paso "respaldos: versiones antiguas caducan (30 d)" "$c" poner_ciclo "$BACKUP_BUCKET"

lock_ok=0
if aws s3api get-object-lock-configuration --bucket "$BACKUP_BUCKET" --output json 2>/dev/null \
   | grep -q "\"Days\": $BACKUP_LOCK_DAYS"; then
  lock_ok=1
fi
poner_candado() {
  aws s3api put-object-lock-configuration --bucket "$BACKUP_BUCKET" --object-lock-configuration \
    "{\"ObjectLockEnabled\":\"Enabled\",\"Rule\":{\"DefaultRetention\":{\"Mode\":\"GOVERNANCE\",\"Days\":$BACKUP_LOCK_DAYS}}}"
}
paso "respaldos: candado de retencion ($BACKUP_LOCK_DAYS d, gobernanza)" "$lock_ok" poner_candado
fi

echo
if [[ $CHECK -eq 1 ]]; then
  echo "Nada aplicado. Corre sin --check para dejar los buckets como dice este archivo."
else
  echo "Listo. El candado solo alcanza a los respaldos subidos desde ahora; los anteriores siguen protegidos por el versionado y la politica del rol."
fi
