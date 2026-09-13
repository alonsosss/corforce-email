#!/usr/bin/env bash
# Deriva de imagenes en produccion: un servicio que ESTABA desplegado desde ECR y pasa a
# correr una imagen compilada en el servidor.
#
# El despliegue bueno (scripts/deploy-ecr.sh) compila en el PC desde HEAD y el servidor
# solo hace pull. Cualquier `docker compose build/up --build` ejecutado EN el servidor
# compila con la copia de /opt/core-force-mail/app, que llega por rsync selectivo y puede estar
# atrasada: el contenedor queda sano, el deploy "sale bien" y dentro corre codigo ANTERIOR
# al ya desplegado. Es la unica forma en que la plataforma puede retroceder de version en
# bloque sin que nadie lo note, y por tanto la unica que puede devolver un diseno viejo a todos.
#
# Lo que se vigila NO es "la imagen no viene de ECR": muchos servicios nunca se han vuelto
# a desplegar desde que existe el registro y corren su imagen local sin ningun problema.
# Marcarlos a todos seria ruido y el aviso que importa quedaria enterrado. Lo que se vigila
# es la TRANSICION ecr -> local, que solo puede producirla una compilacion en el servidor.
# Por eso el estado observado se guarda en el propio servidor ($STATE_FILE) y cada pasada
# se compara con la anterior; la primera pasada solo toma la foto.
#
# Los servicios los da docker-compose.images.yml (el override de despliegue, cubierto por
# `make check-compose-images`), asi que un servicio nuevo entra solo.
#
# Uso:
#   scripts/check-image-drift.sh          # sale 1 si hubo regresion (apto para cron/CI)
set -euo pipefail

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"; cd "$ROOT"
# Mismo destino por defecto que scripts/deploy-ecr.sh: el alias SSM que instala
# ops/aws/setup-ssm-local.sh.
DEPLOY_HOST="${DEPLOY_HOST:-core-force-mail-ssm}"; DEPLOY_USER="${DEPLOY_USER:-deploy}"
DEPLOY_PATH="${DEPLOY_PATH:-/opt/core-force-mail/app}"
SSH_KEY="${DEPLOY_SSH_KEY:-$HOME/.ssh/core-force-mail-prod.pem}"
SSH=(ssh -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i "$SSH_KEY" "$DEPLOY_USER@$DEPLOY_HOST")
STATE_FILE=".image-sources"

# Transporte. Desde una IP autorizada se entra por SSH; en CI no, porque el Security Group
# solo admite el 22 desde la IP del administrador. Con DEPLOY_SSM_INSTANCE definido se va
# por SSM, que no necesita ningun puerto de entrada abierto. Ambos leen el script de la
# entrada estandar, asi que los sitios que los usan no cambian.
if [[ -n "${DEPLOY_SSM_INSTANCE:-}" ]]; then
  remoto() { "$ROOT/ops/aws/ssm-exec.sh"; }
else
  remoto() { "${SSH[@]}" 'bash -s'; }
fi

mapfile -t SVCS < <(grep -E '^  [a-z0-9-]+:$' docker-compose.images.yml | tr -d ' :')
[[ ${#SVCS[@]} -eq 0 ]] && { echo "no se pudo leer la lista de servicios de docker-compose.images.yml" >&2; exit 2; }

# Una sola sesion para todos los servicios: lee el estado anterior y la imagen actual.
OUT="$(remoto <<REMOTO
cd $DEPLOY_PATH
cat $STATE_FILE 2>/dev/null | sed 's/^/PREV /'
for s in ${SVCS[*]}; do
  # Se busca por la ETIQUETA de compose y no por el nombre del contenedor. Docker renombra
  # el contenedor a "<id>_<nombre>" cuando una recreacion se cruza consigo misma, y con el
  # nombre roto este chequeo no encontraria nada: daria esos servicios por "locales" y
  # seguiria diciendo OK, es decir, dejaria de vigilar justo lo que existe para vigilar.
  img=\$(docker ps -a --filter "label=com.docker.compose.service=\$s" --filter "label=com.docker.compose.project=app" --format '{{.Image}}' 2>/dev/null | head -1)
  [ -z "\$img" ] && img='-'
  echo "NOW \$s \$img"
done
REMOTO
)"

declare -A PREV=() NOW=() TAG=()
while read -r kind svc img; do
  case "$kind" in
    PREV) PREV["$svc"]="$img" ;;
    NOW)
      TAG["$svc"]="${img##*:}"
      case "$img" in
        -) NOW["$svc"]=absent ;;
        *.dkr.ecr.*.amazonaws.com/core-force-mail/*) NOW["$svc"]=ecr ;;
        *) NOW["$svc"]=local ;;
      esac
      ;;
  esac
done <<< "$OUT"

REGRESSED=(); LOCAL=(); ABSENT=()
for s in "${SVCS[@]}"; do
  case "${NOW[$s]:-absent}" in
    ecr) ;;
    local)
      LOCAL+=("$s")
      [[ "${PREV[$s]:-}" == "ecr" ]] && REGRESSED+=("$s")
      ;;
    absent) ABSENT+=("$s") ;;
  esac
done

# La foto nueva se guarda siempre, tambien cuando hay regresion: el aviso es para actuar
# ahora, no para repetirse en cada pasada hasta que alguien lo silencie.
# El contenido viaja DENTRO del script, no por la entrada estandar de un `cat` remoto:
# SSM entrega un comando, no una tuberia, y asi el mismo codigo sirve para los dos
# transportes. El delimitador va entrecomillado para que el shell remoto no expanda nada.
{ echo "cat > $DEPLOY_PATH/$STATE_FILE <<'FOTO'"
  for s in "${SVCS[@]}"; do echo "$s ${NOW[$s]:-absent}"; done
  echo "FOTO"
} | remoto

[[ ${#ABSENT[@]} -gt 0 ]] && echo "  AVISO: sin contenedor: ${ABSENT[*]}"

# ── Rezagados ────────────────────────────────────────────────────────────────
# Distinto del retroceso: aqui la imagen si viene de ECR, pero es de un commit anterior y
# desde entonces cambio codigo suyo. Pasa porque .deployed-tag lo escribe TODO despliegue,
# tambien los que llevan lista explicita de servicios: el archivo avanza para todos
# aunque solo se hayan desplegado dos, y lo que cambio y no estaba en la lista deja de
# aparecer en las detecciones siguientes. Es un aviso, no un fallo: puede ser deliberado.
REZAGADOS=()
declare -A CAMBIADOS=()
for s in "${SVCS[@]}"; do
  [[ "${NOW[$s]:-absent}" == "ecr" ]] || continue
  t="${TAG[$s]:-}"
  [[ -z "$t" || "$t" == "latest" ]] && continue
  git rev-parse -q --verify "$t^{commit}" >/dev/null 2>&1 || continue
  git merge-base --is-ancestor "$t" HEAD 2>/dev/null || continue
  if [[ -z "${CAMBIADOS[$t]+x}" ]]; then
    CAMBIADOS[$t]="$(git diff --name-only "$t"..HEAD | ops/scaffold/service-paths.sh --changed | tr '\n' ' ')"
  fi
  [[ " ${CAMBIADOS[$t]} " == *" $s "* ]] && REZAGADOS+=("$s")
done

if [[ ${#REZAGADOS[@]} -gt 0 ]]; then
  echo "  AVISO: servicios que corren un commit anterior con cambios propios sin desplegar:"
  printf '    %s\n' "${REZAGADOS[@]}"
  echo "    Ponlos al dia con: scripts/deploy-ecr.sh ${REZAGADOS[*]}"
fi

if [[ ${#REGRESSED[@]} -gt 0 ]]; then
  echo "check-image-drift: FALLA - servicios que RETROCEDIERON de ECR a imagen local:"
  printf '  %s\n' "${REGRESSED[@]}"
  echo
  echo "  Se compilaron en el servidor con su copia del codigo, que va por rsync selectivo:"
  echo "  pueden estar corriendo una version ANTERIOR a la desplegada. Repon cada uno con:"
  echo "    scripts/deploy-ecr.sh ${REGRESSED[*]}"
  exit 1
fi

echo "check-image-drift: OK (sin regresiones; ${#LOCAL[@]} de ${#SVCS[@]} servicios aun no migrados a ECR)"
