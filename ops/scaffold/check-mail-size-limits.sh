#!/usr/bin/env bash
# Guardarrail: todo mensaje que acepta Postfix lo analiza Rspamd, pasa ENTERO por ClamAV y puede
# llegar entero a la cuarentena de mail-security.
#
# Por que: Postfix pasa cada mensaje por el milter de Rspamd (smtpd_milters y non_smtpd_milters).
# Uno mayor que max_message no se analiza: el proxy del milter contesta tempfail y Postfix, con
# milter_default_action = tempfail, lo rechaza temporalmente hasta devolverlo al remitente. Rspamd
# manda a /pipe de mail-security el mensaje entero con sus metadatos en un multipart, con el cuerpo
# acotado por MAIL_QUARANTINE_MAX_BODY_MB, cuyo techo es maxPipeMaxBodyMiB.
#
# El antivirus falla callado y en dos sitios, y por eso va atado aqui. Rspamd manda a clamd el
# mensaje ENTERO (antivirus.conf, scan_mime_parts = false): uno mayor que max_size NO se analiza,
# no deja ningun simbolo y se entrega como si estuviera limpio (common.lua solo lo anota en info).
# Y clamd, con un flujo mayor que StreamMaxLength, responde ERROR o corta la conexion, pero con un
# fichero mayor que MaxFileSize o un total mayor que MaxScanSize lo analiza TRUNCADO y responde
# "stream: OK": un veredicto limpio falso. AlertExceedsMax convierte eso en la deteccion
# Heuristics.Limits.Exceeded, clamav.lua la traduce (como cualquier error o plazo agotado) al
# simbolo CLAM_VIRUS_FAIL, y la regla de force_actions.conf lo convierte en soft reject.
#
# Compara message_size_limit de deploy/mail/postfix/conf/main.cf.base (bytes; sin el parametro, el
# defecto de Postfix, 10240000), max_message de deploy/mail/rspamd/local.d/options.inc (bytes y
# obligatorio: el defecto cambia con la version de Rspamd; ningun otro fichero de
# deploy/mail/rspamd lo fija, porque lo pisaria), max_size de rspamd/local.d/antivirus.conf,
# StreamMaxLength, MaxFileSize, MaxScanSize y AlertExceedsMax de deploy/mail/clamav/clamd.conf, la
# regla de rspamd/local.d/force_actions.conf, maxPipeMaxBodyMiB y defaultPipeMaxBodyMiB de
# services/mail-security/main.go, MaxQuarantineMaxSizeBytes de su dominio y el CHECK de la
# migracion 08, postfixMessageSizeLimit de services/webmail/main.go y MAIL_QUARANTINE_MAX_BODY_MB
# de .env.example. MARGEN (1 MiB) cubre las cabeceras que reconstruye el milter y el envoltorio de
# /pipe con sus metadatos:
#   max_message, max_size, StreamMaxLength y MaxFileSize >= message_size_limit + MARGEN
#   MaxScanSize >= MaxFileSize, AlertExceedsMax = yes, y soft reject sobre CLAM_VIRUS_FAIL
#   message_size_limit + MARGEN <= techo de /pipe <= max_message + MARGEN
#   MaxQuarantineMaxSizeBytes = techo de /pipe = techo del CHECK de la migracion 08
#   postfixMessageSizeLimit del webmail = message_size_limit
#   1 <= defecto de /pipe y valor de .env.example <= techo
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAIN_CF="$ROOT/deploy/mail/postfix/conf/main.cf.base"
RSPAMD_DIR="$ROOT/deploy/mail/rspamd"
OPTIONS_INC="$RSPAMD_DIR/local.d/options.inc"
AV_CONF="$RSPAMD_DIR/local.d/antivirus.conf"
FORCE_ACTIONS="$RSPAMD_DIR/local.d/force_actions.conf"
CLAMD_CONF="$ROOT/deploy/mail/clamav/clamd.conf"
MAIN_GO="$ROOT/services/mail-security/main.go"
ENTITIES_GO="$ROOT/services/mail-security/internal/domain/entities.go"
MIGRATION="$ROOT/migrations/cell/canonical/mail-security/08_quarantine_max_size.sql"
WEBMAIL_GO="$ROOT/services/webmail/main.go"
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

# go_mib <fichero> <constante>: N de "[const] constante [tipo] = N * 1024 * 1024", en MiB.
go_mib() {
  sed -nE "s#^[[:space:]]*(const[[:space:]]+)?$2([[:space:]]+[[:alnum:]]+)?[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*\*[[:space:]]*1024[[:space:]]*\*[[:space:]]*1024.*#\3#p" "$1" | head -1
}

# go_shift20 <fichero> <constante>: N de "constante = N << 20", en MiB.
go_shift20() {
  sed -nE "s#^[[:space:]]*$2[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*<<[[:space:]]*20.*#\1#p" "$1" | head -1
}

# clamd_param <clave>: ultimo valor de "Clave valor" en clamd.conf (clamd aplica el ultimo).
clamd_param() { sed -nE "s/^[[:space:]]*$1[[:space:]]+([^[:space:]#]+).*/\1/p" "$CLAMD_CONF" | tail -1; }

# clam_bytes <valor>: tamano de clamd.conf (N, NK, NM o NG) en bytes.
clam_bytes() {
  local n="$1" mult=1
  case "$1" in
    *[Kk]) n="${1%?}"; mult=1024 ;;
    *[Mm]) n="${1%?}"; mult=$MIB ;;
    *[Gg]) n="${1%?}"; mult=$((MIB * 1024)) ;;
  esac
  [[ "$n" =~ ^[0-9]+$ ]] && echo $((10#$n * mult))
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

AV_RAW=$(sed -nE 's/^[[:space:]]*max_size[[:space:]]*=[[:space:]]*([^;[:space:]#]+).*/\1/p' "$AV_CONF" | tail -1)
AV=""
if [[ -z "$AV_RAW" ]]; then
  falla "falta max_size en $AV_CONF: sin el rige el defecto del modulo (20 MB) y todo mensaje mayor se entrega sin analizar"
else
  AV=$(bytes "$AV_RAW") || { AV=""; falla "max_size = $AV_RAW en $AV_CONF: se escribe en bytes (p. ej. 105906176)"; }
fi
# Solo la regla clamav fija max_size en local.d; el max_size de oletools vive en el entrypoint.
OTROS=$(grep -rlE '^[[:space:]]*max_size[[:space:]]*=' "$RSPAMD_DIR/local.d" | grep -vxF "$AV_CONF")
if [[ -n "$OTROS" ]]; then
  falla "el max_size del antivirus solo se fija en $AV_CONF; tambien aparece en: $(echo $OTROS)"
fi

S=$(clam_bytes "$(clamd_param StreamMaxLength)") || { S=""; falla "StreamMaxLength de $CLAMD_CONF no es un tamano (N, NK, NM o NG)"; }
F=$(clam_bytes "$(clamd_param MaxFileSize)") || { F=""; falla "MaxFileSize de $CLAMD_CONF no es un tamano (N, NK, NM o NG)"; }
SC=$(clam_bytes "$(clamd_param MaxScanSize)") || { SC=""; falla "MaxScanSize de $CLAMD_CONF no es un tamano (N, NK, NM o NG)"; }
ALERT=$(clamd_param AlertExceedsMax)
if [[ "${ALERT,,}" != yes ]]; then
  falla "AlertExceedsMax = ${ALERT:-<ausente>} en $CLAMD_CONF: sin el, lo que clamd analiza truncado responde \"stream: OK\" y pasa por limpio. Ponlo en yes"
fi
if ! perl -0777 -ne 'exit 1 unless /\{[^{}]*?(?:action\s*=\s*"soft reject"[^{}]*?expression\s*=\s*"CLAM_VIRUS_FAIL"|expression\s*=\s*"CLAM_VIRUS_FAIL"[^{}]*?action\s*=\s*"soft reject")[^{}]*?\}/s' "$FORCE_ACTIONS"; then
  falla "falta en $FORCE_ACTIONS una regla con action = \"soft reject\" y expression = \"CLAM_VIRUS_FAIL\": sin ella, clamd caido o un analisis recortado entregan el mensaje sin veredicto"
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

if [[ -n "$P" ]]; then
  for par in "max_size de antivirus.conf:${AV}" "StreamMaxLength de clamd.conf:${S}" "MaxFileSize de clamd.conf:${F}"; do
    nombre="${par%:*}" valor="${par##*:}"
    if [[ -n "$valor" ]] && ((valor < P + MARGIN)); then
      falla "$nombre = $valor < message_size_limit de Postfix ($P) + $MARGIN: un mensaje que Postfix acepta se quedaria sin veredicto de ClamAV"
    fi
  done
fi
if [[ -n "$SC" && -n "$F" ]] && ((SC < F)); then
  falla "MaxScanSize ($SC) < MaxFileSize ($F) en $CLAMD_CONF: el total analizado se agota antes que el fichero y el analisis sale recortado"
fi

W_MIB=$(go_shift20 "$WEBMAIL_GO" postfixMessageSizeLimit)
if [[ -z "$W_MIB" ]]; then
  falla "no se lee postfixMessageSizeLimit = N << 20 en $WEBMAIL_GO"
elif [[ -n "$P" ]] && ((W_MIB * MIB != P)); then
  falla "postfixMessageSizeLimit del webmail (${W_MIB} MiB) != message_size_limit de Postfix ($P): el webmail admitiria adjuntos que la celda no entrega ni ClamAV analiza entero"
fi

B_MIB=$(go_mib "$ENTITIES_GO" MaxQuarantineMaxSizeBytes)
SQL_MAX=$(sed -nE 's/.*max_size_bytes[[:space:]]*<=[[:space:]]*([0-9]+).*/\1/p' "$MIGRATION" | tail -1)
[[ -n "$B_MIB" ]] || falla "no se lee MaxQuarantineMaxSizeBytes = N * 1024 * 1024 en $ENTITIES_GO"
[[ -n "$SQL_MAX" ]] || falla "no se lee el techo max_size_bytes <= N en $MIGRATION"
if [[ -n "$B_MIB" && -n "$Q_MIB" ]] && ((B_MIB != Q_MIB)); then
  falla "MaxQuarantineMaxSizeBytes (${B_MIB} MiB) != maxPipeMaxBodyMiB (${Q_MIB} MiB): una empresa podria fijar un max_size_bytes que /pipe nunca recibe"
fi
if [[ -n "$B_MIB" && -n "$SQL_MAX" ]] && ((SQL_MAX != B_MIB * MIB)); then
  falla "el CHECK de $MIGRATION ($SQL_MAX) != MaxQuarantineMaxSizeBytes ($((B_MIB * MIB))): la base y el servicio no acotan lo mismo"
fi

if [[ $FAIL -eq 0 ]]; then
  echo "  OK: Postfix $P bytes; Rspamd max_message $R y max_size $AV; clamd flujo $S, fichero $F, total $SC con AlertExceedsMax y soft reject sobre CLAM_VIRUS_FAIL; /pipe de 1 a ${Q_MIB} MiB (defecto ${D_MIB}, .env.example ${E_MIB}) con el mismo techo en el dominio y en la migracion 08; webmail ${W_MIB} MiB; margen $MARGIN."
fi
exit $FAIL
