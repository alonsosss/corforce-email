#!/usr/bin/env bash
# Guardarrail: la gracia de una rotacion DKIM programada (domain-service) tiene que durar mas que
# la cola de Postfix mas un TTL habitual de un TXT.
#
# Por que: Rspamd firma al aceptar el mensaje y Postfix lo reintenta hasta maximal_queue_lifetime.
# Un mensaje firmado con la clave anterior justo antes de retirarla puede llegar al receptor al
# final de la cola; si el TXT de esa clave ya no esta (el cliente lo quita al vencer la gracia, y
# su cache dura como mucho un TTL), la firma no se puede comprobar. La gracia se cuenta desde la
# ultima vez que la clave pudo firmar, asi que basta con que el minimo cubra la cola y el TTL.
#
# Compara maximal_queue_lifetime de deploy/mail/postfix/conf/main.cf.base (unidades de Postfix s, m, h,
# d, w; sin unidad, dias) con minDKIMRotationGrace de services/domain-service/main.go,
# DefaultDKIMRotationGrace de services/domain-service/internal/app/usecase.go y
# MAIL_DKIM_ROTATION_GRACE de .env.example. Regla: cola + margen <= minimo <= defecto, y el valor
# de .env.example no baja del minimo. Las constantes de Go se escriben como N * time.Hour.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAIN_CF="$ROOT/deploy/mail/postfix/conf/main.cf.base"
MAIN_GO="$ROOT/services/domain-service/main.go"
USECASE_GO="$ROOT/services/domain-service/internal/app/usecase.go"
ENV_EXAMPLE="$ROOT/.env.example"
# TTL mas largo habitual de un TXT en la zona de un cliente.
TTL_MARGIN_HOURS=24
FAIL=0

falla() { echo "  FALLA: $*"; FAIL=1; }

# postfix_hours <valor>: horas enteras de un tiempo de Postfix, redondeando hacia arriba.
postfix_hours() {
  local v="$1" n unit
  if [[ ! "$v" =~ ^([0-9]+)([smhdw]?)$ ]]; then
    return 1
  fi
  n="${BASH_REMATCH[1]}"
  unit="${BASH_REMATCH[2]:-d}"
  case "$unit" in
    s) echo $(((n + 3599) / 3600)) ;;
    m) echo $(((n + 59) / 60)) ;;
    h) echo "$n" ;;
    d) echo $((n * 24)) ;;
    w) echo $((n * 168)) ;;
  esac
}

# go_hours <fichero> <constante>: N de "constante = N * time.Hour".
go_hours() {
  sed -nE "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*\*[[:space:]]*time\.Hour.*/\1/p" "$1" | head -1
}

# Postfix aplica la ultima definicion del parametro.
QUEUE_RAW=$(sed -nE 's/^[[:space:]]*maximal_queue_lifetime[[:space:]]*=[[:space:]]*([^[:space:]#]+).*/\1/p' "$MAIN_CF" | tail -1)
if [[ -z "$QUEUE_RAW" ]]; then
  # Sin el parametro, Postfix usa su valor por defecto: 5d.
  QUEUE_RAW=5d
fi
QUEUE_H=$(postfix_hours "$QUEUE_RAW") || { falla "maximal_queue_lifetime = $QUEUE_RAW en $MAIN_CF no es un tiempo de Postfix"; QUEUE_H=0; }

MIN_H=$(go_hours "$MAIN_GO" minDKIMRotationGrace)
DEF_H=$(go_hours "$USECASE_GO" DefaultDKIMRotationGrace)
[[ -n "$MIN_H" ]] || falla "no se lee minDKIMRotationGrace = N * time.Hour en $MAIN_GO"
[[ -n "$DEF_H" ]] || falla "no se lee DefaultDKIMRotationGrace = N * time.Hour en $USECASE_GO"

ENV_RAW=$(sed -nE 's/^MAIL_DKIM_ROTATION_GRACE=(.*)$/\1/p' "$ENV_EXAMPLE" | tail -1)
ENV_H=""
if [[ "$ENV_RAW" =~ ^([0-9]+)h$ ]]; then
  ENV_H="${BASH_REMATCH[1]}"
else
  falla "MAIL_DKIM_ROTATION_GRACE=$ENV_RAW en .env.example: se escribe en horas (p. ej. 168h)"
fi

if [[ -n "$MIN_H" && -n "$DEF_H" && $QUEUE_H -gt 0 ]]; then
  NEED=$((QUEUE_H + TTL_MARGIN_HOURS))
  if ((MIN_H < NEED)); then
    falla "minDKIMRotationGrace (${MIN_H}h) < maximal_queue_lifetime (${QUEUE_RAW} = ${QUEUE_H}h) + ${TTL_MARGIN_HOURS}h de TTL: correo aun en cola llegaria sin el TXT de su clave. Sube el minimo en $MAIN_GO"
  fi
  if ((DEF_H < MIN_H)); then
    falla "DefaultDKIMRotationGrace (${DEF_H}h) por debajo del minimo (${MIN_H}h)"
  fi
  if [[ -n "$ENV_H" ]] && ((ENV_H < MIN_H)); then
    falla "MAIL_DKIM_ROTATION_GRACE=${ENV_RAW} en .env.example por debajo del minimo (${MIN_H}h): domain-service no arrancaria"
  fi
fi

if [[ $FAIL -eq 0 ]]; then
  echo "  OK: gracia DKIM minima ${MIN_H}h, por defecto ${DEF_H}h, .env.example ${ENV_RAW}; cola de Postfix ${QUEUE_RAW} + ${TTL_MARGIN_HOURS}h."
fi
exit $FAIL
