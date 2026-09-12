#!/usr/bin/env bash
# Reconcilia TODA la IAM de la plataforma en una sola corrida.
#
# Existe porque el modo anterior no escalaba: cada capacidad nueva (secretos, respaldos,
# escaneo de imagenes) traia su propio JSON y su propio "pidele a alguien que lo adjunte
# en la consola". Con tres es incomodo; con un servidor por vertical es la clase de deuda
# que hace que una capacidad quede a medias en un entorno y nadie se entere.
#
# Aqui las capacidades del rol de instancia y el usuario de deploy local se declaran
# como codigo. Correrlo de nuevo los deja como dice este archivo, aunque alguien los haya
# tocado a mano: eso es lo que hace que aprovisionar una cuenta nueva sea un comando y no
# una lista de clics.
#
# NO borra lo que no gestiona. Si el rol arrastra politicas de antes -adjuntas o inline con
# otro nombre-, las lista al final para que alguien decida: IAM concede por suma, asi que
# una politica vieja y olvidada sigue concediendo permisos aunque aqui no aparezca.
#
# El rol de la instancia NO puede ejecutar esto, y es correcto: si pudiera ampliarse sus
# propios permisos, comprometer la instancia seria comprometer la cuenta. Se corre con
# credenciales de administrador, y lo mas simple es el CloudShell de la consola AWS.
#
# Uso:
#   ops/aws/setup-iam.sh              # aplica
#   ops/aws/setup-iam.sh --check      # solo dice que cambiaria, no toca nada
set -euo pipefail

ROLE="${CF_INSTANCE_ROLE:-core-force-mail-ec2-role}"
DEPLOY_USER="${CF_DEPLOY_USER:-core-force-deploy-local}"
# Instancia sobre la que se permite abrir terminal por SSM. Acotar a una sola es el punto:
# una credencial filtrada no da acceso a lo que se aprovisione despues.
SSM_INSTANCE="${CF_SSM_INSTANCE:-i-0f87bab0b228634f5}"
REGION="${AWS_REGION:-us-east-1}"
NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"
BACKUP_BUCKET="${BACKUP_S3_BUCKET:-}"
MEDIA_BUCKET="${MEDIA_S3_BUCKET:-}"
CHECK=0
[[ "${1:-}" == "--check" ]] && CHECK=1

command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
ACC="$(aws sts get-caller-identity --query Account --output text)" || {
  echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }

# Los buckets llevan el id de cuenta en el nombre, asi que se derivan salvo override.
: "${BACKUP_BUCKET:=cf-backups-${ACC}}"
: "${MEDIA_BUCKET:=cf-media-${ACC}}"
: "${FLEET_BUCKET:=cf-oee-flota-${ACC}}"

echo "Cuenta $ACC / rol $ROLE / region $REGION"
[[ $CHECK -eq 1 ]] && echo "(modo --check: no se aplica nada)"
echo

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# Una politica por capacidad, no una gigante: asi se lee cual falta, se puede quitar una
# sin tocar las demas, y el nombre dice para que sirve.
emit() { cat > "$tmp/$1.json"; }

emit secretos <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"LeerSecretosDeLaPlataforma","Effect":"Allow",
  "Action":["secretsmanager:GetSecretValue","secretsmanager:DescribeSecret"],
  "Resource":"arn:aws:secretsmanager:${REGION}:${ACC}:secret:core-force/*"}]}
EOF

emit ecr-push <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"Autenticacion","Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},
 {"Sid":"PublicarImagenes","Effect":"Allow",
  "Action":["ecr:BatchCheckLayerAvailability","ecr:PutImage","ecr:InitiateLayerUpload",
            "ecr:UploadLayerPart","ecr:CompleteLayerUpload","ecr:BatchGetImage",
            "ecr:GetDownloadUrlForLayer","ecr:CreateRepository","ecr:DescribeRepositories"],
  "Resource":"arn:aws:ecr:${REGION}:${ACC}:repository/${NAMESPACE}/*"}]}
EOF

# Terminal por SSM: el transporte del despliegue cuando la IP de la PC cambia.
#
# El Security Group admite el 22 desde UNA sola IP y la de la PC es dinamica, asi que el
# despliegue se corta con un timeout que parece del servidor y arreglarlo exige subir al
# perfil raiz. Por SSM no hace falta abrir ningun puerto: el agente sale hacia AWS.
#
# Acotada a proposito hasta donde IAM permite: NO es ssm:*, es abrir sesion en ESA
# instancia y unicamente con el documento de SSH. El agente ya estaba conectado antes de
# esto (AmazonSSMManagedInstanceCore viene en el rol de la instancia); lo que faltaba era
# el permiso para usar el canal, no el canal.
#
# Cerrar o retomar una sesion se acota a las PROPIAS: sin esa condicion, cualquiera con
# esta credencial podria cortar la sesion de otro.
emit ssm-terminal <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"AbrirTerminalEnLaInstanciaDeProduccion","Effect":"Allow",
  "Action":"ssm:StartSession",
  "Resource":["arn:aws:ec2:${REGION}:${ACC}:instance/${SSM_INSTANCE}",
              "arn:aws:ssm:${REGION}::document/AWS-StartSSHSession"]},
 {"Sid":"CerrarSoloLasPropias","Effect":"Allow",
  "Action":["ssm:TerminateSession","ssm:ResumeSession"],
  "Resource":"arn:aws:ssm:${REGION}:${ACC}:session/\${aws:username}-*"}]}
EOF

# Escaneo: configurarlo es a nivel de REGISTRO, asi que esas acciones no admiten un
# recurso acotado. Leer los hallazgos si se acota al namespace propio.
emit ecr-scanning <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"ConfigurarEscaneo","Effect":"Allow",
  "Action":["ecr:GetRegistryScanningConfiguration","ecr:PutRegistryScanningConfiguration",
            "ecr:PutImageScanningConfiguration"],
  "Resource":"*"},
 {"Sid":"LeerHallazgos","Effect":"Allow",
  "Action":["ecr:DescribeImageScanFindings","ecr:DescribeImages"],
  "Resource":"arn:aws:ecr:${REGION}:${ACC}:repository/${NAMESPACE}/*"}]}
EOF

# Respaldos: escribir y leer, pero NO borrar. Un respaldo que el servidor puede borrar no
# protege del caso que mas importa, que es alguien -o algo- con acceso al servidor. La
# retencion la hace la regla de ciclo de vida del bucket, no la instancia.
emit respaldos <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"EscribirRespaldos","Effect":"Allow",
  "Action":["s3:PutObject","s3:GetObject","s3:ListBucket"],
  "Resource":["arn:aws:s3:::${BACKUP_BUCKET}","arn:aws:s3:::${BACKUP_BUCKET}/postgres/*"]}]}
EOF

emit medios <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"ArchivosDeUsuario","Effect":"Allow",
  "Action":["s3:PutObject","s3:GetObject","s3:DeleteObject","s3:ListBucket"],
  "Resource":["arn:aws:s3:::${MEDIA_BUCKET}","arn:aws:s3:::${MEDIA_BUCKET}/*"]}]}
EOF

# Flota OEE: el bucket propio de los agentes de borde. publish-release.sh sube el paquete
# a flota/* y oee-edge firma la URL de descarga con este mismo rol. Sin borrado, como los
# respaldos: una version publicada no la retira el servidor. Hasta 2026-09-05 este bucket
# solo lo cubria AmazonS3FullAccess, que es lo que impedia retirar esa politica ancha.
emit flota <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"PaquetesDeLaFlota","Effect":"Allow",
  "Action":["s3:PutObject","s3:GetObject","s3:ListBucket"],
  "Resource":["arn:aws:s3:::${FLEET_BUCKET}","arn:aws:s3:::${FLEET_BUCKET}/flota/*"]}]}
EOF

# Observacion: solo lectura del estado de la plataforma (instancias, base de datos,
# metricas, logs, costos). Es lo que necesita quien opera desde su PC sin poder
# tocar infraestructura ni datos.
emit observacion <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"ObservarPlataforma","Effect":"Allow",
  "Action":["ec2:Describe*","rds:Describe*","cloudwatch:Get*","cloudwatch:List*",
            "cloudwatch:Describe*","logs:Describe*","logs:Get*","logs:FilterLogEvents",
            "ce:GetCostAndUsage","ce:GetCostForecast","s3:ListAllMyBuckets"],
  "Resource":"*"}]}
EOF

# Sin permiso para LEER la IAM no se puede informar nada honesto: "no existe" y "no lo
# puedo ver" se leerian igual, y el informe diria que falta todo. Se comprueba primero.
probe="$(aws iam get-role-policy --role-name "$ROLE" --policy-name core-force-secretos 2>&1 || true)"
if grep -q 'AccessDenied' <<<"$probe"; then
  echo "FALLA: estas credenciales no pueden leer la IAM del rol $ROLE." >&2
  echo "       Este script se corre con un administrador de la cuenta; lo mas simple es" >&2
  echo "       el CloudShell de la consola AWS. El rol de la instancia no puede -ni debe-" >&2
  echo "       ampliarse sus propios permisos." >&2
  exit 2
fi

# Deja una politica inline como dice su archivo en $tmp, sobre un rol o un usuario.
reconcilia() {
  local ambito="$1" titular="$2" pol="$3"   # ambito: role | user
  local name="core-force-${pol}"
  local actual wanted current
  actual="$(aws iam "get-${ambito}-policy" "--${ambito}-name" "$titular" --policy-name "$name" \
              --query PolicyDocument --output json 2>/dev/null || true)"
  wanted="$(python3 -c 'import json,sys;print(json.dumps(json.load(open(sys.argv[1])),sort_keys=True))' "$tmp/$pol.json")"
  current="$(printf '%s' "${actual:-null}" | python3 -c 'import json,sys
try:
    d=json.load(sys.stdin)
    print("null" if d is None else json.dumps(d,sort_keys=True))
except Exception:
    print("null")')"

  if [[ "$current" == "$wanted" ]]; then
    printf '  %-28s al dia\n' "$name"
    return
  fi
  if [[ $CHECK -eq 1 ]]; then
    printf '  %-28s %s\n' "$name" "$([[ "$current" == "null" ]] && echo 'FALTA' || echo 'DIFIERE')"
    return
  fi
  aws iam "put-${ambito}-policy" "--${ambito}-name" "$titular" --policy-name "$name" \
      --policy-document "file://$tmp/$pol.json"
  printf '  %-28s aplicada\n' "$name"
}

for pol in secretos ecr-push ecr-scanning respaldos medios flota; do
  reconcilia role "$ROLE" "$pol"
done

# Lo que el rol tiene y este archivo no declara. No se toca: puede ser deliberado. Pero
# tiene que verse, porque concede permisos igual.
echo
otras="$(aws iam list-role-policies --role-name "$ROLE" --query 'PolicyNames[]' --output text 2>/dev/null \
          | tr '\t' '\n' | grep -v '^core-force-' | grep -v '^$' || true)"
adjuntas="$(aws iam list-attached-role-policies --role-name "$ROLE" \
             --query 'AttachedPolicies[].PolicyName' --output text 2>/dev/null | tr '\t' '\n' | grep -v '^$' || true)"
if [[ -n "$otras$adjuntas" ]]; then
  echo "El rol tiene ademas permisos que este archivo NO gestiona; revisalos:"
  [[ -n "$otras" ]]    && sed 's/^/  inline:   /' <<<"$otras"
  [[ -n "$adjuntas" ]] && sed 's/^/  adjunta:  /' <<<"$adjuntas"
  echo
fi

# --- Usuario para el deploy desde fuera del servidor --------------------------------
# scripts/deploy-ecr.sh con TRANSPORT=ecr compila en la PC del desarrollador y necesita
# credenciales locales. Este usuario solo publica imagenes al namespace propio y lee el
# estado de la plataforma: una llave filtrada de una laptop no puede tocar datos ni
# infraestructura. La llave de acceso se crea UNA sola vez (no se reconcilia aqui):
#   aws iam create-access-key --user-name $DEPLOY_USER
echo "Usuario de deploy $DEPLOY_USER"
if ! aws iam get-user --user-name "$DEPLOY_USER" >/dev/null 2>&1; then
  if [[ $CHECK -eq 1 ]]; then
    echo "  FALTA: se crearia"
  else
    aws iam create-user --user-name "$DEPLOY_USER" \
        --tags Key=proposito,Value=deploy-desde-pc-local >/dev/null
    echo "  creado"
  fi
fi
if aws iam get-user --user-name "$DEPLOY_USER" >/dev/null 2>&1; then
  reconcilia user "$DEPLOY_USER" ecr-push
  reconcilia user "$DEPLOY_USER" observacion
  reconcilia user "$DEPLOY_USER" ssm-terminal
fi
echo

if [[ $CHECK -eq 1 ]]; then
  echo "Nada aplicado. Corre sin --check para dejar la IAM como dice este archivo."
else
  echo "IAM reconciliada. Siguientes pasos:"
  echo "  ops/ecr/enable-scanning.sh                            # desde el servidor: escaneo de imagenes"
  echo "  aws iam create-access-key --user-name $DEPLOY_USER    # una vez, para 'aws configure' en la PC"
fi
