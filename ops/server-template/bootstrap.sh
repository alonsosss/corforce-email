#!/usr/bin/env bash
#
# Core Force Mail - plantilla de aprovisionamiento de servidores.
#
# Convierte una EC2 Ubuntu recien creada (o un servidor propio Ubuntu o Debian, perfil
# autoalojado) en un host de la plataforma, identico en cada ambiente. Es IDEMPOTENTE: puede
# ejecutarse varias veces sin romper nada.
#
# Que hace:
#   - Paquetes base + Docker Engine + Compose V2 (repo oficial de Docker)
#   - Usuario de despliegue con grupo docker y llave SSH autorizada
#   - /etc/docker/daemon.json (rotacion de logs, live-restore, ulimits)
#   - /etc/default/grub.d/99-core-force-mail-recuperacion.cfg (consola serie en GRUB y
#     espera de 30 s solo tras un arranque fallido), solo en EC2 (GRUB_CONSOLA_SERIE)
#   - Endurecimiento SSH (solo llaves), UFW (solo SSH a nivel host), fail2ban
#   - Actualizaciones de seguridad automaticas
#   - Tuning de kernel (sysctl) y swapfile de seguridad
#   - Zona horaria y estructura de carpetas de despliegue
#   - ENVIRONMENT (production o staging) y DEPLOY_PROFILE en el .env de la plataforma
#   - Perfil autoalojado: TLS interno (ops/security/internal-tls.sh) y su renovacion diaria
#
# No toca los puertos publicos: esos los gobierna el Security Group de AWS.
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
  # Un valor con espacios sin comillas (DEPLOY_PUBKEY lo es siempre) hace que `source` ejecute su
  # segunda palabra como orden y aborte con 127 sin decir por que. Se detecta antes, con la causa.
  if grep -nE "^[[:space:]]*[A-Z_]+=[^\"'[:space:]#][^#]*[[:space:]]+[^[:space:]#]" "$SCRIPT_DIR/server.env" >&2; then
    printf '\033[1;31m[error]\033[0m %s\n' "server.env: valores con espacios sin comillas (arriba); ponlos entre comillas dobles, p. ej. DEPLOY_PUBKEY=\"ssh-ed25519 AAAA... comentario\"" >&2
    exit 1
  fi
  # shellcheck disable=SC1091
  source "$SCRIPT_DIR/server.env"
fi

ENVIRONMENT="${ENVIRONMENT:-}"
SERVER_ROLE="${SERVER_ROLE:-prod}"
SERVER_HOSTNAME="${SERVER_HOSTNAME:-}"
DEPLOY_USER="${DEPLOY_USER:-deploy}"
CORE_ROOT="${CORE_ROOT:-/opt/core-force-mail}"
DEPLOY_PATH="${DEPLOY_PATH:-${CORE_ROOT}/app}"
DEPLOY_PUBKEY="${DEPLOY_PUBKEY:-}"
SWAP_SIZE="${SWAP_SIZE:-4G}"
TZ="${TZ:-America/Lima}"
DEPLOY_PROFILE="${DEPLOY_PROFILE:-}"
GRUB_CONSOLA_SERIE="${GRUB_CONSOLA_SERIE:-auto}"

log()  { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[advertencia]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

# Sin valor por defecto: un server.env que no lo declara falla aqui, antes de dejar un
# servidor con los controles de desarrollo (ops/security/secrets/require-server-environment.sh).
case "$ENVIRONMENT" in
  production|staging) ;;
  *) die "ENVIRONMENT en server.env debe ser production o staging (valor: '$ENVIRONMENT'); development y test son solo para local" ;;
esac
case "$DEPLOY_PROFILE" in
  ""|aws|selfhosted) ;;
  *) die "DEPLOY_PROFILE en server.env debe ser aws, selfhosted o vacio (valor: '$DEPLOY_PROFILE')" ;;
esac
case "$GRUB_CONSOLA_SERIE" in
  auto|si|no) ;;
  *) die "GRUB_CONSOLA_SERIE en server.env debe ser auto, si o no (valor: '$GRUB_CONSOLA_SERIE')" ;;
esac

[[ "$(id -u)" -eq 0 ]] || die "ejecuta como root (sudo ./bootstrap.sh)"
[[ -r /etc/os-release ]] || die "no se pudo leer /etc/os-release"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  ubuntu|debian) ;;
  *) warn "probado en Ubuntu y Debian; ID detectado: ${ID:-desconocido}" ;;
esac

export DEBIAN_FRONTEND=noninteractive

# ----------------------------------------------------------------------------
# 1) Paquetes base
# ----------------------------------------------------------------------------
log "Actualizando indice de paquetes e instalando base"
apt-get update -y
# UFW gestiona el firewall del host y persiste sus propias reglas. No se instala
# iptables-persistent/netfilter-persistent porque en Ubuntu 24.04 entran en
# conflicto con ufw.
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
  # El repositorio de Docker es por distribucion: los paquetes de Ubuntu en Debian (el servidor
  # autoalojado es Debian 13) instalan contra otra libc y otro systemd.
  distro="ubuntu"
  [[ "${ID:-}" == "debian" ]] && distro="debian"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL "https://download.docker.com/linux/${distro}/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  codename="${VERSION_CODENAME:-}"
  case "$distro:$codename" in
    ubuntu:focal|ubuntu:jammy|ubuntu:noble|debian:bookworm|debian:trixie) : ;;
    ubuntu:*) warn "codename '$codename' no soportado por el repo Docker; usando 'noble'"; codename="noble" ;;
    debian:*) die "Debian '$codename' no soportado por el repo Docker de esta plantilla (bookworm o trixie)" ;;
  esac

  arch="$(dpkg --print-architecture)"
  echo "deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${distro} ${codename} stable" \
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
# 4b) GRUB: poder recuperar la maquina si un kernel nuevo no arranca (solo EC2)
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
#
# Es propio de EC2. En otro proveedor la consola de rescate es la pantalla (VNC en Netcup, por
# ejemplo): con GRUB_TERMINAL="console serial" el menu se reparte con un puerto serie que nadie
# mira, y la espera de recordfail no existe en Debian. GRUB_CONSOLA_SERIE decide: auto (por
# defecto) lo aplica solo si la maquina es EC2, si lo fuerza y no lo retira. Retirarlo tambien
# converge: un servidor aprovisionado con una version anterior de este guion lo pierde aqui.
es_ec2() {
  # Nitro: el fabricante DMI. Xen (generaciones anteriores): el uuid del hipervisor.
  [[ "$(cat /sys/devices/virtual/dmi/id/sys_vendor 2>/dev/null)" == "Amazon EC2" ]] && return 0
  [[ "$(head -c 3 /sys/hypervisor/uuid 2>/dev/null)" == "ec2" ]] && return 0
  return 1
}
grub_serie="$GRUB_CONSOLA_SERIE"
if [[ "$grub_serie" == auto ]]; then
  if es_ec2; then grub_serie=si; else grub_serie=no; fi
fi
grub_drop=/etc/default/grub.d/99-core-force-mail-recuperacion.cfg

# regenerar_grub: update-grub y comprobacion de sintaxis. Un grub.cfg mal formado no falla al
# escribirse: falla al arrancar, y para entonces la maquina ya no esta.
regenerar_grub() {
  cp -a /boot/grub/grub.cfg "/boot/grub/grub.cfg.bak.$(date -u +%Y%m%d%H%M%S 2>/dev/null || echo prev)" 2>/dev/null || true
  update-grub
  if grub-script-check /boot/grub/grub.cfg; then
    log "grub.cfg regenerado y con sintaxis correcta"
  else
    warn "grub.cfg NO pasa la comprobacion de sintaxis; revisar antes de reiniciar"
  fi
}

if [[ "$grub_serie" == si ]]; then
  log "Aplicando GRUB de recuperacion (consola serie y espera tras un arranque fallido)"
  if ! cmp -s "$CONFIG_DIR/99-grub-recuperacion.cfg" "$grub_drop" 2>/dev/null; then
    install -m 0644 "$CONFIG_DIR/99-grub-recuperacion.cfg" "$grub_drop"
    regenerar_grub
  else
    log "GRUB de recuperacion sin cambios"
  fi
elif [[ -f "$grub_drop" ]]; then
  log "Retirando el GRUB de consola serie de EC2 (GRUB_CONSOLA_SERIE=$GRUB_CONSOLA_SERIE, maquina no EC2 o excluida)"
  rm -f "$grub_drop"
  regenerar_grub
else
  log "GRUB sin consola serie (no es EC2 o GRUB_CONSOLA_SERIE=no)"
fi

# ----------------------------------------------------------------------------
# 5) Usuario de despliegue
# ----------------------------------------------------------------------------
log "Configurando usuario de despliegue: $DEPLOY_USER"
if ! id "$DEPLOY_USER" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$DEPLOY_USER"
fi
usermod -aG docker "$DEPLOY_USER"
# PgBouncer lee userlist.txt por el grupo 70 (el uid del pooler en su imagen) y
# ops/db/pgbouncer-userlist.sh solo puede asignarle ese grupo si el usuario pertenece a el.
grupo_pooler="$(getent group 70 | cut -d: -f1 || true)"
if [[ -z "$grupo_pooler" ]]; then
  groupadd --gid 70 pgbouncer-pool
  grupo_pooler=pgbouncer-pool
fi
usermod -aG "$grupo_pooler" "$DEPLOY_USER"

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
# Codigo separado de logs, datos y respaldos.
install -d -m 0750 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$CORE_ROOT"
for sub in app env docker scripts logs backups data tmp; do
  install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$CORE_ROOT/$sub"
done
install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$DEPLOY_PATH"

# ----------------------------------------------------------------------------
# 6b) Entorno de la plataforma
# ----------------------------------------------------------------------------
# .env.example trae ENVIRONMENT=development para el entorno local, y con ese valor los
# servicios relajan controles. El servidor lo declara aqui, en el .env que Compose pasa a
# los contenedores, y lo repone si alguien lo cambio; with-secrets.sh se niega a desplegar
# con otro valor.
env_app="$DEPLOY_PATH/.env"
[[ -f "$env_app" ]] || install -m 0600 -o "$DEPLOY_USER" -g "$DEPLOY_USER" /dev/null "$env_app"
# declarar_en_env <CLAVE> <valor>: deja una sola asignacion exacta de la clave en el .env.
declarar_en_env() {
  local patron="^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*="
  log "Declarando $1=$2 en $env_app"
  if [[ "$(grep -E "$patron" "$env_app" || true)" != "$1=$2" ]]; then
    sed -i -E "/$patron/d" "$env_app"
    if [[ -s "$env_app" && -n "$(tail -c 1 "$env_app")" ]]; then
      echo >> "$env_app"
    fi
    printf '%s=%s\n' "$1" "$2" >> "$env_app"
  fi
}
declarar_en_env ENVIRONMENT "$ENVIRONMENT"
# El perfil de despliegue (ops/maintenance/perfil-despliegue.sh). Vacio en server.env: no se toca
# lo que diga el .env.
[[ -n "$DEPLOY_PROFILE" ]] && declarar_en_env DEPLOY_PROFILE "$DEPLOY_PROFILE"
chown "$DEPLOY_USER:$DEPLOY_USER" "$env_app"
chmod 0600 "$env_app"

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
  systemctl enable --now core-force-mail-backup.timer core-force-mail-backup-verify.timer >/dev/null

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
# 14) Perfil autoalojado: TLS interno y su renovacion
# ----------------------------------------------------------------------------
# Sin RDS ni ElastiCache, los certificados de Postgres, Redis y mail-auth los emite una CA interna
# del servidor (ops/security/internal-tls.sh, idempotente). Se genera aqui si el guion viajo junto
# a la plantilla (git archive de docker-compose.yml, docker-compose.selfhosted.yml, ops/security y
# ops/server-template) o ya esta
# desplegado; el despliegue se niega a continuar sin la CA. La renovacion es un timer diario.
tls_estado="no aplica (DEPLOY_PROFILE=${DEPLOY_PROFILE:-vacio})"
perfil_env="$(sed -n -E 's/^[[:space:]]*DEPLOY_PROFILE[[:space:]]*=[[:space:]]*//p' "$env_app" | tail -n 1)"
if [[ "$perfil_env" == selfhosted ]]; then
  tls_guion=""
  for candidato in "$SCRIPT_DIR/../security/internal-tls.sh" "$DEPLOY_PATH/ops/security/internal-tls.sh"; do
    [[ -f "$candidato" ]] && { tls_guion="$candidato"; break; }
  done
  if [[ -n "$tls_guion" ]]; then
    log "Generando o revisando el TLS interno"
    # El directorio lo decide el .env del despliegue, que es el que monta el compose.
    tls_dir="$(sed -n -E 's/^[[:space:]]*INTERNAL_TLS_DIR[[:space:]]*=[[:space:]]*//p' "$env_app" | tail -n 1)"
    [[ -n "$tls_dir" ]] && export INTERNAL_TLS_DIR="$tls_dir"
    bash "$tls_guion"
    tls_estado="$(bash "$tls_guion" --comprobar >/dev/null 2>&1 && echo vigente || echo 'revisar con --comprobar')"
  else
    warn "perfil autoalojado sin ops/security/internal-tls.sh a mano: generalo antes del primer despliegue (docs/Operacion_Despliegue.md, 11)"
    tls_estado="pendiente"
  fi
  tls_units=""
  for candidato in "$SCRIPT_DIR/../security/systemd" "$DEPLOY_PATH/ops/security/systemd"; do
    [[ -f "$candidato/core-force-mail-internal-tls.timer" ]] && { tls_units="$candidato"; break; }
  done
  if [[ -n "$tls_units" ]]; then
    install -m 0644 "$tls_units/core-force-mail-internal-tls.service" "$tls_units/core-force-mail-internal-tls.timer" /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable --now core-force-mail-internal-tls.timer >/dev/null
  else
    warn "no se encontraron las unidades de renovacion del TLS interno; vuelve a correr tras el primer despliegue"
  fi
fi

# ----------------------------------------------------------------------------
# Resumen
# ----------------------------------------------------------------------------
log "Aprovisionamiento completado"
cat <<EOF

  Entorno          : ENVIRONMENT=$ENVIRONMENT en $DEPLOY_PATH/.env
  Perfil           : DEPLOY_PROFILE=${perfil_env:-aws (vacio)}
  GRUB serie       : $grub_serie (GRUB_CONSOLA_SERIE=$GRUB_CONSOLA_SERIE)
  TLS interno      : $tls_estado
  Rol del servidor : $SERVER_ROLE
  Hostname         : $(hostnamectl --static 2>/dev/null || hostname)
  Docker           : $(docker --version 2>/dev/null)
  Compose          : $(docker compose version 2>/dev/null | head -n1)
  Usuario deploy   : $DEPLOY_USER  (grupo docker)
  Ruta de deploy   : $DEPLOY_PATH
  SSH              : solo llaves (UFW permite 22), fail2ban activo
  Firewall host    : $(ufw status | head -n1)
  Respaldo         : $(systemctl is-enabled core-force-mail-backup.timer 2>/dev/null || echo 'no instalado')

  Siguientes pasos:
    1) Security Group: 22 solo desde la IP de administracion; cada puerto publico, solo
       desde su origen (80/443 desde el proxy de borde). En un servidor propio no hay
       Security Group: docker-compose.selfhosted.yml publica 80 y 443, y los puertos que
       publica Docker no pasan por UFW (docs/Operacion_Despliegue.md, 11).
    2) Completa $DEPLOY_PATH/.env con la configuracion de .env.example sin copiarlo
       encima (traeria ENVIRONMENT=development) y publica los secretos en el almacen:
       ops/security/secrets/README.md.
    3) Despliega desde tu maquina (el primer despliegue, con la lista de servicios):
       DEPLOY_HOST=<ip> DEPLOY_USER=$DEPLOY_USER DEPLOY_PATH=$DEPLOY_PATH \\
         ./scripts/deploy-ecr.sh <servicios>

EOF
