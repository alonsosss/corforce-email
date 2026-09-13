#!/usr/bin/env bash
# Deja esta PC lista para desplegar por SSM en vez de por el puerto 22.
#
# POR QUE. El Security Group admite el 22 desde UNA sola IP y la de una PC domestica es
# dinamica. Cuando cambia, el despliegue muere con "Connection timed out" -que parece un
# problema del servidor y no lo es- y arreglarlo exige subir al perfil raiz con MFA para
# reescribir la regla. Ha pasado varias veces.
#
# Por SSM no hay puerto que autorizar: el agente de la instancia abre la conexion HACIA
# AWS. El acceso deja de depender de donde estes sentado y pasa a depender de IAM, que se
# revoca al instante y queda registrado en CloudTrail.
#
# QUE NO HACE. No cierra el puerto 22. Sigue abierto a proposito como puerta de
# emergencia: si el agente de SSM muriera y este fuera el unico camino, la unica salida
# seria parar la instancia y montar su disco en otra. Para usarla:
#
#   DEPLOY_HOST=<ip-publica-de-la-instancia> scripts/deploy-ecr.sh
#
# Uso: CF_SSM_INSTANCE=<id-de-instancia> ops/aws/setup-ssm-local.sh
# (el id lo imprime ops/aws/setup-github-deploy.sh).
#
# No necesita sudo: el plugin se instala en ~/.local/bin.
#
# Requiere que la credencial de AWS de esta PC tenga la politica core-force-ssm-terminal
# (la declara ops/aws/setup-iam.sh, que se corre con un administrador de la cuenta).
set -euo pipefail

ALIAS="${CF_SSM_ALIAS:-core-force-mail-ssm}"
INSTANCIA="${CF_SSM_INSTANCE:?define CF_SSM_INSTANCE con el id de la instancia de produccion}"
REGION="${AWS_REGION:-us-east-1}"
LLAVE="${DEPLOY_SSH_KEY:-$HOME/.ssh/core-force-mail-prod.pem}"
USUARIO="${DEPLOY_USER:-deploy}"
CONFIG="$HOME/.ssh/config"

log() { printf '  %s\n' "$*"; }

# ── 1) El plugin de sesion ───────────────────────────────────────────────────
# El AWS CLI lo invoca por nombre desde el PATH. Se instala en ~/.local/bin y no en
# /usr/local: asi no hace falta sudo, que en una PC de trabajo es una barrera real.
echo "Plugin de Session Manager"
if command -v session-manager-plugin >/dev/null 2>&1; then
  log "ya instalado ($(session-manager-plugin --version 2>/dev/null || echo version desconocida))"
else
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  url="https://s3.amazonaws.com/session-manager-downloads/plugin/latest/ubuntu_64bit/session-manager-plugin.deb"
  log "descargando"
  curl -sSL -o "$tmp/smp.deb" "$url"
  (cd "$tmp" && ar x smp.deb && { tar -xf data.tar.gz 2>/dev/null || tar -xf data.tar.xz; })
  binario="$(find "$tmp" -name session-manager-plugin -type f | head -1)"
  [[ -n "$binario" ]] || { echo "no se encontro el binario dentro del paquete" >&2; exit 1; }
  mkdir -p "$HOME/.local/bin"
  install -m 0755 "$binario" "$HOME/.local/bin/session-manager-plugin"
  log "instalado en ~/.local/bin"
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) ;;
    *) log "AVISO: ~/.local/bin no esta en el PATH; el AWS CLI no encontrara el plugin" ;;
  esac
fi

# ── 2) El alias de SSH ───────────────────────────────────────────────────────
# Con esto `ssh`, `scp` y `rsync` siguen funcionando igual: lo unico que cambia es el
# transporte. El script de despliegue no necesita ni una linea distinta.
echo "Alias SSH '$ALIAS'"
mkdir -p "$HOME/.ssh"; touch "$CONFIG"; chmod 600 "$CONFIG"
if grep -qE "^Host .*\b${ALIAS}\b" "$CONFIG"; then
  log "ya existe en $CONFIG (no se toca)"
else
  cp -a "$CONFIG" "$CONFIG.bak.$(date +%Y%m%d%H%M%S)"
  cat >> "$CONFIG" <<EOF

# Produccion por SSM. Camino DIARIO del despliegue; el 22 queda como emergencia:
#   DEPLOY_HOST=<ip-publica-de-la-instancia> scripts/deploy-ecr.sh
# Lo instala ops/aws/setup-ssm-local.sh
Host $ALIAS
    HostName $INSTANCIA
    User $USUARIO
    IdentityFile $LLAVE
    IdentitiesOnly yes
    ServerAliveInterval 60
    ServerAliveCountMax 3
    ProxyCommand sh -c "aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=%p --region $REGION"
EOF
  log "anadido a $CONFIG (respaldo del anterior guardado)"
fi

# ── 3) Comprobarlo de verdad ─────────────────────────────────────────────────
# Un alias que no se ha probado no sirve: el fallo aparecerria en mitad de un despliegue.
echo "Comprobacion"
if ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=45 "$ALIAS" 'true' 2>/dev/null; then
  log "conexion por SSM OK"
else
  echo "  FALLA: no se pudo conectar por SSM." >&2
  echo "  Comprueba, en este orden:" >&2
  echo "    - que la credencial tenga la politica core-force-ssm-terminal (ops/aws/setup-iam.sh)" >&2
  echo "    - que el agente responda: aws ssm describe-instance-information --region $REGION" >&2
  echo "  Mientras tanto el puerto 22 sigue abierto: DEPLOY_HOST=<ip-publica-de-la-instancia> scripts/deploy-ecr.sh" >&2
  exit 1
fi

echo
echo "Listo. El despliegue ya va por SSM sin abrir ningun puerto."
