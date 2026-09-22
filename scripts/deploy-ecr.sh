#!/usr/bin/env bash
# Deploy rapido: construye EN LOCAL (paralelo + cache de BuildKit), publica las
# imagenes etiquetadas por commit y el servidor solo hace
# pull && up (nunca compila). Rollback: DEPLOY_TAG=<sha-anterior> en el server.
#
# Uso:
#   scripts/deploy-ecr.sh                    # detecta servicios cambiados vs .deployed-tag
#   scripts/deploy-ecr.sh svc1 svc2 ...      # servicios explicitos
#   TRANSPORT=save scripts/deploy-ecr.sh ... # sin AWS local: docker save | ssh load
set -euo pipefail

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"; cd "$ROOT"
# El transporte por defecto es SSM, no la IP. El Security Group admite el 22 desde UNA
# sola IP y la de esta PC es dinamica: cuando cambiaba, esto moria con "Connection timed
# out" -que parece del servidor y no lo es- y arreglarlo exigia subir al perfil raiz con
# MFA. Por SSM no hay puerto que autorizar y funciona desde cualquier sitio. El destino, el
# candado y la guardia de retroceso son comunes con scripts/deploy-mail.sh
# (scripts/lib/despliegue.sh).
#
# El 22 sigue abierto como puerta de emergencia. Si SSM fallara:
#   DEPLOY_HOST=<ip-publica-de-la-instancia> scripts/deploy-ecr.sh
# shellcheck source-path=SCRIPTDIR source=lib/despliegue.sh
. "$ROOT/scripts/lib/despliegue.sh"
TRANSPORT="${TRANSPORT:-ecr}"   # ecr | save
TAG="$(git rev-parse --short HEAD)"
ECR_REGION="${ECR_REGION:-us-east-1}"; NS=core-force-mail

remote() { "${SSH[@]}" "cd $DEPLOY_PATH && $*"; }

despliegue_comprobar_conexion || exit 1

# Prometheus monta ops/observability/prometheus como configuracion, pero lee las reglas al
# ARRANCAR o al recibir una recarga: el rsync dejaba la regla nueva en disco y Prometheus
# seguia evaluando las viejas hasta el siguiente reinicio del contenedor, que puede tardar
# semanas. Una alerta que no se carga no falla: no suena. Se recarga tras cada rsync; si
# el contenedor no esta levantado (la observabilidad va aparte), se avisa y no se aborta.
recargar_prometheus() {
  if remote 'P=$(docker ps -q -f name=prometheus | head -1); [ -n "$P" ] && docker kill -s HUP "$P"' >/dev/null 2>&1; then
    echo ">> Prometheus recargado (reglas de alerta al dia)"
  else
    echo ">> aviso: Prometheus no esta levantado; las reglas nuevas se cargaran cuando arranque"
  fi
}

despliegue_comprobar_arbol || exit 1

# Ficheros que viajan al servidor: SIEMPRE desde HEAD (git archive), nunca desde el
# arbol de trabajo. Una migracion nueva sin commitear no puede colarse a produccion.
#
# Lo que no se hornea en ninguna imagen solo llega al servidor por este rsync. ops/ecr y
# ops/security van aqui porque sus scripts se EJECUTAN en el servidor -la limpieza de
# imagenes y el almacen de secretos-: una clave nueva declarada en el repositorio no sirve
# de nada hasta que llega, porque add-secret.sh rechaza las que no estan declaradas alli.
# ops/observability por lo mismo, y con una vuelta de tuerca: Prometheus MONTA
# /opt/core-force-mail/app/ops/observability/prometheus como su configuracion, asi que sus
# reglas de alerta viven en el arbol que sincroniza este rsync. Una alerta que no llega no
# falla: simplemente no suena.
#
# La limpieza es UNA rutina y UN trap: traps sueltos se pisan entre si, y un
# fallo a mitad dejaba el candado puesto o el token de ECR en el servidor.
STAGE_DIR=""
ECR_LOGUEADO=0
limpiar() {
  [[ -n "$STAGE_DIR" ]] && rm -rf "$STAGE_DIR"
  [[ "$ECR_LOGUEADO" == "1" ]] && remote "docker logout $ECR_REGISTRY >/dev/null 2>&1" || true
  liberar_candado
}
trap limpiar EXIT

stage_head_files() {
  STAGE_DIR="$(mktemp -d)"
  # tar.umask=022: git archive escribe 664/775 por defecto y el arbol del servidor no debe ser
  # escribible por el grupo.
  git -c tar.umask=022 archive HEAD -- "$@" | tar -x -C "$STAGE_DIR"
}

# Lo que viaja al servidor, UNA lista para los tres caminos (solo ficheros, ecr y save). Cuando
# divergen, un fichero llega o no segun el transporte que se usara ese dia, que es de las cosas
# mas dificiles de diagnosticar. docker-compose.selfhosted.yml y selfhosted/ son del perfil de
# produccion autoalojada: en un servidor de AWS viajan y no se usan.
FICHEROS_SERVIDOR=(docker-compose.yml docker-compose.images.yml docker-compose.images.save.yml
  docker-compose.observability.yml
  docker-compose.selfhosted.yml selfhosted migrations ops/db ops/security ops/ecr ops/observability
  ops/maintenance ops/backup pgbouncer)

# ── perfil de despliegue ─────────────────────────────────────────────────────
# El .env del servidor declara DEPLOY_PROFILE (ops/maintenance/perfil-despliegue.sh): con
# selfhosted, cada compose lleva docker-compose.selfhosted.yml y el propio despliegue levanta
# Postgres, Redis, PgBouncer y el proxy de borde, que en AWS son servicios gestionados. Se lee
# despues del rsync, que es lo que trae el guion a un servidor nuevo.
COMPOSE_ARGS=""
INFRA=()
TAG_DESPLEGADO=""
leer_perfil() {
  COMPOSE_ARGS="$(remote "ops/maintenance/perfil-despliegue.sh --compose")" || {
    echo "!! el perfil de despliegue del servidor no es valido (detalle arriba)" >&2
    exit 1
  }
  read -r -a INFRA <<<"$(remote "ops/maintenance/perfil-despliegue.sh --infra")"
  [[ ${#INFRA[@]} -gt 0 ]] && echo ">> perfil autoalojado: $COMPOSE_ARGS | infraestructura: ${INFRA[*]}"
  # La red mail-engines y el volumen de certificados son del proyecto de los motores: los crea su
  # despliegue (scripts/deploy-mail.sh). Sin ellos `up` fallaria a mitad de la recreacion.
  remote "ops/security/secrets/with-secrets.sh ops/maintenance/recursos-externos.sh $COMPOSE_ARGS" || {
    echo "!! faltan recursos externos de la plataforma (detalle arriba): despliega antes los motores" >&2
    echo "   con scripts/deploy-mail.sh (docs/Operacion_Despliegue.md, 11, paso 4)" >&2
    exit 1
  }
  return 0
}

# cambio_desplegado <rutas>: cierto si cambiaron desde el commit que corre el servidor (o si no
# se sabe cual es). Los ficheros montados como directorio llegan con el rsync, pero nginx y el
# arranque de Redis solo los leen al crear el contenedor, y Compose no ve cambios de contenido.
cambio_desplegado() {
  [[ -z "$TAG_DESPLEGADO" ]] && return 0
  git rev-parse -q --verify "$TAG_DESPLEGADO^{commit}" >/dev/null || return 0
  ! git diff --quiet "$TAG_DESPLEGADO" HEAD -- "$@"
}

en_infra() { [[ " ${INFRA[*]} " == *" $1 "* ]]; }

# Infraestructura del perfil ANTES de recrear los servicios: sin base, pooler ni Redis no arranca
# ninguno. `up -d` solo recrea lo que cambio en compose; un pg_hba nuevo se aplica con SIGHUP (sin
# cortar conexiones) y un arranque de Redis nuevo obliga a recrearlo.
aplicar_infra() {
  local previos=() s
  for s in "${INFRA[@]}"; do [[ "$s" != edge-proxy ]] && previos+=("$s"); done
  [[ ${#previos[@]} -eq 0 ]] && return 0
  remote "ops/security/secrets/with-secrets.sh docker compose $COMPOSE_ARGS up -d ${previos[*]}"
  if en_infra redis && cambio_desplegado selfhosted/redis; then
    remote "ops/security/secrets/with-secrets.sh docker compose $COMPOSE_ARGS up -d --no-deps --force-recreate redis"
  fi
  if en_infra postgres-primary && cambio_desplegado selfhosted/postgres; then
    remote "docker kill -s HUP \$(docker ps -q --filter label=com.docker.compose.project=app --filter label=com.docker.compose.service=postgres-primary)" >/dev/null
    echo ">> postgres-primary: pg_hba recargado"
  fi
  remote "ops/maintenance/esperar-sanos.sh --proyecto app ${previos[*]}" || {
    echo "!! la infraestructura del perfil no arranco (detalle arriba)" >&2
    exit 1
  }
}

# El proxy de borde DESPUES del gateway, del que depende.
aplicar_borde() {
  en_infra edge-proxy || return 0
  local forzar=""
  cambio_desplegado selfhosted/edge && forzar="--force-recreate"
  remote "ops/security/secrets/with-secrets.sh docker compose $COMPOSE_ARGS up -d --no-deps $forzar edge-proxy"
  remote "ops/maintenance/esperar-sanos.sh --proyecto app edge-proxy" || {
    echo "!! el proxy de borde no arranco (detalle arriba)" >&2
    exit 1
  }
}

despliegue_comprobar_buildkit || exit 1

# El transporte ecr exige credenciales AWS locales (access key IAM). Sin ellas el
# camino correcto sigue siendo compilar aqui: se degrada a save (docker save | ssh load)
# en vez de abortar, porque la alternativa real seria compilar en el servidor, y eso
# compite por CPU con lo que esta sirviendo produccion.
if [[ "$TRANSPORT" == "ecr" ]] && ! aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">> sin credenciales AWS locales (o sesion expirada): se usa save." >&2
  echo ">> para habilitar ecr: ops/aws/setup-iam.sh crea el usuario core-force-mail-deploy-local;" >&2
  echo ">> con su llave, 'aws configure' (region us-east-1) deja credenciales permanentes." >&2
  TRANSPORT=save
fi

# ── 1. servicios a desplegar ─────────────────────────────────────────────────
# cambiados_desde imprime los servicios con cambios entre un commit y HEAD. Memoriza por
# commit: los servidores suelen tener pocos tags distintos vivos y la lista es la misma
# para todos los servicios que corren el mismo.
declare -A CAMBIADOS_CACHE
cambiados_desde() {
  local base="$1"
  if [[ -z "${CAMBIADOS_CACHE[$base]+x}" ]]; then
    CAMBIADOS_CACHE[$base]="$(git diff --name-only "$base"..HEAD | ops/scaffold/service-paths.sh --changed | tr '\n' ' ')"
  fi
  printf '%s' "${CAMBIADOS_CACHE[$base]}"
}

# rezagados encuentra los servicios que se quedaron atras aunque .deployed-tag diga otra cosa.
#
# El archivo .deployed-tag lo escribe TODO despliegue, incluidos los que llevan una lista
# explicita de servicios. Un despliegue de dos servicios avanza el archivo para todos, de
# modo que lo que cambio y no estaba en esa lista se vuelve invisible para las detecciones
# siguientes: nadie lo vuelve a mirar, y no lo delata nada, porque check-image-drift vigila
# RETROCESOS, no rezagos.
#
# Aqui la base de comparacion de cada servicio es el commit que ese contenedor corre de
# verdad. Solo se juzga a los que corren un tag conocido del repositorio: los que estan en
# "latest" o en una imagen local no pasan por ECR y no se pueden comparar.
rezagados() {
  local corriendo="$1" svc tag_vivo
  while read -r svc _ _; do
    [[ -z "$svc" ]] && continue
    tag_vivo="$(awk -v n="app-$svc-1" '$1==n {split($2,a,":"); print a[length(a)]}' <<<"$corriendo")"
    [[ -z "$tag_vivo" || "$tag_vivo" == "latest" || "$tag_vivo" == "$TAG" ]] && continue
    git rev-parse -q --verify "$tag_vivo^{commit}" >/dev/null || continue
    [[ " $(cambiados_desde "$tag_vivo") " == *" $svc "* ]] && echo "$svc"
  done < <(ops/scaffold/service-paths.sh)
}

declare -a SVCS
if [[ $# -gt 0 ]]; then
  SVCS=("$@")
else
  BASE="$(remote 'cat .deployed-tag 2>/dev/null' || true)"
  if [[ -z "$BASE" ]] || ! git rev-parse -q --verify "$BASE" >/dev/null; then
    echo "sin .deployed-tag valido en el server: indica servicios explicitos" >&2; exit 1
  fi
  # El mapa ruta->servicio se deriva del compose (ops/scaffold/service-paths.sh): cubre
  # los servicios Go, la aplicacion web y las raices compartidas (pkg/, go.mod,
  # migrations/) sin listas escritas a mano, de modo que un servicio nuevo entra solo.
  CORRIENDO="$(remote "docker ps --format '{{.Names}} {{.Image}}'" || true)"
  # servicios_sin_commit (scripts/lib/despliegue.sh) anade los que corren una imagen sin etiqueta
  # de commit: es lo que migra un servidor anterior al etiquetado por commit del transporte save.
  mapfile -t TODOS < <(ops/scaffold/service-paths.sh | awk '{print $1}')
  mapfile -t SVCS < <(
    { cambiados_desde "$BASE" | tr ' ' '\n'; rezagados "$CORRIENDO"; servicios_sin_commit app "${TODOS[@]}"; } |
      grep -v '^$' | sort -u
  )
fi
# Sin imagenes que reconstruir puede quedar, aun asi, trabajo para el servidor: lo que
# EJECUTA o MONTA (migraciones, compose, ops/security...) viaja por rsync, no dentro de una
# imagen. El caso se resuelve mas abajo, cuando ya existen el candado y la guarda.
[[ ${#SVCS[@]} -eq 0 ]] && SOLO_FICHEROS=1
if [[ "${SOLO_FICHEROS:-0}" != "1" ]]; then
  echo ">> tag $TAG | servicios (${#SVCS[@]}): ${SVCS[*]}"
fi

# ── 1b. guardia de retroceso (scripts/lib/despliegue.sh) ─────────────────────
# Antes del build para fallar pronto; se repite con el candado en la mano.
guardia_retroceso app "$TAG" "${SVCS[@]}" || exit 1

# El servidor no tiene el codigo fuente: solo levanta lo que el override de imagenes
# referencia. Un servicio ausente del override intentaria compilar alli y el deploy
# quedaria a medias, asi que se verifica antes de construir nada.
make -s check-compose-images

# Sin servicios que reconstruir puede quedar, aun asi, trabajo que llevar al servidor:
# lo que el servidor EJECUTA o MONTA (migraciones, compose, ops/security, ops/ecr...) viaja
# por rsync, no dentro de una imagen. Salir aqui dejaba ese cambio en el repositorio sin que
# nada lo delatara: el despliegue decia "nada que desplegar" y el fichero nuevo -una clave
# declarada en el almacen de secretos, por ejemplo- no llegaba nunca.
if [[ "${SOLO_FICHEROS:-0}" == "1" ]]; then
  if git diff --quiet "$(remote 'cat .deployed-tag 2>/dev/null' || echo HEAD)" HEAD -- "${FICHEROS_SERVIDOR[@]}" 2>/dev/null; then
    echo "nada que desplegar"; exit 0
  fi
  echo ">> tag $TAG | sin imagenes que reconstruir; se sincronizan los ficheros del servidor"
  adquirir_candado plataforma || exit 1
  guardia_retroceso app "$TAG" "${SVCS[@]}" || exit 1
  TAG_DESPLEGADO="$(remote 'cat .deployed-tag 2>/dev/null' || true)"
  stage_head_files "${FICHEROS_SERVIDOR[@]}"
  rsync -a -e "ssh -o IdentitiesOnly=yes -i $SSH_KEY" "$STAGE_DIR"/ "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_PATH/"
  recargar_prometheus
  leer_perfil
  aplicar_infra
  aplicar_borde
  remote "echo $TAG > .deployed-tag"
  echo ">> DEPLOY $TAG COMPLETO (solo ficheros)"
  exit 0
fi

# ── 2. build local en paralelo (BuildKit + cache) ───────────────────────────
# Se construye a traves de Compose y no con "docker build" a mano: cada servicio declara
# su propio context y dockerfile (los Go usan la raiz, la aplicacion web ./web). Compose
# es la unica fuente que los conoce todos.
export DOCKER_BUILDKIT=1
# El proyecto de compose es el mismo que corre el servidor (etiquetas com.docker.compose.*, que
# es por donde se buscan los contenedores). El nombre de la imagen ya no depende de el: lo fija
# el override del transporte (docker-compose.images.yml o docker-compose.images.save.yml).
export COMPOSE_PROJECT_NAME=app

# Compose lanza TODOS los builds a la vez. En un despliegue acotado da igual, pero
# cuando el cambio toca pkg/ la lista son todos los servicios Go y el PC de trabajo se
# queda sin RAM (swap) e inusable. Se construye en lotes: mismo resultado y misma cache,
# con el pico de CPU/RAM acotado. Ajustable con DEPLOY_BUILD_LOTE; los builds Go
# comparten la cache de modulos, asi que el pico no es la suma, pero en una maquina de
# 16 GB conviene bajarlo a 4.
BUILD_LOTE="${DEPLOY_BUILD_LOTE:-12}"
build_en_lotes() {
  local total=${#SVCS[@]} i
  for ((i = 0; i < total; i += BUILD_LOTE)); do
    (( total > BUILD_LOTE )) && echo ">> build lote $((i / BUILD_LOTE + 1))/$(((total + BUILD_LOTE - 1) / BUILD_LOTE)): ${SVCS[*]:i:BUILD_LOTE}"
    "${COMPOSE[@]}" build "${SVCS[@]:i:BUILD_LOTE}"
  done
}

# La RECREACION en el servidor tambien va en lotes, y ahi el pico es de CPU: `up -d` con
# la lista entera arranca todos los contenedores a la vez, y en una instancia pequena un
# pico al tope es una cola de peticiones para quien esta usando la plataforma en ese
# momento. En lotes el trabajo total es el mismo y tarda algo mas; lo que cambia es que
# el pico se reparte. Ajustable con DEPLOY_UP_LOTE.
UP_LOTE="${DEPLOY_UP_LOTE:-20}"
# Antes de recrear: el userlist.txt de PgBouncer presente (sin el, el pooler no arranca) y aviso
# de las claves de .env.example que el .env del servidor no tiene. Solo viajan nombres de claves.
preparar_servidor() {
  remote "ops/security/secrets/with-secrets.sh ops/db/pgbouncer-userlist.sh --ensure"
  sed -n -E 's/^([A-Z][A-Z0-9_]*)=.*/\1/p' .env.example | remote "ops/maintenance/claves-env.sh .env"
}

# Tras recrear: imagen correcta no es arranque correcto. Un servicio que no arranca por
# configuracion queda reiniciandose con el tag nuevo; se espera a que arranque o se falla.
esperar_sanos() {
  remote "ops/maintenance/esperar-sanos.sh --proyecto app ${SVCS[*]}" || {
    echo "!! algun servicio no arranco tras el despliegue (detalle arriba)" >&2
    exit 1
  }
}

por_lotes_remoto() {
  local prefijo="$1"; shift
  local total=${#SVCS[@]} i
  for ((i = 0; i < total; i += UP_LOTE)); do
    (( total > UP_LOTE )) && echo ">> $prefijo lote $((i / UP_LOTE + 1))/$(((total + UP_LOTE - 1) / UP_LOTE))"
    remote "$* ${SVCS[*]:i:UP_LOTE}"
  done
}

if [[ "$TRANSPORT" == "ecr" ]]; then
  ACC="$(aws sts get-caller-identity --query Account --output text)"
  export ECR_REGISTRY="$ACC.dkr.ecr.$ECR_REGION.amazonaws.com" DEPLOY_TAG="$TAG"
  # Con el override activo, Compose etiqueta cada imagen directamente como
  # <registry>/<ns>/<svc>:<tag>: no hay retag manual que se pueda desincronizar.
  COMPOSE=(docker compose -f docker-compose.yml -f docker-compose.images.yml)
  build_en_lotes
  echo ">> build local OK"

  aws ecr get-login-password --region "$ECR_REGION" | docker login --username AWS --password-stdin "$ECR_REGISTRY" >/dev/null
  "${COMPOSE[@]}" push -q "${SVCS[@]}"
  # "latest" es la imagen que resuelve docker-compose.images.yml cuando no se da DEPLOY_TAG.
  for s in "${SVCS[@]}"; do
    docker tag "$ECR_REGISTRY/$NS/$s:$TAG" "$ECR_REGISTRY/$NS/$s:latest"
    docker push -q "$ECR_REGISTRY/$NS/$s:latest"
  done
  echo ">> push ECR OK"

  # server: pull etiquetado + up con override de imagenes. Desde aqui todo va
  # bajo candado: la re-verificacion de retroceso ve el estado REAL tras
  # cualquier despliegue que haya corrido en paralelo durante nuestro build.
  adquirir_candado plataforma || exit 1
  guardia_retroceso app "$TAG" "${SVCS[@]}" || exit 1
  TAG_DESPLEGADO="$(remote 'cat .deployed-tag 2>/dev/null' || true)"
  stage_head_files "${FICHEROS_SERVIDOR[@]}"
  rsync -a -e "ssh -o IdentitiesOnly=yes -i $SSH_KEY" "$STAGE_DIR"/ "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_PATH/"
  recargar_prometheus
  preparar_servidor
  leer_perfil
  # El login de docker contra ECR caduca a las 12 h. El servidor tiene su rol
  # IAM, pero si nadie renueva la sesion el pull falla con un 403 opaco que
  # parece un problema de permisos y no de caducidad. Se renueva en cada
  # despliegue: cuesta un segundo y evita perseguir el error equivocado.
  # docker login deja el token de ECR EN CLARO en ~/.docker/config.json y ahi se
  # queda. Se usa dentro del despliegue y se retira al terminar, de modo que en
  # reposo no hay ninguna credencial de registro guardada en el servidor: el rol
  # IAM de la instancia basta para volver a obtenerla.
  #
  # El logout va con trap para que tambien se ejecute si el pull o el up fallan.
  remote "aws ecr get-login-password --region $ECR_REGION | docker login --username AWS --password-stdin $ECR_REGISTRY >/dev/null"
  ECR_LOGUEADO=1

  # El pull va entero: solo trae bytes y no compite por CPU con lo que esta sirviendo.
  remote "export DEPLOY_TAG=$TAG && ops/security/secrets/with-secrets.sh docker compose $COMPOSE_ARGS -f docker-compose.images.yml pull -q ${SVCS[*]}"
  aplicar_infra
  # La recreacion, en lotes: es la que arranca procesos y dispara la CPU.
  # --no-build: el servidor no tiene el codigo y NUNCA compila; si la imagen no esta, se para aqui.
  por_lotes_remoto "recrear" "export DEPLOY_TAG=$TAG && ops/security/secrets/with-secrets.sh docker compose $COMPOSE_ARGS -f docker-compose.images.yml up -d --no-deps --no-build"

  verificar_imagen_desplegada app "$TAG" "${SVCS[@]}" || exit 1
  esperar_sanos
  aplicar_borde
else
  # Sin AWS local (perfil autoalojado): la imagen la construye el PC y viaja con
  # docker save | ssh docker load. Con el override del transporte save, Compose la etiqueta
  # core-force-mail/<svc>:<commit> aqui y el servidor levanta esa misma etiqueta con --no-build.
  #
  # Antes este camino enviaba app-<svc>:latest: con una etiqueta flotante, ni la guardia de
  # retroceso ni la verificacion de imagen podian saber que commit corria cada servicio (las dos
  # descartan "latest"), asi que en el perfil autoalojado -el de produccion- el despliegue "salia
  # bien" aunque dejara dentro codigo viejo, y no habia rollback por etiqueta.
  export DEPLOY_TAG="$TAG"
  COMPOSE=(docker compose -f docker-compose.yml -f docker-compose.images.save.yml)
  build_en_lotes
  adquirir_candado plataforma || exit 1
  guardia_retroceso app "$TAG" "${SVCS[@]}" || exit 1
  echo ">> build local OK"
  IMAGENES=()
  for s in "${SVCS[@]}"; do IMAGENES+=("$NS/$s:$TAG"); done
  docker save "${IMAGENES[@]}" | gzip | "${SSH[@]}" 'gunzip | docker load' >/dev/null
  "${SSH[@]}" "docker image inspect ${IMAGENES[*]} >/dev/null" || {
    echo "!! el servidor no tiene las imagenes tras el docker load" >&2
    exit 1
  }
  TAG_DESPLEGADO="$(remote 'cat .deployed-tag 2>/dev/null' || true)"
  stage_head_files "${FICHEROS_SERVIDOR[@]}"
  rsync -a -e "ssh -o IdentitiesOnly=yes -i $SSH_KEY" "$STAGE_DIR"/ "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_PATH/"
  recargar_prometheus
  preparar_servidor
  leer_perfil
  aplicar_infra
  por_lotes_remoto "recrear" "export DEPLOY_TAG=$TAG && ops/security/secrets/with-secrets.sh docker compose $COMPOSE_ARGS -f docker-compose.images.save.yml up -d --no-deps --no-build"
  verificar_imagen_desplegada app "$TAG" "${SVCS[@]}" || exit 1
  esperar_sanos
  aplicar_borde
  # Las imagenes etiquetadas por commit tambien se acumulan en el PC que construye (antes era una
  # sola por servicio, app-<svc>:latest, que se sobreescribia). Se conservan las ultimas, como en
  # el servidor, para que el rollback siga siendo un deploy sin build.
  ops/ecr/prune-local-images.sh --apply >/dev/null || echo ">> no se pudo retirar las imagenes viejas de esta maquina" >&2
fi

# Las migraciones de tenant corren al arrancar organization. Si alguna cambio el TIPO de
# una columna, las sentencias preparadas que pgbouncer guarda en sus conexiones al servidor
# siguen con el tipo viejo y ese servicio falla con "cached plan must not change result
# type" hasta una hora despues, aunque se reinicie (el plan no vive en el servicio). Se
# renuevan las conexiones con RECONNECT, que es gradual: no corta ninguna transaccion.
RECONNECT_ARGS=""
for s in "${SVCS[@]}"; do [[ "$s" == "organization" ]] && RECONNECT_ARGS="--wait-migrations"; done
remote "ops/maintenance/pgbouncer-reconnect.sh $RECONNECT_ARGS" || echo ">> aviso: no se pudo renovar las conexiones de pgbouncer; si un servicio falla con 'cached plan', ejecuta ops/maintenance/pgbouncer-reconnect.sh en el servidor" >&2

# ── 4. registrar tag desplegado + sanidad ────────────────────────────────────
# El 'ps' tambien va por el envoltorio. No crea contenedores y seria seguro sin el, pero sin
# secretos Compose avisa de cada ${VAR} vacia: ese ruido al final de cada despliegue es lo que
# hace invisible el aviso que si importa.
remote "echo $TAG > .deployed-tag && ops/security/secrets/with-secrets.sh docker compose ps --format '{{.Name}} {{.Status}}' | grep -E \"$(IFS='|'; echo "${SVCS[*]}")\" | head -20"

# Un servicio que retrocedio de ECR a una imagen compilada en el servidor corre codigo
# posiblemente ANTERIOR al desplegado, sin sintoma visible. Se revisa aqui porque el
# despliegue es el unico momento en que alguien esta mirando; no aborta (la regresion suele
# ser de OTRO servicio y el despliegue en curso si quedo aplicado y verificado arriba).
scripts/check-image-drift.sh || echo ">> revisa la deriva de imagenes antes del proximo cambio"

# Las copias locales de imagenes viejas se acumulan sin que nada las retire. La politica de
# ciclo de vida de ECR (ops/ecr/lifecycle-policy.json) limpia el REGISTRO en AWS, pero no
# sabe nada del disco de la maquina: cada despliegue hace un pull y esa copia se queda para
# siempre. Sin esto el disco se llena, y cuando se llena no falla el despliegue: falla
# Postgres, que es mucho peor.
#
# Se limpia aqui, que es donde se generan, y no en un cron: asi la limpieza va atada al acto
# que la hace necesaria y no hay que acordarse de nada. Conserva las 3 ultimas de cada
# servicio para que el retroceso con DEPLOY_TAG=<sha-anterior> siga siendo posible.
#
# No aborta si falla: lo desplegado ya quedo verificado arriba, y quedarse sin limpiar una
# vez es molesto, no grave.
remote "ops/ecr/prune-local-images.sh --apply" || echo ">> no se pudo retirar las imagenes viejas del servidor"

# Los parches de kernel y de libc se instalan solos (unattended-upgrades) pero no protegen
# hasta reiniciar, y sin este aviso nada lo dice. El despliegue es el momento en que
# alguien mira la consola, asi que se avisa aqui. Solo informa: reiniciar es una ventana
# aparte.
if remote "test -f /var/run/reboot-required" 2>/dev/null; then
  echo ">> AVISO: el servidor tiene parches instalados que exigen REINICIO ($(remote "sort -u /var/run/reboot-required.pkgs 2>/dev/null | tr '\n' ' '"))" >&2
  echo ">>        no protegen hasta reiniciar; planifica una ventana de reinicio" >&2
fi

echo ">> DEPLOY $TAG COMPLETO"
