#!/bin/sh
# Traduce cloudflare-ips.txt a la configuracion de nginx: set_real_ip_from (la IP del visitante
# solo se toma de CF-Connecting-IP si la conexion llega desde Cloudflare) y el mapa
# $edge_desde_cloudflare, que se evalua sobre la IP de la conexion y no sobre la del visitante.
# Una linea que no es un CIDR impide arrancar: un rango mal escrito dejaria fuera a Cloudflare o,
# peor, confiaria en quien no es.
set -eu

origen="${EDGE_CLOUDFLARE_IPS_FILE:-/etc/core-force-mail/edge/cloudflare-ips.txt}"
destino=/etc/nginx/conf.d/00-cloudflare.conf

[ -r "$origen" ] || { echo "edge: no se puede leer $origen" >&2; exit 1; }

v4='^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$'
v6='^[0-9a-fA-F:]*:[0-9a-fA-F:]*/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$'
rangos=""
n=0
while IFS= read -r linea || [ -n "$linea" ]; do
  linea=$(printf '%s' "$linea" | sed -e 's/#.*//' -e 's/[[:space:]]//g')
  [ -z "$linea" ] && continue
  if ! printf '%s' "$linea" | grep -Eq "$v4|$v6"; then
    echo "edge: rango no valido en $origen: '$linea'" >&2
    exit 1
  fi
  rangos="$rangos $linea"
  n=$((n + 1))
done <"$origen"
[ "$n" -gt 0 ] || { echo "edge: $origen no tiene ningun rango" >&2; exit 1; }

{
  echo "# Generado al arrancar desde $origen; no editar."
  for r in $rangos; do echo "set_real_ip_from $r;"; done
  echo "real_ip_header CF-Connecting-IP;"
  echo "real_ip_recursive off;"
  echo "geo \$realip_remote_addr \$edge_desde_cloudflare {"
  echo "  default 0;"
  for r in $rangos; do echo "  $r 1;"; done
  echo "}"
} >"$destino"
echo "edge: $n rangos de Cloudflare cargados"
