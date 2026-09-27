#!/usr/bin/env bash
# Pase de desarrollo del detector de navegador automatizado en produccion
# (docs/Plan_Proteccion_Frente_a_Bots.md, capa 2; services/gateway/automation_pass.go).
#
#   scripts/pase-automatizacion.sh on [horas] [cidr]   # abre el pase (por defecto 4 h, tu IP actual)
#   scripts/pase-automatizacion.sh off                  # lo cierra
#   scripts/pase-automatizacion.sh estado               # que hay en el servidor
#
# Mientras el pase esta abierto, la web que reciben las IP del rango monta la aplicacion aunque el
# navegador este automatizado (Playwright, el MCP de Chrome DevTools); el resto de internet sigue
# bloqueado. Caduca solo a la hora dicha, no puede durar mas de 168 h ni abarcar mas de 256
# direcciones, y mientras dura suena la alerta PaseDeAutomatizacionActivo. Escribe las dos
# variables WEB_AUTOMATION_OBSERVE_* en el .env del servidor y recrea el gateway (segundos).
#
# Mismo transporte que los despliegues: DEPLOY_HOST, DEPLOY_USER, DEPLOY_SSH_KEY.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib/despliegue.sh
source scripts/lib/despliegue.sh
remote() { "${SSH[@]}" "cd $DEPLOY_PATH && $*"; }

accion="${1:-}"
case "$accion" in
  on|off|estado) ;;
  *) sed -n '2,8p' "$0" >&2; exit 2 ;;
esac
despliegue_comprobar_conexion || exit 1

estado() {
  local lineas
  lineas="$(remote "grep -E '^WEB_AUTOMATION_OBSERVE_' .env || true")"
  if [[ -z "$lineas" ]]; then
    echo "pase cerrado: el gateway sirve la web en enforce a todo el mundo"
    return 0
  fi
  echo "$lineas"
  local until
  until="$(sed -n 's/^WEB_AUTOMATION_OBSERVE_UNTIL=//p' <<<"$lineas")"
  if [[ -n "$until" && "$(date -u -d "$until" +%s 2>/dev/null || echo 0)" -le "$(date -u +%s)" ]]; then
    echo "el pase ya caduco: el gateway lo ignora; 'off' limpia las variables"
  fi
}

# Recrea el gateway con la MISMA imagen que dejo el ultimo despliegue: el tag de .deployed-tag y el
# override de imagenes que corresponda (docker save deja core-force-mail/<svc>; si no, el registro).
# Sin el override, compose buscaria app-gateway:latest, que no existe, y no recrearia nada.
recrear_gateway() {
  local compose_args tag imagenes
  compose_args="$(remote "ops/maintenance/perfil-despliegue.sh --compose")"
  tag="$(remote 'cat .deployed-tag 2>/dev/null' || true)"
  [[ -n "$tag" ]] || { echo "el servidor no tiene .deployed-tag: despliega primero con scripts/deploy-ecr.sh" >&2; exit 1; }
  if remote "docker inspect --format '{{.Config.Image}}' app-gateway-1" | grep -q '^core-force-mail/'; then
    imagenes=docker-compose.images.save.yml
  else
    imagenes=docker-compose.images.yml
  fi
  remote "export DEPLOY_TAG=$tag && ops/security/secrets/with-secrets.sh docker compose $compose_args -f $imagenes up -d --no-deps --no-build gateway" >/dev/null
  remote "ops/maintenance/esperar-sanos.sh --proyecto app gateway" || {
    echo "!! el gateway no arranco: revisa 'docker logs app-gateway-1' (un pase mal escrito lo impide arrancar)" >&2
    exit 1
  }
}

# Sustituye las dos variables en el .env del servidor conservando el resto y los permisos (0600).
escribir_env() {
  local contenido="$1"
  remote "umask 077 && grep -vE '^WEB_AUTOMATION_OBSERVE_' .env > .env.pase.tmp && printf '%s' '$contenido' >> .env.pase.tmp && cat .env.pase.tmp > .env && rm -f .env.pase.tmp"
}

case "$accion" in
  estado)
    estado
    ;;
  off)
    escribir_env ""
    recrear_gateway
    echo "pase cerrado"
    ;;
  on)
    horas="${2:-4}"
    [[ "$horas" =~ ^[0-9]+$ && "$horas" -ge 1 && "$horas" -le 168 ]] || { echo "horas: entero de 1 a 168" >&2; exit 2; }
    cidr="${3:-}"
    if [[ -z "$cidr" ]]; then
      # La IP publica desde la que se conecta este puesto, vista por el propio servidor: sin
      # consultar a terceros.
      ip="$(remote 'echo "${SSH_CLIENT%% *}"')"
      [[ -n "$ip" ]] || { echo "no se pudo saber tu IP; indica el CIDR como tercer argumento" >&2; exit 1; }
      if [[ "$ip" == *:* ]]; then cidr="$ip/128"; else cidr="$ip/32"; fi
    fi
    [[ "$cidr" =~ ^[0-9A-Fa-f.:]+/[0-9]{1,3}$ ]] || { echo "cidr: formato ip/prefijo" >&2; exit 2; }
    until="$(date -u -d "+${horas} hours" +%Y-%m-%dT%H:%M:%SZ)"
    escribir_env "WEB_AUTOMATION_OBSERVE_UNTIL=$until"$'\n'"WEB_AUTOMATION_OBSERVE_CIDRS=$cidr"$'\n'
    recrear_gateway
    echo "pase abierto hasta $until (UTC) para $cidr"
    echo "cierra antes con: $0 off"
    ;;
esac
