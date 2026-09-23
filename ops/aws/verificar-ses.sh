#!/usr/bin/env bash
# Comprueba, sin cambiar nada, que la salida por Amazon SES de un ambiente esta como la
# necesita transactional: la cuenta (acceso de produccion, envio habilitado, supresion de
# rebotes y quejas), la pila de ops/aws/setup-ses.sh, los dos conjuntos con su destino de
# eventos en el topic de la pila, la suscripcion HTTPS confirmada y cada identidad de envio
# verificada con DKIM, MAIL FROM propio y el conjunto transaccional por defecto.
#
# Existe porque cada pieza falla en silencio: un conjunto sin destino envia igual y sus
# rebotes no llegan a suppression; una suscripcion sin confirmar descarta los eventos; una
# identidad con otro conjunto por defecto manda sus eventos a un topic que nadie escucha.
#
# Basta la politica de observacion de ops/aws/setup-iam.sh.
#
# Uso:
#   ops/aws/verificar-ses.sh <dev|staging|prod> [identidad ...]
#   ops/aws/verificar-ses.sh prod avisos.ejemplo.com
#
# Sale con 1 si algo falla. El modo de pruebas de SES (sin acceso de produccion) es un
# aviso, no un fallo: todo lo demas se puede dejar listo antes de que AWS lo apruebe.
set -euo pipefail

ENVIRONMENT="${1:-}"
case "$ENVIRONMENT" in dev|staging|prod) ;; *)
  echo "Uso: $0 <dev|staging|prod> [identidad ...]" >&2; exit 2 ;;
esac
shift
IDENTITIES=("$@")
for ident in "${IDENTITIES[@]}"; do
  [[ "$ident" =~ ^[A-Za-z0-9.@_+-]+$ ]] || { echo "FALLA: identidad no valida: $ident" >&2; exit 2; }
done

command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "FALLA: falta python3" >&2; exit 1; }
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-$(aws configure get region 2>/dev/null || true)}}"
[[ -n "$REGION" ]] || { echo "FALLA: sin region (AWS_REGION)" >&2; exit 1; }
ACC="$(aws sts get-caller-identity --query Account --output text)" || {
  echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }
STACK="cfm-${ENVIRONMENT}-ses-mail"

FALLAS=0
ok()    { printf '  ok     %s\n' "$*"; }
aviso() { printf '  AVISO  %s\n' "$*"; }
falla() { printf '  FALLA  %s\n' "$*"; FALLAS=$((FALLAS + 1)); }

# Una llamada denegada no es "no existe": se informa como tal para no mandar a nadie a
# crear lo que ya esta.
llama() {
  local salida
  if ! salida="$(aws "$@" --region "$REGION" --output json 2>&1)"; then
    if grep -qE 'AccessDenied|AuthorizationError|not authorized' <<<"$salida"; then
      echo "DENEGADO"
    else
      echo "ERROR: $(head -c 300 <<<"$salida")"
    fi
    return 1
  fi
  printf '%s' "$salida"
}

# Lee un campo de un JSON por una ruta de claves separadas por puntos; vacio si falta.
campo() {
  python3 -c '
import json, sys
d = json.loads(sys.stdin.read())
for k in sys.argv[1].split("."):
    d = d.get(k) if isinstance(d, dict) else None
print("" if d is None else (json.dumps(d) if isinstance(d, (list, dict)) else d))' "$1"
}

echo "Cuenta $ACC / region $REGION / pila $STACK"
echo

echo "Cuenta de SES"
if cuenta="$(llama sesv2 get-account)"; then
  [[ "$(campo SendingEnabled <<<"$cuenta")" == "True" ]] && ok "envio habilitado" || falla "el envio esta deshabilitado en la cuenta"
  if [[ "$(campo ProductionAccessEnabled <<<"$cuenta")" == "True" ]]; then
    ok "acceso de produccion"
  else
    aviso "modo de pruebas: solo llega a direcciones verificadas y al simulador de SES"
  fi
  ok "cuota: $(campo SendQuota.Max24HourSend <<<"$cuenta") por dia, $(campo SendQuota.MaxSendRate <<<"$cuenta") por segundo"
  razones="$(campo SuppressionAttributes.SuppressedReasons <<<"$cuenta")"
  if [[ "$razones" == *BOUNCE* && "$razones" == *COMPLAINT* ]]; then
    ok "supresion de cuenta para rebotes y quejas"
  else
    falla "la supresion de la cuenta no cubre rebotes y quejas: ${razones:-ninguna}"
  fi
else
  falla "no se pudo leer la cuenta: $cuenta"
fi
echo

echo "Pila $STACK"
TOPIC=""; SET_T=""; SET_M=""
if pila="$(llama cloudformation describe-stacks --stack-name "$STACK")"; then
  salidas="$(python3 -c '
import json, sys
s = json.load(sys.stdin)["Stacks"][0]
print(s["StackStatus"])
o = {x["OutputKey"]: x["OutputValue"] for x in s.get("Outputs", [])}
for k in ("EventsTopicArn", "TransactionalConfigurationSet", "MarketingConfigurationSet"):
    print(o.get(k, ""))' <<<"$pila")"
  { read -r estado; read -r TOPIC; read -r SET_T; read -r SET_M; } <<<"$salidas"
  [[ "$estado" == *_COMPLETE && "$estado" != *ROLLBACK* ]] && ok "estado $estado" || falla "estado $estado"
elif [[ "$pila" == DENEGADO ]]; then
  falla "sin permiso para leer la pila (politica observacion de ops/aws/setup-iam.sh)"
else
  falla "sin pila (se crea con ops/aws/setup-ses.sh $ENVIRONMENT): $pila"
fi
echo

if [[ -n "$TOPIC" ]]; then
  for par in "transaccional:$SET_T" "marketing:$SET_M"; do
    clase="${par%%:*}"; nombre="${par#*:}"
    echo "Conjunto $clase $nombre"
    if ! destinos="$(llama sesv2 get-configuration-set-event-destinations --configuration-set-name "$nombre")"; then
      falla "no se pudo leer: $destinos"; echo; continue
    fi
    resultado="$(python3 -c '
import json, sys
topic = sys.argv[1]
need = {"BOUNCE", "COMPLAINT", "REJECT", "DELIVERY", "RENDERING_FAILURE"}
for d in json.load(sys.stdin).get("EventDestinations", []):
    if d.get("Enabled") and d.get("SnsDestination", {}).get("TopicArn") == topic:
        missing = need - set(d.get("MatchingEventTypes", []))
        print("falta:" + ",".join(sorted(missing)) if missing else "ok")
        break
else:
    print("sin-destino")' "$TOPIC" <<<"$destinos")"
    case "$resultado" in
      ok) ok "publica rebotes, quejas, rechazos y entregas en el topic de la pila" ;;
      sin-destino) falla "ningun destino activo publica en $TOPIC" ;;
      *) falla "el destino no publica ${resultado#falta:}" ;;
    esac
    echo
  done

  echo "Topic $TOPIC"
  if subs="$(llama sns list-subscriptions-by-topic --topic-arn "$TOPIC")"; then
    resultado="$(python3 -c '
import json, sys
subs = [s for s in json.load(sys.stdin).get("Subscriptions", []) if s.get("Protocol") == "https"]
for s in subs:
    arn = s.get("SubscriptionArn", "")
    estado = "pendiente" if arn in ("PendingConfirmation", "Deleted") or not arn.startswith("arn:") else "confirmada"
    print(estado + " " + s.get("Endpoint", ""))
if not subs:
    print("ninguna")' <<<"$subs")"
    while read -r estado url; do
      case "$estado" in
        confirmada)
          [[ "$url" == */api/v1/public/transactional/ses-events ]] && ok "suscripcion confirmada: $url" \
            || aviso "suscripcion confirmada a una URL que no es la de transactional: $url" ;;
        pendiente) falla "suscripcion sin confirmar: $url (transactional la confirma si SES_EVENTS_TOPIC_ARN es este topic)" ;;
        *) falla "sin suscripcion HTTPS" ;;
      esac
    done <<<"$resultado"
  else
    falla "no se pudieron leer las suscripciones: $subs"
  fi
  echo
fi

for ident in "${IDENTITIES[@]}"; do
  echo "Identidad $ident"
  if ! id_json="$(llama sesv2 get-email-identity --email-identity "$ident")"; then
    falla "no se pudo leer: $id_json"; echo; continue
  fi
  [[ "$(campo VerifiedForSendingStatus <<<"$id_json")" == "True" ]] && ok "verificada para enviar" || falla "no verificada para enviar"
  dkim="$(campo DkimAttributes.Status <<<"$id_json")"
  [[ "$dkim" == "SUCCESS" ]] && ok "DKIM $(campo DkimAttributes.CurrentSigningKeyLength <<<"$id_json")" || falla "DKIM en estado ${dkim:-desconocido}"
  mailfrom="$(campo MailFromAttributes.MailFromDomain <<<"$id_json")"
  if [[ -z "$mailfrom" ]]; then
    aviso "sin MAIL FROM propio: DMARC solo se alinea por DKIM"
  elif [[ "$(campo MailFromAttributes.MailFromDomainStatus <<<"$id_json")" == "SUCCESS" ]]; then
    ok "MAIL FROM $mailfrom"
  else
    falla "MAIL FROM $mailfrom en estado $(campo MailFromAttributes.MailFromDomainStatus <<<"$id_json")"
  fi
  defecto="$(campo ConfigurationSetName <<<"$id_json")"
  if [[ -n "$SET_T" && "$defecto" == "$SET_T" ]]; then
    ok "conjunto por defecto $defecto"
  else
    falla "conjunto por defecto '${defecto:-ninguno}', se esperaba '${SET_T:-el transaccional de la pila}' (SES_DEFAULT_SET_IDENTITIES en setup-ses.sh)"
  fi
  echo
done

if [[ -n "$TOPIC" ]]; then
  echo "Valores de transactional para este ambiente:"
  echo "  SES_REGION=$REGION"
  echo "  SES_EVENTS_TOPIC_ARN=$TOPIC"
  echo "  SES_CONFIG_SET_TRANSACTIONAL=$SET_T"
  echo "  SES_CONFIG_SET_MARKETING=$SET_M"
  echo
fi

if [[ $FALLAS -gt 0 ]]; then
  echo "$FALLAS comprobaciones fallidas."
  exit 1
fi
echo "Todo en orden."
