#!/usr/bin/env bash
# Segunda barrera del ejecutor de migraciones de buzones (docs/adr/0002, "Red"): en DOCKER-USER, lo que
# el ejecutor manda fuera de su red solo puede ir a los puertos IMAP de origen de una direccion publica.
# Las redes privadas, CGNAT, el enlace local (el metadata del proveedor), loopback y multicast se
# rechazan, tambien los puertos publicados del propio host, que llegan por DNAT a una IP privada. La
# guarda de origen del ejecutor ya rechaza esas direcciones; esto es lo que queda si esa guarda falla.
#
#   sudo ops/security/migration-egress.sh --install   # copia el guion a /usr/local/sbin y activa el servicio
#   migration-egress.sh --apply                       # aplica una vez (root)
#   migration-egress.sh --watch                       # aplica y reaplica en cada arranque del ejecutor
#   migration-egress.sh --remove                      # quita la regla
#   migration-egress.sh --print                       # muestra la cadena
#
# La IP del ejecutor la da Docker al crear el contenedor y cambia al recrearlo: por eso el servicio no
# fija una IP, la lee de `docker inspect` y reaplica con cada evento `start` del contenedor. Lo que el
# ejecutor habla dentro de su propia red (Dovecot, clamd, mail-migration) no pasa por iptables sin
# br_netfilter, que filtraria todas las redes Docker del host: esa parte la cubren la credencial por
# trabajo y la guarda del ejecutor.
#
# Se instala como copia de root en /usr/local/sbin y no se ejecuta desde el checkout, que es del usuario de
# despliegue: si el servicio de root corriera un fichero que ese usuario puede editar, cualquiera con el
# despliegue seria root.
set -euo pipefail

CHAIN=CFM-MIGR-EGRESS
BRIDGE="${MAIL_MIGRATION_BRIDGE:-br-mail-migr}"
RUNNER="${MIGRATION_RUNNER_CONTAINER:-mail-mail-migration-runner-1}"
ENV_FILE="${MIGRATION_EGRESS_ENV_FILE:-/opt/core-force-mail/app/.env}"
INSTALL_PATH=/usr/local/sbin/cfm-migration-egress
UNIT=/etc/systemd/system/core-force-mail-migration-egress.service
PRIVATE_NETS="0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4"

log() { echo "migration-egress: $*" >&2; }

# Puertos de origen permitidos: MAIL_MIGRATION_SOURCE_PORTS del .env (configuracion, no secreto), los
# mismos que acepta mail-migration. Solo se leen cifras y comas: nada del fichero se ejecuta.
source_ports() {
  local p=""
  [[ -r "$ENV_FILE" ]] && p="$(sed -n -E 's/^[[:space:]]*MAIL_MIGRATION_SOURCE_PORTS[[:space:]]*=[[:space:]]*"?([0-9,]*)"?[[:space:]]*$/\1/p' "$ENV_FILE" | tail -n 1)"
  p="${p:-143,993}"
  [[ "$p" =~ ^[0-9]{1,5}(,[0-9]{1,5}){0,14}$ ]] || { log "MAIL_MIGRATION_SOURCE_PORTS no valido: $p"; return 1; }
  printf '%s' "$p"
}

runner_ip() {
  local ip
  ip="$(docker inspect "$RUNNER" --format '{{range $n, $v := .NetworkSettings.Networks}}{{$v.IPAddress}} {{end}}' 2>/dev/null | awk '{print $1}')"
  [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] && printf '%s' "$ip"
}

drop_jumps() {
  local rule
  while rule="$(iptables -S DOCKER-USER 2>/dev/null | grep -m1 -- "-j $CHAIN")"; do
    # shellcheck disable=SC2086
    iptables ${rule/-A/-D}
  done
}

apply() {
  local ip ports net
  iptables -L DOCKER-USER -n >/dev/null 2>&1 || { log "sin cadena DOCKER-USER (Docker no ha arrancado)"; return 1; }
  ports="$(source_ports)"
  iptables -N "$CHAIN" 2>/dev/null || true
  iptables -F "$CHAIN"
  iptables -A "$CHAIN" -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN
  for net in $PRIVATE_NETS; do
    iptables -A "$CHAIN" -d "$net" -j REJECT --reject-with icmp-admin-prohibited
  done
  iptables -A "$CHAIN" -p tcp -m multiport --dports "$ports" -j RETURN
  iptables -A "$CHAIN" -j REJECT --reject-with icmp-admin-prohibited

  drop_jumps
  ip="$(runner_ip || true)"
  if [[ -z "$ip" ]]; then
    log "el ejecutor ($RUNNER) no esta en marcha: la cadena queda lista y se enlaza cuando arranque"
    return 0
  fi
  iptables -I DOCKER-USER 1 -i "$BRIDGE" -s "$ip/32" -j "$CHAIN"
  log "aplicada para $ip (puertos de origen $ports)"
}

remove() {
  drop_jumps
  iptables -F "$CHAIN" 2>/dev/null || true
  iptables -X "$CHAIN" 2>/dev/null || true
  log "retirada"
}

watch() {
  apply || true
  # Un reinicio de Docker vacia DOCKER-USER sin generar un `start` del ejecutor si este sigue vivo: el
  # bucle de eventos se corta con Docker y el servicio (Restart=always) vuelve a empezar por apply.
  docker events --filter type=container --filter "container=$RUNNER" --filter event=start --format '{{.Actor.ID}}' |
    while read -r _; do apply || true; done
}

install_service() {
  [[ $EUID -eq 0 ]] || { log "--install necesita root"; exit 1; }
  install -o root -g root -m 0755 "$0" "$INSTALL_PATH"
  cat >"$UNIT" <<EOF
[Unit]
Description=Core Force Mail: salida del ejecutor de migraciones restringida (docs/adr/0002)
After=docker.service
Requires=docker.service

[Service]
Type=simple
Environment=MIGRATION_EGRESS_ENV_FILE=$ENV_FILE
ExecStart=$INSTALL_PATH --watch
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable core-force-mail-migration-egress.service >/dev/null
  systemctl restart core-force-mail-migration-egress.service
  log "instalado en $INSTALL_PATH y activo"
}

case "${1:-}" in
  --install) install_service ;;
  --apply) apply ;;
  --watch) watch ;;
  --remove) remove ;;
  --print) iptables -S DOCKER-USER; iptables -S "$CHAIN" 2>/dev/null || true ;;
  *) echo "uso: $0 --install|--apply|--watch|--remove|--print" >&2; exit 2 ;;
esac
