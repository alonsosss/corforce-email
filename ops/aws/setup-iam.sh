#!/usr/bin/env bash
# Reconcilia TODA la IAM de la plataforma en una sola corrida.
#
# Existe porque el modo anterior no escalaba: cada capacidad nueva (secretos, respaldos,
# escaneo de imagenes) traia su propio JSON y su propio "pidele a alguien que lo adjunte
# en la consola". Con tres es incomodo; con un servidor por vertical es la clase de deuda
# que hace que una capacidad quede a medias en un entorno y nadie se entere.
#
# Aqui las capacidades del rol de instancia, el usuario de deploy local y el usuario de envio
# por SES se declaran como codigo. Correrlo de nuevo los deja como dice este archivo, aunque
# alguien los haya tocado a mano: eso es lo que hace que aprovisionar una cuenta nueva sea un
# comando y no una lista de clics.
#
# NO borra lo que no gestiona. Si el rol arrastra politicas de antes -adjuntas o inline con
# otro nombre-, las lista al final para que alguien decida: IAM concede por suma, asi que
# una politica vieja y olvidada sigue concediendo permisos aunque aqui no aparezca.
#
# El rol de instancia solo existe si el servidor corre en EC2. En el servidor propio (Netcup)
# no hay rol: su parte se omite y el resto se reconcilia igual.
#
# El rol de la instancia NO puede ejecutar esto, y es correcto: si pudiera ampliarse sus
# propios permisos, comprometer la instancia seria comprometer la cuenta. Se corre con
# credenciales de administrador, y lo mas simple es el CloudShell de la consola AWS.
#
# Ninguna politica vive como JSON suelto en el repositorio: todas se renderizan aqui con la
# cuenta de quien ejecuta (STS), la region y el namespace de ECR.
#
# Uso:
#   ops/aws/setup-iam.sh                         # aplica
#   ops/aws/setup-iam.sh --check                 # solo dice que cambiaria, no toca nada
#   AWS_ACCOUNT_ID=<cuenta> ops/aws/setup-iam.sh --render <dir-vacio>
#                                                # solo escribe las politicas, sin tocar AWS
set -euo pipefail

ROLE="${CF_INSTANCE_ROLE:-core-force-mail-ec2-role}"
DEPLOY_USER="${CF_DEPLOY_USER:-core-force-mail-deploy-local}"
# Usuario cuyas claves usa transactional (SES_ACCESS_KEY_ID y SES_SECRET_ACCESS_KEY, en el
# almacen de secretos). Es propio y no el de deploy: una clave de envio filtrada no publica
# imagenes, y una de deploy no envia correo en nombre de las empresas.
SES_USER="${CF_SES_USER:-core-force-mail-ses}"
SES_SET_TRANSACTIONAL="${SES_CONFIG_SET_TRANSACTIONAL:-cfm-transactional}"
SES_SET_MARKETING="${SES_CONFIG_SET_MARKETING:-cfm-marketing}"
# Instancia sobre la que se permite abrir terminal por SSM. Acotar a una sola es el punto:
# una credencial filtrada no da acceso a lo que se aprovisione despues. Sin ella no se
# declara esa politica; el id lo imprime ops/aws/setup-github-deploy.sh.
SSM_INSTANCE="${CF_SSM_INSTANCE:-}"
REGION="${AWS_REGION:-us-east-1}"
NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"
BACKUP_BUCKET="${BACKUP_S3_BUCKET:-}"
MEDIA_BUCKET="${MEDIA_S3_BUCKET:-}"
CHECK=0; RENDER_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) CHECK=1 ;;
    --render) RENDER_DIR="${2:?--render necesita un directorio}"; shift ;;
    *) echo "FALLA: argumento desconocido: $1" >&2; exit 1 ;;
  esac
  shift
done

# La cuenta sale de la identidad que ejecuta. AWS_ACCOUNT_ID solo la sustituye al
# renderizar: al aplicar tiene que coincidir con la del llamador, o las politicas
# apuntarian a recursos de otra cuenta y no concederian nada.
ACC="${AWS_ACCOUNT_ID:-}"
if [[ -z "$RENDER_DIR" || -z "$ACC" ]]; then
  command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
  llamador="$(aws sts get-caller-identity --query Account --output text)" || {
    echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }
  if [[ -n "$ACC" && "$ACC" != "$llamador" ]]; then
    echo "FALLA: AWS_ACCOUNT_ID=$ACC no es la cuenta de estas credenciales ($llamador)" >&2
    exit 1
  fi
  ACC="$llamador"
fi

# Los buckets llevan el id de cuenta en el nombre, asi que se derivan salvo override.
: "${BACKUP_BUCKET:=cf-backups-${ACC}}"
: "${MEDIA_BUCKET:=cf-media-${ACC}}"

# Cada valor se interpola dentro de un ARN: un comodin o una comilla colados por una
# variable ampliarian el recurso o romperian la politica.
valida() { [[ "$2" =~ $3 ]] || { echo "FALLA: $1 no es valido: '$2'" >&2; exit 1; }; }
valida AWS_ACCOUNT_ID "$ACC" '^[0-9]{12}$'
valida AWS_REGION "$REGION" '^[a-z]{2}(-[a-z]+)+-[0-9]+$'
valida ECR_NAMESPACE "$NAMESPACE" '^[a-z0-9]+([._/-][a-z0-9]+)*$'
valida BACKUP_S3_BUCKET "$BACKUP_BUCKET" '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$'
valida MEDIA_S3_BUCKET "$MEDIA_BUCKET" '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$'
valida SES_CONFIG_SET_TRANSACTIONAL "$SES_SET_TRANSACTIONAL" '^[a-zA-Z0-9_-]{1,64}$'
valida SES_CONFIG_SET_MARKETING "$SES_SET_MARKETING" '^[a-zA-Z0-9_-]{1,64}$'
[[ -z "$SSM_INSTANCE" ]] || valida CF_SSM_INSTANCE "$SSM_INSTANCE" '^i-[0-9a-f]{8,17}$'

echo "Cuenta $ACC / rol $ROLE / region $REGION"
[[ $CHECK -eq 1 ]] && echo "(modo --check: no se aplica nada)"
echo

if [[ -n "$RENDER_DIR" ]]; then
  mkdir -p "$RENDER_DIR"
  [[ -z "$(ls -A "$RENDER_DIR")" ]] || { echo "FALLA: $RENDER_DIR no esta vacio" >&2; exit 1; }
  out="$RENDER_DIR"
else
  out="$(mktemp -d)"; trap 'rm -rf "$out"' EXIT
fi

# Una politica por capacidad, no una gigante: asi se lee cual falta, se puede quitar una
# sin tocar las demas, y el nombre dice para que sirve.
emit() { cat > "$out/$1.json"; }

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
if [[ -n "$SSM_INSTANCE" ]]; then
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
fi

# Escaneo: configurarlo es a nivel de REGISTRO, asi que esas acciones no admiten un
# recurso acotado. ops/ecr/enable-scanning.sh ademas lista los repositorios sin
# nombrarlos para informar, por eso DescribeRepositories va con ellas. Leer los hallazgos
# si se acota al namespace propio.
emit ecr-scanning <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"ConfigurarEscaneo","Effect":"Allow",
  "Action":["ecr:GetRegistryScanningConfiguration","ecr:PutRegistryScanningConfiguration",
            "ecr:PutImageScanningConfiguration","ecr:DescribeRepositories"],
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

# Retencion de imagenes: ops/ecr/create-repos.sh aplica ops/ecr/lifecycle-policy.json a
# cada repositorio. Un repositorio creado sin este permiso se queda sin ella y las imagenes
# se acumulan sin limite, lo que no da sintoma y solo aparece en la factura.
emit ecr-retencion <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"AdministrarRetencionDeImagenes","Effect":"Allow",
  "Action":["ecr:GetLifecyclePolicy","ecr:PutLifecyclePolicy"],
  "Resource":"arn:aws:ecr:${REGION}:${ACC}:repository/${NAMESPACE}/*"}]}
EOF

# Observacion: solo lectura del estado de la plataforma (instancias, base de datos,
# metricas, logs, costos y la salida por SES: identidades, conjuntos, topic y suscripcion,
# que es lo que comprueba ops/aws/verificar-ses.sh). Es lo que necesita quien opera desde
# su PC sin poder tocar infraestructura ni datos.
emit observacion <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"ObservarPlataforma","Effect":"Allow",
  "Action":["ec2:Describe*","rds:Describe*","cloudwatch:Get*","cloudwatch:List*",
            "cloudwatch:Describe*","logs:Describe*","logs:Get*","logs:FilterLogEvents",
            "ce:GetCostAndUsage","ce:GetCostForecast","s3:ListAllMyBuckets",
            "ses:GetAccount","ses:ListEmailIdentities","ses:GetEmailIdentity",
            "ses:ListConfigurationSets","ses:GetConfigurationSet",
            "ses:GetConfigurationSetEventDestinations",
            "sns:ListTopics","sns:GetTopicAttributes","sns:ListSubscriptionsByTopic",
            "cloudformation:DescribeStacks"],
  "Resource":"*"}]}
EOF

# Envio por SES: transactional solo llama a SendEmail (sesv2). La identidad va con comodin
# porque cada empresa verifica su propio dominio en la cuenta y SES solo deja enviar desde
# una identidad verificada. El conjunto no: lo que sale por otro conjunto sale sin los
# eventos de la plataforma, y sus rebotes y quejas no llegan a suppression. Ni SendRawEmail
# ni plantillas de SES: el servicio no los usa.
emit ses-envio <<EOF
{"Version":"2012-10-17","Statement":[
 {"Sid":"EnviarPorLosConjuntosDeLaPlataforma","Effect":"Allow",
  "Action":"ses:SendEmail",
  "Resource":["arn:aws:ses:${REGION}:${ACC}:identity/*",
              "arn:aws:ses:${REGION}:${ACC}:configuration-set/${SES_SET_TRANSACTIONAL}",
              "arn:aws:ses:${REGION}:${ACC}:configuration-set/${SES_SET_MARKETING}"]}]}
EOF

if [[ -n "$RENDER_DIR" ]]; then
  for f in "$out"/*.json; do
    python3 -m json.tool "$f" >/dev/null || { echo "FALLA: $f no es JSON valido" >&2; exit 1; }
  done
  echo "Politicas renderizadas en $RENDER_DIR; no se aplico nada:"
  ls -1 "$out" | sed 's/^/  /'
  exit 0
fi

# Sin permiso para LEER la IAM no se puede informar nada honesto: "no existe" y "no lo
# puedo ver" se leerian igual, y el informe diria que falta todo. Se comprueba primero.
probe="$(aws iam get-role-policy --role-name "$ROLE" --policy-name core-force-mail-ecr-push 2>&1 || true)"
if grep -q 'AccessDenied' <<<"$probe"; then
  echo "FALLA: estas credenciales no pueden leer la IAM del rol $ROLE." >&2
  echo "       Este script se corre con un administrador de la cuenta; lo mas simple es" >&2
  echo "       el CloudShell de la consola AWS. El rol de la instancia no puede -ni debe-" >&2
  echo "       ampliarse sus propios permisos." >&2
  exit 2
fi

# Deja una politica inline como dice su archivo en $out, sobre un rol o un usuario.
reconcilia() {
  local ambito="$1" titular="$2" pol="$3"   # ambito: role | user
  local name="core-force-mail-${pol}"
  local actual wanted current
  actual="$(aws iam "get-${ambito}-policy" "--${ambito}-name" "$titular" --policy-name "$name" \
              --query PolicyDocument --output json 2>/dev/null || true)"
  wanted="$(python3 -c 'import json,sys;print(json.dumps(json.load(open(sys.argv[1])),sort_keys=True))' "$out/$pol.json")"
  current="$(printf '%s' "${actual:-null}" | python3 -c 'import json,sys
try:
    d=json.load(sys.stdin)
    print("null" if d is None else json.dumps(d,sort_keys=True))
except Exception:
    print("null")')"

  if [[ "$current" == "$wanted" ]]; then
    printf '  %-36s al dia\n' "$name"
    return
  fi
  if [[ $CHECK -eq 1 ]]; then
    printf '  %-36s %s\n' "$name" "$([[ "$current" == "null" ]] && echo 'FALTA' || echo 'DIFIERE')"
    return
  fi
  aws iam "put-${ambito}-policy" "--${ambito}-name" "$titular" --policy-name "$name" \
      --policy-document "file://$out/$pol.json"
  printf '  %-36s aplicada\n' "$name"
}

# Quita una politica gestionada que en esta corrida no corresponde.
retira() {
  local ambito="$1" titular="$2" name="core-force-mail-$3"
  if ! aws iam "get-${ambito}-policy" "--${ambito}-name" "$titular" --policy-name "$name" >/dev/null 2>&1; then
    printf '  %-36s no concedida\n' "$name"
    return
  fi
  if [[ $CHECK -eq 1 ]]; then
    printf '  %-36s SOBRA\n' "$name"
    return
  fi
  aws iam "delete-${ambito}-policy" "--${ambito}-name" "$titular" --policy-name "$name"
  printf '  %-36s retirada\n' "$name"
}

# "No existe" es un estado valido (servidor fuera de EC2); "no lo puedo ver" ya se descarto arriba.
HAS_ROLE=1
if ! rol="$(aws iam get-role --role-name "$ROLE" 2>&1)"; then
  grep -q 'NoSuchEntity' <<<"$rol" || { echo "FALLA: no se pudo leer el rol $ROLE: $rol" >&2; exit 2; }
  HAS_ROLE=0
fi

if [[ $HAS_ROLE -eq 1 ]]; then
echo "Rol de instancia $ROLE"
for pol in ecr-push ecr-scanning respaldos medios; do
  reconcilia role "$ROLE" "$pol"
done
# Los secretos de la plataforma ya no viven en Secrets Manager (docs/adr/0008): estas dos
# politicas quedan obsoletas. Se retiran explicitamente, no solo se dejan de declarar, porque el
# informe de "politicas que este archivo no gestiona" (mas abajo) solo lista las que NO llevan el
# prefijo core-force-mail-, y estas dos sí lo llevan: sin este paso, una cuenta que ya las tuviera
# concedidas conservaria acceso de lectura o escritura al almacen de AWS para siempre, sin que
# ninguna corrida futura de este script lo mencionara.
retira role "$ROLE" secretos
retira role "$ROLE" secretos-escritura

# Lo que el rol tiene y este archivo no declara. No se toca: puede ser deliberado. Pero
# tiene que verse, porque concede permisos igual.
echo
otras="$(aws iam list-role-policies --role-name "$ROLE" --query 'PolicyNames[]' --output text 2>/dev/null \
          | tr '\t' '\n' | grep -v '^core-force-mail-' | grep -v '^$' || true)"
adjuntas="$(aws iam list-attached-role-policies --role-name "$ROLE" \
             --query 'AttachedPolicies[].PolicyName' --output text 2>/dev/null | tr '\t' '\n' | grep -v '^$' || true)"
if [[ -n "$otras$adjuntas" ]]; then
  echo "El rol tiene ademas permisos que este archivo NO gestiona; revisalos:"
  [[ -n "$otras" ]]    && sed 's/^/  inline:   /' <<<"$otras"
  [[ -n "$adjuntas" ]] && sed 's/^/  adjunta:  /' <<<"$adjuntas"
  echo
fi
else
  echo "Rol de instancia $ROLE: no existe (servidor fuera de EC2); se omite."
  echo
fi

# --- Usuario para el deploy desde fuera del servidor --------------------------------
# scripts/deploy-ecr.sh con TRANSPORT=ecr compila en la PC del desarrollador y necesita
# credenciales locales. Este usuario solo publica imagenes al namespace propio (y fija su
# retencion) y lee el estado de la plataforma: una llave filtrada de una laptop no puede
# tocar datos ni infraestructura. La llave de acceso se crea UNA sola vez (no se reconcilia aqui):
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
  reconcilia user "$DEPLOY_USER" ecr-retencion
  reconcilia user "$DEPLOY_USER" observacion
  if [[ -n "$SSM_INSTANCE" ]]; then
    reconcilia user "$DEPLOY_USER" ssm-terminal
  else
    printf '  %-36s sin CF_SSM_INSTANCE: no se declara\n' core-force-mail-ssm-terminal
  fi
fi
echo

# --- Usuario de envio por SES ---------------------------------------------------------
# Sus claves las crea quien administra la cuenta, UNA vez, y van directo al almacen de
# secretos (ops/security/secrets/add-secret.sh), nunca a un .env ni a un chat:
#   aws iam create-access-key --user-name $SES_USER
echo "Usuario de envio $SES_USER (conjuntos $SES_SET_TRANSACTIONAL y $SES_SET_MARKETING)"
if ! aws iam get-user --user-name "$SES_USER" >/dev/null 2>&1; then
  if [[ $CHECK -eq 1 ]]; then
    echo "  FALTA: se crearia"
  else
    aws iam create-user --user-name "$SES_USER" \
        --tags Key=proposito,Value=envio-ses-transactional >/dev/null
    echo "  creado"
  fi
fi
if aws iam get-user --user-name "$SES_USER" >/dev/null 2>&1; then
  reconcilia user "$SES_USER" ses-envio
fi
echo

if [[ $CHECK -eq 1 ]]; then
  echo "Nada aplicado. Corre sin --check para dejar la IAM como dice este archivo."
else
  echo "IAM reconciliada. Siguientes pasos:"
  echo "  ops/ecr/enable-scanning.sh                            # desde el servidor: escaneo de imagenes"
  echo "  aws iam create-access-key --user-name $DEPLOY_USER    # una vez, para 'aws configure' en la PC"
  echo "  aws iam create-access-key --user-name $SES_USER       # una vez, al almacen de secretos de transactional"
fi
