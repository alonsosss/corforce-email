#!/usr/bin/env bash
# Instala o actualiza los temporizadores del respaldo en un servidor, aprovisionado o no.
#
#   sudo ops/backup/install-timers.sh                     # usuario deploy, /opt/core-force-mail/app
#   sudo DEPLOY_USER=deploy DEPLOY_PATH=/opt/core-force-mail/app ops/backup/install-timers.sh
#
# Idempotente: reescribe las unidades de ops/backup/systemd con el usuario y la ruta del servidor,
# prepara los directorios que el trabajo necesita con su dueno y activa los temporizadores. Lo
# llama bootstrap.sh; en un servidor ya aprovisionado se ejecuta tal cual, tras el despliegue que
# trae los guiones (las unidades apuntan a $DEPLOY_PATH/ops/backup).
#
# El trabajo corre como el usuario de despliegue, nunca como root: es el dueno del .env y de los
# volcados, y en el perfil autoalojado necesita el grupo docker para lanzar las herramientas de
# Postgres en la red interna.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIDADES="$SCRIPT_DIR/systemd"
DEPLOY_USER="${DEPLOY_USER:-deploy}"
CORE_ROOT="${CORE_ROOT:-/opt/core-force-mail}"
DEPLOY_PATH="${DEPLOY_PATH:-$CORE_ROOT/app}"
METRICS_DIR="${METRICS_TEXTFILE_DIR:-$CORE_ROOT/metrics}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
PLANTILLA_USUARIO=deploy
PLANTILLA_RUTA=/opt/core-force-mail/app
TEMPORIZADORES=(core-force-mail-backup.timer core-force-mail-backup-mail.timer core-force-mail-backup-verify.timer)

die() { echo "install-timers: $*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || die "ejecuta como root (sudo)"
[[ -d "$UNIDADES" ]] || die "no existe $UNIDADES"
id "$DEPLOY_USER" >/dev/null 2>&1 || die "no existe el usuario $DEPLOY_USER"
[[ "$DEPLOY_PATH" =~ ^/[A-Za-z0-9._/-]+$ ]] || die "DEPLOY_PATH debe ser una ruta absoluta sin espacios"

# valor_env <CLAVE>: configuracion no secreta del .env del despliegue (la ultima asignacion).
valor_env() {
  [[ -f "$DEPLOY_PATH/.env" ]] || return 0
  sed -n -E "s/^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=(.*)$/\\2/p" "$DEPLOY_PATH/.env" | tail -n 1 |
    sed -E "s/^\"(.*)\"$/\\1/; s/^'(.*)'$/\\1/"
}

if [[ "$(valor_env DEPLOY_PROFILE)" == selfhosted ]] && ! id -nG "$DEPLOY_USER" | tr ' ' '\n' | grep -qx docker; then
  die "$DEPLOY_USER no esta en el grupo docker: en el perfil autoalojado el respaldo no llega a Postgres"
fi

backup_dir="$(valor_env BACKUP_DIR)"
backup_dir="${backup_dir:-$CORE_ROOT/backups}"
secretos="$(valor_env BACKUP_SECRETS_FILE)"
secretos="${secretos:-$CORE_ROOT/env/backup.env}"
install -d -m 0700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$backup_dir"
install -d -m 0755 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$METRICS_DIR"
install -d -m 0700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$(dirname "$secretos")"
if [[ -f "$secretos" ]]; then
  chown "$DEPLOY_USER:$DEPLOY_USER" "$secretos"
  chmod 0600 "$secretos"
fi

cambios=0
for unidad in "$UNIDADES"/*.service "$UNIDADES"/*.timer; do
  destino="$SYSTEMD_DIR/$(basename "$unidad")"
  nuevo="$(sed -e "s|^User=$PLANTILLA_USUARIO\$|User=$DEPLOY_USER|" -e "s|$PLANTILLA_RUTA|$DEPLOY_PATH|g" "$unidad")"
  if [[ ! -f "$destino" || "$(cat "$destino")" != "$nuevo" ]]; then
    printf '%s\n' "$nuevo" >"$destino.tmp"
    chmod 0644 "$destino.tmp"
    mv -f "$destino.tmp" "$destino"
    echo "install-timers: $(basename "$unidad") instalada"
    cambios=1
  fi
done
if [[ $cambios -eq 1 ]]; then
  systemctl daemon-reload
fi
systemctl enable --now "${TEMPORIZADORES[@]}" >/dev/null

# Los temporizadores reemplazan al crontab de cuando el usuario de despliegue no tenia root: los
# dos a la vez correrian dos volcados simultaneos de la misma base.
if crontab -u "$DEPLOY_USER" -l 2>/dev/null | grep -q 'ops/backup/'; then
  echo "install-timers: retirando las entradas de cron del respaldo que ahora lleva systemd"
  crontab -u "$DEPLOY_USER" -l 2>/dev/null |
    grep -v -e 'ops/backup/' -e '^# Core Force: respaldo por empresa' \
      -e '^# si algun dia se instalan las unidades de systemd' -e '^# para no duplicar la ejecucion' |
    crontab -u "$DEPLOY_USER" -
fi

for t in "${TEMPORIZADORES[@]}"; do
  echo "install-timers: $t $(systemctl is-enabled "$t") / $(systemctl is-active "$t")"
done
[[ -x "$DEPLOY_PATH/ops/backup/backup-tenants.sh" ]] ||
  echo "install-timers: AVISO: aun no hay $DEPLOY_PATH/ops/backup/backup-tenants.sh; el trabajo fallara hasta el primer despliegue" >&2
