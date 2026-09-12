#!/usr/bin/env bash
# Carga los secretos de produccion en el entorno del proceso que lo invoca.
#
#   . ops/security/secrets/load.sh
#
# Es la UNICA forma sancionada de obtener una credencial de arranque fuera de un contenedor.
# Antes cada script se la buscaba por su cuenta leyendo el .env, y el dia que el .env dejo de
# tener credenciales -al pasar al almacen- esos scripts no fallaron con un mensaje claro: se
# quedaron con la variable vacia. El respaldo nocturno habria abortado por "faltan
# credenciales" sin que nadie relacionara la causa.
#
# Se puede invocar de dos maneras:
#   - Sourceado (lo normal en un script que necesita las credenciales para si mismo).
#   - Via with-secrets.sh, que lo sourcea y ejecuta un comando (lo usan los despliegues,
#     porque Compose necesita las variables en el ENTORNO para interpolar ${VAR}).
#
# Respaldo: si el almacen no esta disponible se continua con lo que haya en el .env, pero
# solo MIENTRAS el .env conserve las credenciales obligatorias. Una vez migrado ya no las
# tiene, y continuar significaria trabajar sin credencial y en silencio: ahi se aborta. La
# decision se toma mirando el .env, no con un interruptor que alguien deba acordarse de
# cambiar.
#
# No lleva `set -e`: se sourcea desde scripts con sus propias opciones y cambiarselas por
# debajo seria un efecto secundario invisible.

# El PATH no se hereda igual en todas partes: una sesion interactiva trae /usr/local/bin,
# pero un servicio de systemd arranca con un PATH minimo y el de cron depende de la
# configuracion de la maquina. El CLI de AWS vive en /usr/local/bin, asi que sin esto la
# materializacion de secretos funciona a mano y falla en el trabajo programado -o al reves,
# segun el servidor-. Se anaden solo los que falten.
for _cf_bin in /usr/local/bin /usr/local/sbin /snap/bin; do
  case ":$PATH:" in
    *":$_cf_bin:"*) ;;
    *) [[ -d "$_cf_bin" ]] && PATH="$PATH:$_cf_bin" ;;
  esac
done
unset _cf_bin
export PATH

_cf_secrets_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_cf_secrets_file="${SECRETS_ENV_FILE:-/dev/shm/core-force-mail/secrets.env}"
_cf_keys_file="${SECRET_KEYS_FILE:-$_cf_secrets_dir/secret-keys.txt}"
_cf_env_file="${WITH_SECRETS_ENV_FILE:-${APP_DIR:-.}/.env}"

# cf_env_tiene_credenciales: cierto si el .env conserva TODAS las claves obligatorias con
# valor. Es lo que distingue "el almacen aun no esta puesto" de "el almacen es la fuente y
# no responde".
cf_env_tiene_credenciales() {
  KEYS_FILE="$_cf_keys_file" ENV_FILE="$_cf_env_file" python3 - <<'PY'
import os, re, sys

keys = [l.strip() for l in open(os.environ["KEYS_FILE"], encoding="utf-8")
        if l.strip() and not l.startswith("#")]
obligatorias = [k for k in keys if not k.endswith("?")]
try:
    env = open(os.environ["ENV_FILE"], encoding="utf-8").read()
except OSError:
    sys.exit(1)
tiene = {m.group(1) for m in re.finditer(r"^([A-Z0-9_]+)=(.+)$", env, re.M) if m.group(2).strip()}
sys.exit(0 if all(k in tiene for k in obligatorias) else 1)
PY
}

cf_cargar_secretos() {
  if ! "$_cf_secrets_dir/fetch-secrets.sh"; then
    if cf_env_tiene_credenciales; then
      echo "secretos: almacen no disponible; se continua con las credenciales del .env" >&2
      return 0
    fi
    echo "secretos: no se pudieron materializar y el .env tampoco los tiene." >&2
    echo "  Continuar significaria trabajar SIN credenciales. Se aborta." >&2
    return 1
  fi

  if [[ -f "$_cf_secrets_file" ]]; then
    set -a
    # shellcheck disable=SC1090
    . "$_cf_secrets_file"
    set +a
  fi
  return 0
}

cf_cargar_secretos
