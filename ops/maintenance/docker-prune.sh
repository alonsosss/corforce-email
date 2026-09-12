#!/usr/bin/env bash
# Poda periodica de Docker en el servidor. Pensado para ejecutarse como root via systemd.
#
# El servidor NUNCA compila: las imagenes llegan hechas desde ECR (scripts/deploy-ecr.sh).
# Por eso aqui no se conserva reserva de build cache: cualquier cache que exista es el
# resto de una compilacion que no debio ocurrir, y se retira entera. Lo mismo con las
# imagenes locales del proyecto Compose (app-<svc>): si ningun contenedor las usa, son el
# rastro de un "compose build" hecho a mano y ademas anclan la cache de BuildKit.
#
# NO toca imagenes ni contenedores en uso, ni volumenes.
#
# Trampa del almacen containerd (2026-08-29): tras compilar en el servidor, BuildKit puede
# dejar registros marcados "en uso" que `builder prune -a` no libera (8,7 GB en el caso
# real). Los suelta un `systemctl restart docker`; con live-restore los contenedores
# siguen corriendo. Se avisa en el log en vez de reiniciar el demonio desde un timer.
set -uo pipefail

DOCKER="${DOCKER:-docker}"
COMPOSE_PROJECT="${COMPOSE_PROJECT_NAME:-app}"

log() { echo "[$(date -Is)] $*"; }

log "uso de disco (antes):"; df -h / | tail -1

log "imagenes locales ${COMPOSE_PROJECT}-* sin contenedor"
mapfile -t EN_USO < <($DOCKER ps -a --format '{{.Image}}' | sort -u)
mapfile -t LOCALES < <($DOCKER images --format '{{.Repository}}:{{.Tag}}' | grep "^${COMPOSE_PROJECT}-")
retiradas=0
for img in "${LOCALES[@]}"; do
  en_uso=0
  for u in "${EN_USO[@]}"; do [ "$u" = "$img" ] && en_uso=1 && break; done
  [ "$en_uso" = "1" ] && continue
  if $DOCKER rmi "$img" >/dev/null 2>&1; then
    retiradas=$((retiradas + 1))
    log "  retirada $img"
  fi
done
log "  ${retiradas} imagenes locales retiradas"

log "builder prune -a (sin reserva: el servidor no compila)"
$DOCKER builder prune -a -f 2>&1 | tail -1 || true
restante=$($DOCKER system df --format '{{.Type}} {{.Size}}' 2>/dev/null | awk '/^Build Cache/{print $3}')
case "$restante" in
  0B|"") ;;
  *) log "  AVISO: quedan ${restante} de build cache marcados en uso; los libera 'systemctl restart docker' (live-restore mantiene los contenedores)" ;;
esac

log "image prune (solo dangling)"
$DOCKER image prune -f 2>&1 | tail -1 || true

log "uso de disco (despues):"; df -h / | tail -1
