#!/usr/bin/env bash
# Guardarrail: ops/deliverability/verificar-entrega.sh (linea base de entregabilidad, plan de mejoras
# A1) sigue detectando lo que debe, sin red.
#
# Por que: la herramienta es lo que dira a quien opere el servidor si un dominio puede entregar. Una
# regla que deja de morder (un SPF doble que pasa por bueno, un DMARC en p=none dado por protegido, un
# informe DMARC a otro dominio sin autorizacion) no falla: informa OK sobre un dominio que no lo esta.
#
# Se ejecuta con un dig simulado (una tabla de respuestas) y se rompe el dominio de prueba de una
# forma cada vez: cada mutacion tiene que producir su linea. El caso base sano tiene que producir las
# lineas OK. La parte SMTP se apunta a 127.0.0.1, donde no escucha nadie: tiene que dar FALLA, que es
# lo que se debe decir de una IP que no responde.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TOOL="$ROOT/ops/deliverability/verificar-entrega.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

# dig simulado: lee $FAKE_DNS con lineas TIPO<TAB>nombre<TAB>respuesta (PTR: IP en el nombre).
mkdir -p "$TMP/bin"
cat > "$TMP/bin/dig" <<'DIG'
#!/usr/bin/env bash
tipo=""; nombre=""
args=()
for a in "$@"; do
  if [[ "$a" == @* ]]; then printf '%s %s\n' "$a" "${*: -1}" >> "$FAKE_DNS.args"; continue; fi
  [[ "$a" == +* ]] || args+=("$a")
done
if [[ "${args[0]:-}" == "-x" ]]; then tipo=PTR; nombre="${args[1]}"; else tipo="${args[0]}"; nombre="${args[1]}"; fi
awk -F'\t' -v t="$tipo" -v n="$nombre" '$1==t && $2==n {print $3}' "$FAKE_DNS"
DIG
chmod +x "$TMP/bin/dig"

openssl genrsa -out "$TMP/k2048.pem" 2048 2>/dev/null
openssl genrsa -out "$TMP/k1024.pem" 1024 2>/dev/null
pub() { openssl pkey -in "$1" -pubout -outform DER 2>/dev/null | base64 -w0; }
K2048="$(pub "$TMP/k2048.pem")"; K1024="$(pub "$TMP/k1024.pem")"

# base <archivo>: dominio d.test sano.
base() {
  {
    printf 'MX\td.test\t10 mx.d.test.\n'
    printf 'A\tmx.d.test\t127.0.0.1\n'
    printf 'PTR\t127.0.0.1\tmx.d.test.\n'
    printf 'TXT\td.test\t"v=spf1 ip4:127.0.0.1 -all"\n'
    printf 'TXT\ts1._domainkey.d.test\t"v=DKIM1; k=rsa; p=%s"\n' "$K2048"
    printf 'TXT\t_dmarc.d.test\t"v=DMARC1; p=reject; rua=mailto:r@d.test"\n'
    printf 'A\td.test\t127.0.0.1\n'
  } > "$1"
}
correr() { PATH="$TMP/bin:$PATH" FAKE_DNS="$1" bash "$TOOL" d.test --ip 127.0.0.1 --selector s1 2>&1; }

# esperar <nombre> <linea esperada> <edicion sed sobre la tabla | vacio>
esperar() {
  local nombre="$1" esperada="$2" edicion="${3:-}" salida
  base "$TMP/dns"
  [[ -z "$edicion" ]] || sed -i "$edicion" "$TMP/dns"
  salida="$(correr "$TMP/dns")"
  if grep -qF -- "$esperada" <<< "$salida"; then echo "  $nombre"; else falla "$nombre: falta '$esperada'"; fi
}

esperar "caso sano: SPF con -all" "OK      SPF: termina en -all"
esperar "caso sano: SPF autoriza la IP" "OK      SPF: parece autorizar a 127.0.0.1"
esperar "caso sano: DKIM de 2048 bits" "OK      DKIM s1: clave de 2048 bits"
esperar "caso sano: DMARC reject" "OK      DMARC: p=reject"
esperar "caso sano: FCrDNS" "OK      PTR: mx.d.test resuelve de vuelta a 127.0.0.1"
esperar "caso sano: una IP que no responde en el 25 es FALLA" "FALLA   SMTP: 127.0.0.1:25 no responde"
esperar "sin MX" "FALLA   MX: el dominio no tiene registro MX" '/^MX\t/d'
esperar "dos registros SPF" "FALLA   SPF: 2 registros v=spf1" '/^TXT\td.test/{p;s/-all/~all/}'
esperar "SPF con ~all" "AVISO   SPF: ~all (softfail)" 's/ip4:127.0.0.1 -all/ip4:127.0.0.1 ~all/'
esperar "SPF con +all" "FALLA   SPF: ?all o +all no protege" 's/ip4:127.0.0.1 -all/ip4:127.0.0.1 +all/'
esperar "clave DKIM de 1024 bits" "AVISO   DKIM s1: clave de 1024 bits" "s|p=$K2048|p=$K1024|"
esperar "clave DKIM revocada (p= vacio)" "FALLA   DKIM s1: la clave publica esta vacia" "s|p=$K2048|p=|"
esperar "sin registro DKIM" "FALLA   DKIM: sin registro en s1._domainkey.d.test" '/^TXT\ts1\./d'
esperar "DMARC en p=none" "AVISO   DMARC: p=none solo observa" 's/p=reject/p=none/'
esperar "sin DMARC" "FALLA   DMARC: sin registro en _dmarc.d.test" '/^TXT\t_dmarc/d'
esperar "informes DMARC a otro dominio sin autorizacion" "AVISO   DMARC: los informes van a r@otro.test, de otro dominio" 's/r@d.test/r@otro.test/'
esperar "PTR que no resuelve de vuelta" "FALLA   PTR: mx.d.test no resuelve a 127.0.0.1" 's/^A\tmx.d.test\t127.0.0.1/A\tmx.d.test\t10.9.9.9/'
esperar "IP sin PTR" "FALLA   PTR: 127.0.0.1 no tiene registro inverso" '/^PTR/d'
esperar "IP en una lista negra" "FALLA   lista negra zen.spamhaus.org: la IP FIGURA" '$a\A\t1.0.0.127.zen.spamhaus.org\t127.0.0.2'
esperar "lista negra que rechaza la consulta" "AVISO   lista negra zen.spamhaus.org: NO CONCLUYENTE" '$a\A\t1.0.0.127.zen.spamhaus.org\t127.255.255.254'

# --resolver: las consultas van al resolvedor indicado, salvo las de listas negras.
base "$TMP/dns"; rm -f "$TMP/dns.args"
PATH="$TMP/bin:$PATH" FAKE_DNS="$TMP/dns" bash "$TOOL" d.test --ip 127.0.0.1 --selector s1 --resolver 9.9.9.9 >/dev/null 2>&1
if grep -qE '^@9\.9\.9\.9 d\.test$' "$TMP/dns.args" 2>/dev/null; then echo "  --resolver: las consultas DNS van al resolvedor indicado"; else falla "--resolver: no se uso @9.9.9.9 en la consulta de MX"; fi
if grep -q 'spamhaus' "$TMP/dns.args" 2>/dev/null; then falla "--resolver: una consulta de lista negra fue al resolvedor publico"; else echo "  --resolver: las listas negras siguen con el resolvedor del sistema"; fi
base "$TMP/dns"
if PATH="$TMP/bin:$PATH" FAKE_DNS="$TMP/dns" bash "$TOOL" d.test --ip 127.0.0.1 --resolver 'x;rm' >/dev/null 2>&1; then falla "--resolver: acepto un valor que no es una IP"; else echo "  --resolver: rechaza un valor que no es una IP"; fi

# informes DMARC a otro dominio CON autorizacion: OK.
base "$TMP/dns"; sed -i 's/r@d.test/r@otro.test/' "$TMP/dns"
printf 'TXT\td.test._report._dmarc.otro.test\t"v=DMARC1"\nA\totro.test\t127.0.0.1\n' >> "$TMP/dns"
if grep -qF "OK      DMARC: otro.test autoriza recibir informes de d.test" <<< "$(correr "$TMP/dns")"; then
  echo "  informes DMARC a otro dominio con autorizacion"
else falla "informes DMARC con autorizacion: no dio OK"; fi

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: la herramienta de entregabilidad detecta MX, SPF, DKIM, DMARC, PTR, SMTP y listas negras; 24 casos con un dig simulado."
