#!/bin/bash
# Genera las contrasenas del controller de Rspamd (override.d/worker-controller-password.inc) a partir
# del almacen de secretos, en cada arranque. Es el unico sitio del que salen: el fichero no se edita a
# mano y lo que otro deje en el (la ruta worker_password de dockerapi, heredada de mailcow) dura hasta
# el siguiente arranque.
#
#   RSPAMD_CONTROLLER_PASSWORD         lectura: estadisticas e historial (la pantalla Antispam)
#   RSPAMD_CONTROLLER_ENABLE_PASSWORD  escritura: learnspam y learnham (el aprendizaje desde la cuarentena)
#
# Comprobado con Rspamd 4.1.4: si falta enable_password, la contrasena de lectura tambien autoriza las
# ordenes de escritura. Por eso, sin contrasena de escritura se pone una aleatoria que nadie conoce y
# nunca se guarda: dar lectura no puede dar aprendizaje. Sin ninguna de las dos, el controller queda
# cerrado fuera del bucle local (secure_ip).
#
# En el fichero solo quedan hashes (rspamadm pw). rspamadm recibe la contrasena por argumento porque no
# la lee de la entrada sin terminal; eso no la expone mas que el entorno del propio contenedor, donde ya
# esta. Nunca se imprime.
#
#   CONTROLLER_PASSWORD_FILE  destino (por defecto el de Rspamd; las pruebas lo cambian)
#   RSPAMADM                  binario de rspamadm (por defecto rspamadm)
set -euo pipefail

destino="${CONTROLLER_PASSWORD_FILE:-/etc/rspamd/override.d/worker-controller-password.inc}"
rspamadm="${RSPAMADM:-rspamadm}"
lectura="${RSPAMD_CONTROLLER_PASSWORD:-}"
escritura="${RSPAMD_CONTROLLER_ENABLE_PASSWORD:-}"

hash_de() {
  local h
  h="$("$rspamadm" pw -e -p "$1" | tail -n 1)"
  # Un hash de Rspamd empieza por $<tipo>$ y no lleva comillas ni espacios: cualquier otra cosa
  # romperia el fichero de configuracion o, peor, lo dejaria sin contrasena.
  if [[ ! "$h" =~ ^\$[0-9]+\$[A-Za-z0-9\$]+$ ]]; then
    echo "controller-password: rspamadm no devolvio un hash valido" >&2
    return 1
  fi
  printf '%s' "$h"
}

if [[ -n "$lectura" && "$lectura" == "$escritura" ]]; then
  echo "controller-password: la contrasena de lectura y la de escritura son la misma: la lectura escribiria" >&2
  exit 1
fi

if [[ -z "$escritura" ]]; then
  escritura="$(head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  aprendizaje="desactivado (contrasena de escritura aleatoria y descartada)"
else
  aprendizaje="activado"
fi

# Los hashes antes de escribir: un fallo dentro de "$(...)" en un echo no detiene el guion y dejaria
# un fichero sin la linea que falto.
hash_lectura=""
[[ -n "$lectura" ]] && hash_lectura="$(hash_de "$lectura")"
hash_escritura="$(hash_de "$escritura")"

temporal="$(mktemp "${destino}.XXXXXX")"
trap 'rm -f "$temporal"' EXIT
{
  echo "# Generado por controller-password.sh en cada arranque desde el almacen de secretos: no editar."
  [[ -n "$hash_lectura" ]] && echo "password = \"$hash_lectura\";"
  echo "enable_password = \"$hash_escritura\";"
} >"$temporal"
chmod 0640 "$temporal"
chown root:_rspamd "$temporal" 2>/dev/null || true
mv -f "$temporal" "$destino"
trap - EXIT

if [[ -n "$lectura" ]]; then
  echo "controller-password: lectura activada; aprendizaje $aprendizaje"
else
  echo "controller-password: lectura desactivada (falta RSPAMD_CONTROLLER_PASSWORD); aprendizaje $aprendizaje"
fi
