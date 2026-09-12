#!/usr/bin/env bash
#
# Core Force - plantilla oficial de aprovisionamiento de servidores.
#
# Convierte una EC2 Ubuntu recien creada en un host de produccion estandar,
# identico en cada servidor (prod / dev / ia / reportes). Es IDEMPOTENTE: puede
# ejecutarse varias veces sin romper nada.
#
# Que hace:
#   - Paquetes base + Docker Engine + Compose V2 (repo oficial de Docker)
#   - Usuario de despliegue con grupo docker y llave SSH autorizada
#   - /etc/docker/daemon.json (rotacion de logs, live-restore, ulimits)
#   - /etc/default/grub.d/99-core-force-mail-recuperacion.cfg (consola serie en GRUB y
#     espera de 30 s solo tras un arranque fallido)
#   - Endurecimiento SSH (solo llaves), UFW (solo SSH a nivel host), fail2ban
#   - Actualizaciones de seguridad automaticas
#   - Tuning de kernel (sysctl) y swapfile de seguridad
#   - Zona horaria y estructura de carpetas de despliegue
#
# No toca los puertos 80/443: esos los gobierna el Security Group de AWS (lock
# primario) y, como defensa en profundidad, ops/security/cloudflare-firewall.sh.
#
# Uso:
#   cp server.env.example server.env   # ajusta valores
#   sudo ./bootstrap.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_DIR="$SCRIPT_DIR/config"

# ----------------------------------------------------------------------------
# Configuracion (server.env > entorno > defaults)
# ----------------------------------------------------------------------------
if [[ -f "$SCRIPT_DIR/server.env" ]]; then
  # shellcheck disable=SC1091
  source "$SCRIPT_DIR/server.env"
fi

SERVER_ROLE="${SERVER_ROLE:-prod}"
SERVER_HOSTNAME="${SERVER_HOSTNAME:-}"
DEPLOY_USER="${DEPLOY_USER:-deploy}"
CORE_ROOT="${CORE_ROOT:-/opt/core-force}"
DEPLOY_PATH="${DEPLOY_PATH:-${CORE_ROOT}/app}"
DEPLOY_PUBKEY="${DEPLOY_PUBKEY:-}"
SWAP_SIZE="${SWAP_SIZE:-4G}"
TZ="${TZ:-America/Lima}"

log()  { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[advertencia]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || die "ejecuta como root (sudo ./bootstrap.sh)"
[[ -r /etc/os-release ]] || die "no se pudo leer /etc/os-release"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || warn "probado en Ubuntu; ID detectado: ${ID:-desconocido}"

export DEBIAN_FRONTEND=noninteractive

# ----------------------------------------------------------------------------
# 1) Paquetes base
# ----------------------------------------------------------------------------
log "Actualizando indice de paquetes e instalando base"
apt-get update -y
# UFW gestiona el firewall del host y persiste sus propias reglas. No se instala
# iptables-persistent/netfilter-persistent porque en Ubuntu 24.04 entran en
# conflicto con ufw; el lockdown de Cloudflare persiste por su systemd unit.
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg git jq ufw fail2ban \
  unattended-upgrades chrony htop ncdu

# ----------------------------------------------------------------------------
# 2) Zona horaria y hostname
# ----------------------------------------------------------------------------
log "Configurando zona horaria: $TZ"
timedatectl set-timezone "$TZ" || warn "no se pudo fijar la zona horaria"

if [[ -n "$SERVER_HOSTNAME" && "$(hostnamectl --static 2>/dev/null)" != "$SERVER_HOSTNAME" ]]; then
  log "Fijando hostname: $SERVER_HOSTNAME"
  hostnamectl set-hostname "$SERVER_HOSTNAME"
fi

# ----------------------------------------------------------------------------
# 3) Docker Engine + Compose V2 (repositorio oficial)
# ----------------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  log "Instalando Docker Engine + Compose V2"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  codename="${VERSION_CODENAME:-}"
  case "$codename" in
    focal|jammy|noble) : ;;
    *) warn "codename '$codename' no soportado por el repo Docker; usando 'noble'"; codename="noble" ;;
  esac

  arch="$(dpkg --print-architecture)"
  echo "deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${codename} stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -y
  apt-get install -y \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
else
  log "Docker ya instalado: $(docker --version)"
fi

systemctl enable --now docker

# ----------------------------------------------------------------------------
# 4) /etc/docker/daemon.json
# ----------------------------------------------------------------------------
log "Aplicando /etc/docker/daemon.json"
if ! cmp -s "$CONFIG_DIR/daemon.json" /etc/docker/daemon.json 2>/dev/null; then
  if [[ -f /etc/docker/daemon.json ]]; then
    cp -a /etc/docker/daemon.json "/etc/docker/daemon.json.bak.$(date -u +%Y%m%d%H%M%S 2>/dev/null || echo prev)"
    warn "daemon.json existente respaldado antes de sobrescribir"
  fi
  install -m 0644 "$CONFIG_DIR/daemon.json" /etc/docker/daemon.json
  # live-restore y log-opts requieren restart (reload no los aplica). Con
  # live-restore ya activo, el restart no detiene los contenedores en marcha.
  log "Reiniciando Docker para aplicar daemon.json"
  systemctl restart docker
else
  log "daemon.json sin cambios; no se reinicia Docker"
fi

# ----------------------------------------------------------------------------
# 4b) GRUB: poder recuperar la maquina si un kernel nuevo no arranca
# ----------------------------------------------------------------------------
# La imagen de AWS deja GRUB mudo por el puerto serie y sin ninguna espera, y
# ademas con GRUB_RECORDFAIL_TIMEOUT=0, que desactiva la red de seguridad de
# Ubuntu para "el arranque anterior fallo". Con los parches aplicandose solos y
# el reinicio desatendido, un kernel que no arranca dejaria la maquina en bucle
# sin forma de entrar: habria que desconectar el disco y montarlo en otra
# instancia.
#
# Se instala como fichero propio (99-) en vez de editar el de la imagen: los de
# ese directorio se leen en orden alfabetico, asi que este manda, y una
# actualizacion de la imagen no lo pisa.
#
# Requiere ademas la consola serie habilitada EN LA CUENTA:
#   aws ec2 enable-serial-console-access --region us-east-1
log "Aplicando GRUB de recuperacion (consola serie y espera tras un arranque fallido)"
if ! cmp -s "$CONFIG_DIR/99-grub-recuperacion.cfg" /etc/default/grub.d/99-core-force-mail-recuperacion.cfg 2>/dev/null; then
  install -m 0644 "$CONFIG_DIR/99-grub-recuperacion.cfg" /etc/default/grub.d/99-core-force-mail-recuperacion.cfg
  cp -a /boot/grub/grub.cfg "/boot/grub/grub.cfg.bak.$(date -u +%Y%m%d%H%M%S 2>/dev/null || echo prev)" 2>/dev/null || true
  update-grub
  # Un grub.cfg mal formado no falla al escribirse: falla al arrancar, y para
  # entonces la maquina ya no esta. Se comprueba la sintaxis aqui.
  if grub-script-check /boot/grub/grub.cfg; then
    log "grub.cfg regenerado y con sintaxis correcta"
  else
    warn "grub.cfg NO pasa la comprobacion de sintaxis; revisar antes de reiniciar"
  fi
else
  log "GRUB de recuperacion sin cambios"
fi

# ----------------------------------------------------------------------------
# 5) Usuario de despliegue
# ----------------------------------------------------------------------------
log "Configurando usuario de despliegue: $DEPLOY_USER"
if ! id "$DEPLOY_USER" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$DEPLOY_USER"
fi
usermod -aG docker "$DEPLOY_USER"

deploy_home="$(getent passwd "$DEPLOY_USER" | cut -d: -f6)"
install -d -m 0700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$deploy_home/.ssh"
auth_keys="$deploy_home/.ssh/authorized_keys"
touch "$auth_keys"

add_key() {
  local key="$1"
  [[ -n "$key" ]] || return 0
  grep -qxF "$key" "$auth_keys" 2>/dev/null || echo "$key" >> "$auth_keys"
}

if [[ -n "$DEPLOY_PUBKEY" ]]; then
  add_key "$DEPLOY_PUBKEY"
else
  # Reutiliza la llave del usuario inicial de la AMI para no perder acceso.
  for seed in /home/ubuntu/.ssh/authorized_keys /root/.ssh/authorized_keys; do
    if [[ -s "$seed" ]]; then
      while IFS= read -r line; do
        [[ -n "$line" && "$line" != \#* ]] && add_key "$line"
      done < "$seed"
      warn "DEPLOY_PUBKEY vacio: se reutilizo $seed para $DEPLOY_USER"
      break
    fi
  done
fi
chown "$DEPLOY_USER:$DEPLOY_USER" "$auth_keys"
chmod 0600 "$auth_keys"

# ----------------------------------------------------------------------------
# 6) Estructura de carpetas de despliegue
# ----------------------------------------------------------------------------
log "Creando estructura de despliegue en $CORE_ROOT"
# Estructura estandar Core Force: codigo separado de env, scripts, datos y backups.
install -d -m 0750 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$CORE_ROOT"
for sub in app env docker scripts logs backups data tmp; do
  install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$CORE_ROOT/$sub"
done

# ----------------------------------------------------------------------------
# 7) Endurecimiento SSH (solo llaves)
# ----------------------------------------------------------------------------
log "Endureciendo SSH"
ssh_drop=/etc/ssh/sshd_config.d/99-core-force-mail-hardening.conf
install -m 0644 "$CONFIG_DIR/sshd-hardening.conf" "$ssh_drop"

# Salvaguarda anti-bloqueo: si NINGUN usuario tiene llave, no deshabilites password.
keys_present=0
for f in /home/*/.ssh/authorized_keys /root/.ssh/authorized_keys; do
  [[ -s "$f" ]] && { keys_present=1; break; }
done
if [[ "$keys_present" -eq 0 ]]; then
  warn "no se detectaron llaves SSH; se mantiene PasswordAuthentication=yes para no bloquear el acceso"
  sed -i 's/^PasswordAuthentication no/PasswordAuthentication yes/' "$ssh_drop"
fi

if sshd -t; then
  systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || systemctl restart ssh
else
  rm -f "$ssh_drop"
  die "configuracion sshd invalida; cambios SSH revertidos"
fi

# ----------------------------------------------------------------------------
# 8) UFW (firewall del host: solo SSH; los puertos web los gobierna el SG/CF)
# ----------------------------------------------------------------------------
log "Configurando UFW (host)"
ufw --force default deny incoming
ufw --force default allow outgoing
ufw allow OpenSSH
ufw --force enable

# ----------------------------------------------------------------------------
# 9) fail2ban
# ----------------------------------------------------------------------------
log "Configurando fail2ban"
install -m 0644 "$CONFIG_DIR/jail.local" /etc/fail2ban/jail.local
systemctl enable --now fail2ban
systemctl restart fail2ban

# ----------------------------------------------------------------------------
# 10) Actualizaciones de seguridad automaticas (sin reinicio automatico)
# ----------------------------------------------------------------------------
log "Habilitando actualizaciones de seguridad automaticas"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
cat > /etc/apt/apt.conf.d/51-core-force-mail-unattended <<'EOF'
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
EOF
systemctl enable --now unattended-upgrades 2>/dev/null || true

# ----------------------------------------------------------------------------
# 11) Tuning de kernel
# ----------------------------------------------------------------------------
log "Aplicando tuning de kernel (sysctl)"
install -m 0644 "$CONFIG_DIR/99-core-force-mail-sysctl.conf" /etc/sysctl.d/99-core-force-mail.conf
sysctl --system >/dev/null

log "Limitando el journal de systemd"
install -m 0755 -d /etc/systemd/journald.conf.d
install -m 0644 "$CONFIG_DIR/journald-core-force-mail.conf" /etc/systemd/journald.conf.d/99-core-force-mail.conf
systemctl restart systemd-journald

# ----------------------------------------------------------------------------
# 12) Swapfile de seguridad
# ----------------------------------------------------------------------------
if [[ "$SWAP_SIZE" != "0" ]]; then
  if [[ "$(swapon --show --noheadings 2>/dev/null | wc -l)" -eq 0 ]]; then
    log "Creando swapfile de $SWAP_SIZE"
    # MB para el fallback con dd (acepta formatos tipo "4G" o "2048M").
    swap_mb="$(numfmt --from=iec "$SWAP_SIZE" 2>/dev/null | awk '{print int($1/1024/1024)}')"
    [[ -n "$swap_mb" && "$swap_mb" -gt 0 ]] || swap_mb=4096
    if fallocate -l "$SWAP_SIZE" /swapfile 2>/dev/null \
       || dd if=/dev/zero of=/swapfile bs=1M count="$swap_mb" status=none; then
      chmod 600 /swapfile
      mkswap /swapfile >/dev/null
      swapon /swapfile
      grep -qxF '/swapfile none swap sw 0 0' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
    else
      warn "no se pudo crear el swapfile"
    fi
  else
    log "El host ya tiene swap; se omite el swapfile"
  fi
fi

# ----------------------------------------------------------------------------
# 13) Respaldo por empresa (systemd)
# ----------------------------------------------------------------------------
# El respaldo no puede depender de que alguien se acuerde de instalarlo: un servidor
# sin respaldo se ve exactamente igual que uno con respaldo, hasta el dia que hace
# falta. Por eso se aprovisiona aqui, junto con lo demas.
#
# Solo en el servidor que lleva la base: en un host de solo aplicacion, respaldar
# duplicaria el volcado y competiria por disco sin aportar nada.
# El trabajo deja aqui el resultado de cada corrida y node-exporter lo monta para que
# Prometheus lo recoja. Sin este directorio, que el respaldo deje de correr se ve
# exactamente igual que si corriera.
METRICS_DIR="${METRICS_TEXTFILE_DIR:-/opt/core-force-mail/metrics}"
install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0755 "$METRICS_DIR"

BACKUP_UNITS_DIR="$DEPLOY_PATH/ops/backup/systemd"
if [[ "${INSTALL_BACKUP_TIMERS:-yes}" == "yes" && -d "$BACKUP_UNITS_DIR" ]]; then
  log "Instalando el respaldo por empresa (systemd)"
  install -m 0644 "$BACKUP_UNITS_DIR"/*.service "$BACKUP_UNITS_DIR"/*.timer /etc/systemd/system/
  systemctl daemon-reload
  systemctl enable --now core-force-backup.timer core-force-backup-verify.timer >/dev/null

  # Los timers reemplazan al crontab que se usaba cuando el usuario de despliegue no
  # tenia root. Dejar los dos activos correria el respaldo dos veces: dos volcados
  # simultaneos de la misma base compitiendo por disco y por la conexion.
  if crontab -u "$DEPLOY_USER" -l 2>/dev/null | grep -q 'ops/backup/'; then
    log "Retirando las entradas de cron que ahora lleva systemd"
    crontab -u "$DEPLOY_USER" -l 2>/dev/null \
      | grep -v 'ops/backup/' \
      | grep -v '^# Core Force: respaldo por empresa' \
      | grep -v '^# si algun dia se instalan las unidades de systemd' \
      | grep -v '^# para no duplicar la ejecucion' \
      | crontab -u "$DEPLOY_USER" -
  fi
elif [[ ! -d "$BACKUP_UNITS_DIR" ]]; then
  warn "no se encontro $BACKUP_UNITS_DIR; sincroniza el repo y vuelve a correr para programar el respaldo"
fi

# ----------------------------------------------------------------------------
# Resumen
# ----------------------------------------------------------------------------
log "Aprovisionamiento completado"
cat <<EOF

  Rol del servidor : $SERVER_ROLE
  Hostname         : $(hostnamectl --static 2>/dev/null || hostname)
  Docker           : $(docker --version 2>/dev/null)
  Compose          : $(docker compose version 2>/dev/null | head -n1)
  Usuario deploy   : $DEPLOY_USER  (grupo docker)
  Ruta de deploy   : $DEPLOY_PATH
  SSH              : solo llaves (UFW permite 22), fail2ban activo
  Firewall host    : $(ufw status | head -n1)
  Respaldo         : $(systemctl is-enabled core-force-backup.timer 2>/dev/null || echo 'no instalado')

  Siguientes pasos:
    1) Configura el Security Group (22 desde tu admin, 80/443 desde Cloudflare).
       Ver ops/security/AWS-DEPLOYMENT.md
    2) (Defensa en profundidad) instala el lockdown de Cloudflare:
       sudo ops/security/systemd/install.sh
    3) Despliega desde tu maquina:
       DEPLOY_HOST=<ip> DEPLOY_USER=$DEPLOY_USER DEPLOY_PATH=$DEPLOY_PATH \\
         ./scripts/deploy-fast.sh

EOF
