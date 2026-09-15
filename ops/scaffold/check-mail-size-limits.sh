#!/usr/bin/env bash
# Guardarrail: todo mensaje que acepta Postfix lo analiza Rspamd y puede llegar entero a la
# cuarentena de mail-security.
#
# Por que: Postfix pasa cada mensaje por el milter de Rspamd (smtpd_milters y non_smtpd_milters).
# Uno mayor que max_message no se analiza: el proxy del milter contesta tempfail y Postfix, con
# milter_default_action = tempfail, lo rechaza temporalmente hasta devolverlo al remitente. Rspamd
# manda a /pipe de mail-security el mensaje entero con sus metadatos en un multipart, con el cuerpo
# acotado por MAIL_QUARANTINE_MAX_BODY_MB, cuyo techo es maxPipeMaxBodyMiB.
#
# Compara message_size_limit de deploy/mail/postfix/conf/main.cf (bytes; sin el parametro, el
# defecto de Postfix, 10240000), max_message de deploy/mail/rspamd/local.d/options.inc (bytes y
# obligatorio: el defecto cambia con la version de Rspamd; ningun otro fichero de
# deploy/mail/rspamd lo fija, porque lo pisaria), maxPipeMaxBodyMiB y defaultPipeMaxBodyMiB de
# services/mail-security/main.go y MAIL_QUARANTINE_MAX_BODY_MB de .env.example. MARGEN (1 MiB)
# cubre las cabeceras que reconstruye el milter y el envoltorio de /pipe con sus metadatos:
#   max_message >= message_size_limit + MARGEN
#   message_size_limit + MARGEN <= techo de /pipe <= max_message + MARGEN
#   1 <= defecto de /pipe y valor de .env.example <= techo
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAIN_CF="$ROOT/deploy/mail/postfix/conf/main.cf"
RSPAMD_DIR="$ROOT/deploy/mail/rspamd"
OPTIONS_INC="$RSPAMD_DIR/local.d/options.inc"
MAIN_GO="$ROOT/services/mail-security/main.go"
ENV_EXAMPLE="$ROOT/.env.example"
MIB=1048576
MARGIN=$MIB
FAIL=0

falla() { echo "  FALLA: $*"; FAIL=1; }

# bytes <valor>: el valor como entero decimal (sin ceros a la izquierda que bash lea en octal).
bytes() { [[ "$1" =~ ^[0-9]+$ ]] && echo $((10#$1)); }

# go_int <fichero> <constante>: N de "constante = N".
go_int() {
  sed -nE "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*(\/\/.*)?$/\1/p" "$1" | head -1
}

# Postfix aplica la ultima definicion del parametro.
P_RAW=$(sed -nE 's/^[[:space:]]*message_size_limit[[:space:]]*=[[:space:]]*([^[:space:]#]+).*/\1/p' "$MAIN_CF" | tail -1)
P_RAW=${P_RAW:-10240000}
P=$(bytes "$P_RAW") || P=""
if [[ -z "$P" ]]; then
  falla "message_size_limit = $P_RAW en $MAIN_CF no es un numero de bytes"
elif ((P == 0)); then
  falla "message_size_limit = 0 en $MAIN_CF es sin limite: ningun max_message de Rspamd lo cubre"
  P=""
fi

R_RAW=$(sed -nE 's/^[[:space:]]*max_message[[:space:]]*=[[:space:]]*([^;[:space:]#]+).*/\1/p' "$OPTIONS_INC" | tail -1)
R=""
if [[ -z "$R_RAW" ]]; then
  falla "falta max_message en $OPTIONS_INC: sin el Rspamd aplica su defecto (50 MiB en rspamd 4.1.4)"
else
  R=$(bytes "$R_RAW") || { R=""; falla "max_message = $R_RAW en $OPTIONS_INC: se escribe en bytes (p. ej. 105906176)"; }
fi
OTROS=$(grep -rlE '^[[:space:]]*max_message[[:space:]]*=' "$RSPAMD_DIR" | grep -vxF "$OPTIONS_INC")
if [[ -n "$OTROS" ]]; then
  falla "max_message solo se fija en $OPTIONS_INC; tambien aparece en: $(echo $OTROS)"
fi

Q_MIB=$(go_int "$MAIN_GO" maxPipeMaxBodyMiB)
D_MIB=$(go_int "$MAIN_GO" defaultPipeMaxBodyMiB)
[[ -n "$Q_MIB" ]] || falla "no se lee maxPipeMaxBodyMiB = N en $MAIN_GO"
[[ -n "$D_MIB" ]] || falla "no se lee defaultPipeMaxBodyMiB = N en $MAIN_GO"

E_RAW=$(sed -nE 's/^MAIL_QUARANTINE_MAX_BODY_MB=(.*)$/\1/p' "$ENV_EXAMPLE" | tail -1)
E_MIB=$(bytes "$E_RAW") || { E_MIB=""; falla "MAIL_QUARANTINE_MAX_BODY_MB=$E_RAW en .env.example: se escribe en MiB enteros"; }

if [[ -n "$P" && -n "$R" ]] && ((R < P + MARGIN)); then
  falla "max_message de Rspamd ($R) < message_size_limit de Postfix ($P) + $MARGIN: un mensaje que Postfix acepta no se analizaria y quedaria en tempfail. Sube max_message en $OPTIONS_INC"
fi
if [[ -n "$Q_MIB" ]]; then
  Q=$((Q_MIB * MIB))
  if [[ -n "$P" ]] && ((Q < P + MARGIN)); then
    falla "maxPipeMaxBodyMiB (${Q_MIB} MiB) < message_size_limit de Postfix ($P) + $MARGIN: la cuarentena no podria guardar un mensaje que Postfix acepta"
  fi
  if [[ -n "$R" ]] && ((Q > R + MARGIN)); then
    falla "maxPipeMaxBodyMiB (${Q_MIB} MiB) > max_message de Rspamd ($R) + $MARGIN: /pipe admitiria cuerpos que Rspamd nunca manda"
  fi
  for par in "defaultPipeMaxBodyMiB:${D_MIB}" "MAIL_QUARANTINE_MAX_BODY_MB de .env.example:${E_MIB}"; do
    nombre="${par%:*}" valor="${par##*:}"
    if [[ -n "$valor" ]] && ((valor < 1 || valor > Q_MIB)); then
      falla "$nombre = $valor fuera de 1..${Q_MIB} MiB: mail-security no arrancaria"
    fi
  done
fi

if [[ $FAIL -eq 0 ]]; then
  echo "  OK: Postfix $P bytes, Rspamd max_message $R, /pipe de 1 a ${Q_MIB} MiB (defecto ${D_MIB}, .env.example ${E_MIB}), margen $MARGIN."
fi
exit $FAIL
