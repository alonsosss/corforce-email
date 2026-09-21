#!/usr/bin/env bash
# Linea base de entregabilidad de un dominio y de la IP que envia su correo, solo con consultas
# publicas de lectura: DNS, una conexion TCP y un saludo SMTP. No envia ningun mensaje, no usa
# credenciales y no toca el servidor (plan de mejoras, iniciativa A1).
#
#   verificar-entrega.sh <dominio> [--ip <IP del servidor de correo>] [--selector <selector>]...
#                                  [--estricto]
#
#   <dominio>     el dominio del cliente (por ejemplo mentorenergy.uk)
#   --ip          IP que envia su correo; por defecto, la de su primer MX
#   --selector    selector DKIM a comprobar; se puede repetir
#   --estricto    los AVISO tambien hacen fallar (codigo 1)
#
# Sale con 1 si hay algun FALLA. Cada linea empieza por OK, AVISO, FALLA o INFO. Un AVISO es algo que
# conviene revisar y no siempre es un error: MTA-STS y TLS-RPT son opcionales y una lista negra que
# no responde no dice que la IP este limpia (NO CONCLUYENTE).
#
# Que no puede saber desde fuera: si el servidor puede ENVIAR por el puerto 25 (lo bloquean algunos
# proveedores) ni la reputacion que tiene la IP en Gmail, Outlook o Yahoo. Para eso hay que enviar de
# verdad a buzones de prueba y darse de alta en Google Postmaster Tools y Microsoft SNDS
# (docs/Plan_Estrategico_Mejoras_Correo.md, A1).
set -uo pipefail

DOMINIO=""; IP=""; SELECTORES=(); ESTRICTO=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ip) IP="${2:-}"; shift 2 ;;
    --selector) SELECTORES+=("${2:-}"); shift 2 ;;
    --estricto) ESTRICTO=1; shift ;;
    -h|--help) sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "opcion desconocida: $1" >&2; exit 2 ;;
    *) DOMINIO="${1,,}"; shift ;;
  esac
done
[[ -n "$DOMINIO" ]] || { echo "uso: $0 <dominio> [--ip <IP>] [--selector <selector>]... [--estricto]" >&2; exit 2; }
[[ "$DOMINIO" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}$ ]] || { echo "dominio no valido: $DOMINIO" >&2; exit 2; }
for t in dig openssl base64; do command -v "$t" >/dev/null || { echo "falta $t" >&2; exit 2; }; done

FALLAS=0; AVISOS=0
ok()    { echo "OK      $*"; }
info()  { echo "INFO    $*"; }
aviso() { echo "AVISO   $*"; AVISOS=$((AVISOS + 1)); }
falla() { echo "FALLA   $*"; FALLAS=$((FALLAS + 1)); }
consulta() { dig +short +time=4 +tries=1 "$@" 2>/dev/null; }
txt() { consulta TXT "$1" | sed -E 's/" "//g; s/^"//; s/"$//'; }

echo "== $DOMINIO =="

# ---- MX ----
mapfile -t MX < <(consulta MX "$DOMINIO" | sort -n | awk '{print $2}' | sed 's/\.$//')
if [[ ${#MX[@]} -eq 0 ]]; then
  falla "MX: el dominio no tiene registro MX; no recibe correo"
else
  info "MX: ${MX[*]}"
  if [[ -z "$IP" ]]; then IP="$(consulta A "${MX[0]}" | head -1)"; fi
fi
[[ -n "$IP" ]] && info "IP que se comprueba: $IP" || falla "no se pudo determinar la IP; usar --ip"
[[ "$IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { falla "la IP '$IP' no es IPv4 (solo se comprueba IPv4)"; IP=""; }

# ---- SPF ----
cuenta_spf() { # <dominio> <profundidad>: escribe el numero de terminos que consultan DNS
  local d="$1" prof="$2" reg t n=0 sub
  (( prof > 10 )) && { echo 99; return; }
  reg="$(txt "$d" | grep -i '^v=spf1' | head -1)"
  for t in $reg; do
    case "${t,,}" in
      include:*) n=$((n + 1)); sub="$(cuenta_spf "${t#*:}" $((prof + 1)))"; n=$((n + sub)) ;;
      redirect=*) n=$((n + 1)); sub="$(cuenta_spf "${t#*=}" $((prof + 1)))"; n=$((n + sub)) ;;
      a|a:*|a/*|mx|mx:*|mx/*|ptr|ptr:*|exists:*) n=$((n + 1)) ;;
    esac
  done
  echo "$n"
}
mapfile -t SPFS < <(txt "$DOMINIO" | grep -i '^v=spf1')
if [[ ${#SPFS[@]} -eq 0 ]]; then
  falla "SPF: sin registro v=spf1"
elif [[ ${#SPFS[@]} -gt 1 ]]; then
  falla "SPF: ${#SPFS[@]} registros v=spf1; debe haber uno solo (RFC 7208)"
else
  spf="${SPFS[0]}"; info "SPF: $spf"
  case "$spf" in
    *" -all"|*" -all "*) ok "SPF: termina en -all (rechaza lo no autorizado)" ;;
    *"~all"*) aviso "SPF: ~all (softfail); con DMARC en reject conviene -all" ;;
    *"?all"*|*"+all"*) falla "SPF: ?all o +all no protege el dominio" ;;
    *redirect=*) ok "SPF: delega en otro dominio con redirect" ;;
    *) aviso "SPF: no termina en all ni redirect" ;;
  esac
  n="$(cuenta_spf "$DOMINIO" 0)"
  if (( n > 10 )); then falla "SPF: $n consultas DNS; el maximo es 10 y por encima el resultado es permerror"
  elif (( n >= 8 )); then aviso "SPF: $n consultas DNS de 10 permitidas"
  else ok "SPF: $n consultas DNS de 10 permitidas"; fi
  if [[ -n "$IP" ]]; then
    autorizada=0
    [[ "$spf" == *"ip4:$IP"* ]] && autorizada=1
    if (( autorizada == 0 )); then
      for inc in $(grep -oE 'include:[^ ]+' <<< "$spf" | cut -d: -f2); do
        txt "$inc" | grep -qE "ip4:$IP|a:" && autorizada=1
      done
      grep -qE '(^| )a( |$)|(^| )mx( |$)' <<< "$spf" && autorizada=1
    fi
    (( autorizada == 1 )) && ok "SPF: parece autorizar a $IP (comprobacion aproximada; una IP en un a: o mx: indirecto no se ve)" \
      || aviso "SPF: no se ve que autorice a $IP de forma directa; comprobar a mano con un envio de prueba"
  fi
fi

# ---- DKIM ----
if [[ ${#SELECTORES[@]} -eq 0 ]]; then
  aviso "DKIM: no se indico --selector; no se comprueba (el selector lo elige la plataforma, por ejemplo cfm202609)"
fi
for s in "${SELECTORES[@]}"; do
  reg="$(txt "$s._domainkey.$DOMINIO" | tr -d ' ')"
  if [[ -z "$reg" ]]; then falla "DKIM: sin registro en $s._domainkey.$DOMINIO"; continue; fi
  p="$(grep -oE 'p=[A-Za-z0-9+/=]*' <<< "$reg" | head -1 | cut -c3-)"
  if [[ -z "$p" ]]; then falla "DKIM $s: la clave publica esta vacia (clave revocada)"; continue; fi
  bits="$(printf '%s' "$p" | base64 -d 2>/dev/null | openssl pkey -pubin -inform DER -text -noout 2>/dev/null | sed -nE 's/.*Public-Key: \(([0-9]+) bit\).*/\1/p')"
  if [[ -z "$bits" ]]; then falla "DKIM $s: la clave publica no se puede leer"
  elif (( bits < 1024 )); then falla "DKIM $s: clave de $bits bits, insuficiente"
  elif (( bits < 2048 )); then aviso "DKIM $s: clave de $bits bits; se recomienda 2048"
  else ok "DKIM $s: clave de $bits bits"; fi
done

# ---- DMARC ----
dmarc="$(txt "_dmarc.$DOMINIO" | grep -i '^v=dmarc1' | head -1)"
if [[ -z "$dmarc" ]]; then
  falla "DMARC: sin registro en _dmarc.$DOMINIO"
else
  info "DMARC: $dmarc"
  pol="$(grep -oiE '(^|;) *p=[a-z]+' <<< "$dmarc" | head -1 | sed -E 's/.*p=//I')"
  case "${pol,,}" in
    reject) ok "DMARC: p=reject" ;;
    quarantine) ok "DMARC: p=quarantine (subir a reject cuando los informes muestren cero fuentes legitimas sin alinear)" ;;
    none) aviso "DMARC: p=none solo observa; no protege el dominio" ;;
    *) falla "DMARC: politica ilegible ('$pol')" ;;
  esac
  rua="$(grep -oiE 'rua=[^;]+' <<< "$dmarc" | head -1 | sed -E 's/rua=//I; s/mailto://gI')"
  if [[ -z "$rua" ]]; then aviso "DMARC: sin rua; nadie recibe los informes"
  else
    for a in ${rua//,/ }; do
      dom_rua="${a#*@}"
      if [[ "$dom_rua" == "$DOMINIO" || "$dom_rua" == *".$DOMINIO" ]]; then ok "DMARC: los informes van a $a (mismo dominio)"
      elif [[ -n "$(txt "$DOMINIO._report._dmarc.$dom_rua" | grep -i 'v=dmarc1')" ]]; then ok "DMARC: $dom_rua autoriza recibir informes de $DOMINIO"
      else aviso "DMARC: los informes van a $a, de otro dominio, y $dom_rua no publica la autorizacion $DOMINIO._report._dmarc.$dom_rua; muchos receptores no los enviaran"; fi
      mxrua="$(consulta MX "$dom_rua" | head -1)"; arua="$(consulta A "$dom_rua" | head -1)"
      [[ -n "$mxrua" || -n "$arua" ]] || falla "DMARC: $dom_rua no tiene MX ni A; los informes no llegan"
    done
  fi
fi

# ---- MTA-STS y TLS-RPT (opcionales) ----
if [[ -n "$(txt "_mta-sts.$DOMINIO" | grep -i 'v=STSv1')" ]]; then
  ok "MTA-STS: registro _mta-sts publicado"
  pol="$(curl -fsS -m 8 "https://mta-sts.$DOMINIO/.well-known/mta-sts.txt" 2>/dev/null || true)"
  [[ -n "$pol" ]] && ok "MTA-STS: la politica responde por HTTPS" || falla "MTA-STS: hay registro pero la politica https://mta-sts.$DOMINIO/.well-known/mta-sts.txt no responde"
else
  aviso "MTA-STS: no publicado (opcional; obliga a los remitentes que lo soportan a usar TLS validado)"
fi
[[ -n "$(txt "_smtp._tls.$DOMINIO" | grep -i 'v=TLSRPTv1')" ]] && ok "TLS-RPT: publicado" || aviso "TLS-RPT: no publicado (opcional; informes de fallos de TLS)"

# ---- IP: PTR, saludo, TLS, puertos, listas negras ----
if [[ -n "$IP" ]]; then
  ptr="$(consulta -x "$IP" | head -1 | sed 's/\.$//')"
  if [[ -z "$ptr" ]]; then falla "PTR: $IP no tiene registro inverso; muchos receptores rechazan correo de una IP sin PTR"
  else
    info "PTR: $ptr"
    directa="$(consulta A "$ptr" | tr '\n' ' ')"
    [[ " $directa" == *" $IP "* ]] && ok "PTR: $ptr resuelve de vuelta a $IP (FCrDNS correcto)" \
      || falla "PTR: $ptr no resuelve a $IP (resuelve a: ${directa:-nada})"
  fi

  saludo="$(timeout 12 bash -c "exec 3<>/dev/tcp/$IP/25; read -t 8 l <&3; echo \"\$l\"; printf 'QUIT\r\n' >&3" 2>/dev/null | head -1 | tr -d '\r')"
  if [[ -z "$saludo" ]]; then
    falla "SMTP: $IP:25 no responde desde aqui (cerrado, filtrado o bloqueado por el proveedor de esta maquina)"
  else
    info "SMTP: $saludo"
    host_saludo="$(sed -E 's/^220[- ]//' <<< "$saludo" | awk '{print $1}')"
    if [[ -n "$ptr" && "${host_saludo,,}" == "${ptr,,}" ]]; then ok "SMTP: el saludo ($host_saludo) coincide con el PTR"
    else aviso "SMTP: el saludo ($host_saludo) no coincide con el PTR (${ptr:-sin PTR}); algunos filtros lo penalizan"; fi

    cert="$(echo | timeout 15 openssl s_client -starttls smtp -connect "$IP:25" -servername "${host_saludo:-$IP}" 2>/dev/null | openssl x509 -noout -subject -enddate -ext subjectAltName 2>/dev/null)"
    if [[ -z "$cert" ]]; then falla "TLS 25: el servidor no ofrece STARTTLS o no entrega certificado"
    else
      fin="$(sed -n 's/^notAfter=//p' <<< "$cert")"
      dias=$(( ( $(date -d "$fin" +%s 2>/dev/null || echo 0) - $(date +%s) ) / 86400 ))
      if (( dias < 0 )); then falla "TLS 25: el certificado caduco ($fin)"
      elif (( dias < 15 )); then aviso "TLS 25: el certificado caduca en $dias dias"
      else ok "TLS 25: certificado valido, caduca en $dias dias"; fi
      if grep -qiE "DNS:${host_saludo//./\\.}(,|$| )|CN ?= ?${host_saludo//./\\.}" <<< "$cert"; then ok "TLS 25: el certificado cubre $host_saludo"
      else aviso "TLS 25: el certificado no parece cubrir $host_saludo"; fi
    fi
  fi
  for p in 465 587 993; do
    timeout 6 bash -c "</dev/tcp/$IP/$p" 2>/dev/null && ok "puerto $p abierto" || aviso "puerto $p cerrado o filtrado desde aqui"
  done

  inv="$(awk -F. '{print $4"."$3"."$2"."$1}' <<< "$IP")"
  listada=0
  for zona in zen.spamhaus.org b.barracudacentral.org bl.spamcop.net psbl.surriel.com dnsbl-1.uceprotect.net all.s5h.net; do
    r="$(consulta A "$inv.$zona" | head -1)"
    if [[ -z "$r" ]]; then ok "lista negra $zona: no figura"
    elif [[ "$r" == 127.255.255.* ]]; then aviso "lista negra $zona: NO CONCLUYENTE (respondio $r: consulta rechazada; este resolvedor no puede usarse con esa lista)"
    elif [[ "$r" == 127.* ]]; then falla "lista negra $zona: la IP FIGURA ($r)"; listada=1
    else aviso "lista negra $zona: respuesta inesperada $r"; fi
  done
  (( listada == 1 )) && info "Una IP listada puede pedir la retirada en la web de la lista; comprobar el motivo antes."
fi

echo
echo "Resultado: $FALLAS falla(s), $AVISOS aviso(s)."
(( FALLAS == 0 )) || exit 1
(( ESTRICTO == 1 && AVISOS > 0 )) && exit 1
exit 0
