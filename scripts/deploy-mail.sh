#!/usr/bin/env bash
# Despliegue de los motores de correo (deploy/mail, proyecto compose mail): construye EN LOCAL,
# etiqueta cada motor por commit, lo lleva al servidor con docker save | ssh docker load y alli
# solo se levanta con --no-build, un motor detras de otro y esperando a que cada uno arranque.
#
# Uso:
#   scripts/deploy-mail.sh                           # motores con cambios desde el commit que corre cada uno
#   scripts/deploy-mail.sh dovecot-mail rspamd-mail  # motores explicitos
#   DEPLOY_PLAN=1 scripts/deploy-mail.sh             # solo dice que commit corre cada motor y cuales
#                                                    # desplegaria: no construye ni escribe nada
#
# Destino y candado: los de scripts/deploy-ecr.sh (scripts/lib/despliegue.sh), con DEPLOY_HOST,
# DEPLOY_USER, DEPLOY_SSH_KEY y DEPLOY_PATH (donde esta el .env del servidor). Ademas:
#   MAIL_DEPLOY_PATH     copia de deploy/mail que montan los contenedores (/opt/core-force-mail/mail-src)
#   MAIL_PROJECT         proyecto compose de los motores (mail; fija el nombre de sus volumenes,
#                        MAIL_SSL_VOLUME=<proyecto>_ssl-vol en la plataforma)
#   MAIL_BUILD_LOTE      motores construidos a la vez (3)
#   MAIL_DEPLOY_PLAZO    segundos para que un motor arranque (900: ClamAV carga firmas varios minutos)
#   MAIL_DEPLOY_ESTABLE  segundos corriendo sin reiniciarse para un motor sin chequeo de salud (30)
#   MAIL_DEPLOY_REGRESION  exigir (por defecto: make e2e-mail en verde para lo que se despliega, segun el
#                        flujo mail-engines.yml de GitHub, con gh) | avisar | omitir (con razon)
#   DEPLOY_ALLOW_DIRTY=1, DEPLOY_ALLOW_ROLLBACK=1  como en deploy-ecr.sh
#
# Rollback: desde el commit anterior, DEPLOY_ALLOW_ROLLBACK=1 scripts/deploy-mail.sh <motores>; si
# el servidor aun tiene esa imagen (prune-local-images conserva las tres ultimas) no se reconstruye.
set -euo pipefail

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"; cd "$ROOT"
# shellcheck source-path=SCRIPTDIR source=lib/despliegue.sh
. "$ROOT/scripts/lib/despliegue.sh"

MAIL_DEPLOY_PATH="${MAIL_DEPLOY_PATH:-/opt/core-force-mail/mail-src}"
MAIL_PROJECT="${MAIL_PROJECT:-mail}"
MAIL_BUILD_LOTE="${MAIL_BUILD_LOTE:-3}"
MAIL_DEPLOY_PLAZO="${MAIL_DEPLOY_PLAZO:-900}"
MAIL_DEPLOY_ESTABLE="${MAIL_DEPLOY_ESTABLE:-30}"
TAG="$(git rev-parse --short HEAD)"

# Los motores no tienen repositorios en ECR (ops/ecr/create-repos.sh los deriva de
# docker-compose.yml) y el servidor que los corre es autoalojado: el unico transporte es save.
if [[ "${TRANSPORT:-save}" != save ]]; then
  echo "deploy-mail: TRANSPORT=${TRANSPORT} no soportado; los motores solo viajan con save" >&2
  exit 2
fi

MAIL_DIR=deploy/mail
COMPOSE_MOTORES="$MAIL_DIR/docker-compose.mail.yml"
COMPOSE_IMAGENES="$MAIL_DIR/docker-compose.mail.images.yml"
# Lo que el servidor monta o ejecuta para los motores, desde HEAD. Lleva su propia copia del
# envoltorio de secretos y de la espera: los motores se despliegan antes que la plataforma en un
# servidor nuevo, y no deben depender de la version de ops/ que dejo el ultimo despliegue de esta.
FICHEROS_MOTORES=("$MAIL_DIR" ops/security/secrets ops/maintenance/esperar-sanos.sh ops/ecr/prune-local-images.sh)

ENV_LOCAL="$(mktemp)"
limpiar() {
  rm -f "$ENV_LOCAL"
  liberar_candado
}
trap limpiar EXIT

remote_mail() { "${SSH[@]}" "cd $MAIL_DEPLOY_PATH && $*"; }

# En local se interpola contra un .env de claves vacias: construir no necesita ningun valor del
# servidor, asi ningun secreto local entra en la configuracion, y declararlas calla el aviso de
# variable sin definir (las que llevan valor por defecto con :- lo conservan).
grep -ohE '\$\{[A-Z][A-Z0-9_]*' "$COMPOSE_MOTORES" "$COMPOSE_IMAGENES" | sed 's/^\${//; s/$/=/' | sort -u | grep -v '^MAIL_DEPLOY_TAG=$' >"$ENV_LOCAL"
compose_local() {
  MAIL_DEPLOY_TAG="$TAG" docker compose -p "$MAIL_PROJECT" --env-file "$ENV_LOCAL" \
    -f "$COMPOSE_MOTORES" -f "$COMPOSE_IMAGENES" "$@"
}

DEPLOY_PLAN="${DEPLOY_PLAN:-0}"
despliegue_comprobar_conexion || exit 1
if [[ "$DEPLOY_PLAN" != 1 ]]; then
  despliegue_comprobar_arbol || exit 1
  despliegue_comprobar_buildkit || exit 1
fi

# ── modelo de los motores, desde el propio compose ──────────────────────────
# Una linea por servicio en orden de arranque (dependencias antes):
#   nombre  construido(0|1)  solo-servidor(0|1)  rutas que lo afectan (con comas)  imagen declarada
# Las rutas son su contexto de build, lo que monta del repositorio y los dos ficheros de compose:
# un cambio en cualquiera obliga a recrearlo, porque lo montado solo se lee al arrancar.
CONFIG_ERR="$(mktemp)"
if ! CONFIG="$(compose_local config --format json 2>"$CONFIG_ERR")"; then
  cat "$CONFIG_ERR" >&2; rm -f "$CONFIG_ERR"
  echo "deploy-mail: docker compose no pudo leer $COMPOSE_MOTORES" >&2
  exit 1
fi
rm -f "$CONFIG_ERR"
MODELO="$(ROOT="$ROOT" FIJOS="$COMPOSE_MOTORES,$COMPOSE_IMAGENES" python3 -c '
import json, os, sys
root = os.environ["ROOT"]
d = json.load(sys.stdin)
servicios = d.get("services") or {}

def rel(ruta):
    r = os.path.relpath(ruta, root)
    return None if r.startswith("..") else r

orden, visitados = [], set()
def visitar(n, pila=()):
    if n in visitados:
        return
    if n in pila:
        sys.exit("deploy-mail: dependencias circulares en " + n)
    for dep in sorted((servicios[n].get("depends_on") or {})):
        if dep in servicios:
            visitar(dep, pila + (n,))
    visitados.add(n)
    orden.append(n)
for n in sorted(servicios):
    visitar(n)

for n in orden:
    s = servicios[n]
    rutas = os.environ["FIJOS"].split(",")
    if s.get("build"):
        rutas.append(rel(s["build"]["context"]))
    for v in s.get("volumes") or []:
        if v.get("type") == "bind":
            rutas.append(rel(v["source"]))
    solo = (s.get("labels") or {}).get("core-force-mail.solo-servidor") == "true"
    rutas = sorted({r for r in rutas if r})
    print("\t".join([n, "1" if s.get("build") else "0", "1" if solo else "0", ",".join(rutas), s.get("image") or ""]))
' <<<"$CONFIG")"

declare -A CONSTRUIDO SOLO_SERVIDOR RUTAS IMAGEN
ORDEN=()
while IFS=$'\t' read -r nombre construido solo rutas imagen; do
  [[ -z "$nombre" ]] && continue
  ORDEN+=("$nombre"); CONSTRUIDO[$nombre]=$construido; SOLO_SERVIDOR[$nombre]=$solo
  RUTAS[$nombre]=$rutas; IMAGEN[$nombre]=$imagen
done <<<"$MODELO"

# Imagen de utileria para normalizar duenos en el servidor: la de un motor que NO se construye
# (hoy redis, fijada por version en el compose). Asi no se fija aqui ninguna imagen ni se baja al
# servidor nada que los motores no necesiten ya.
IMAGEN_UTIL=""
for m in "${ORDEN[@]}"; do
  if [[ "${CONSTRUIDO[$m]}" == 0 && -n "${IMAGEN[$m]}" ]]; then IMAGEN_UTIL="${IMAGEN[$m]}"; break; fi
done
[[ -n "$IMAGEN_UTIL" ]] || { echo "deploy-mail: ningun motor declara una imagen fijada; sin ella no se puede normalizar el arbol del servidor" >&2; exit 1; }

# rutas_de <motor>: sus rutas, una por linea.
rutas_de() { tr ',' '\n' <<<"${RUTAS[$1]}"; }

# ── estado del servidor ─────────────────────────────────────────────────────
# .deployed-tags guarda el commit desplegado de CADA motor ("motor commit"): un despliegue parcial
# no puede adelantar la marca de los que no toco, que es lo que esconderia sus cambios pendientes.
declare -A BASE
while read -r motor commit; do
  [[ -n "${motor:-}" && -n "${commit:-}" ]] && BASE[$motor]=$commit
done < <(remote_mail "cat .deployed-tags" 2>/dev/null || true)
declare -A VIVO
while read -r motor imagen; do
  [[ -n "${motor:-}" ]] && VIVO[$motor]=$imagen
done < <("${SSH[@]}" "docker ps -a --filter label=com.docker.compose.project=$MAIL_PROJECT --format '{{.Label \"com.docker.compose.service\"}} {{.Image}}'" || true)

# base_de <motor>: el commit que corre, si se sabe.
base_de() {
  local b="${BASE[$1]:-}" img="${VIVO[$1]:-}"
  if [[ -n "$b" ]] && es_commit "$b"; then echo "$b"; return; fi
  b="${img##*:}"
  [[ "$img" == *:* ]] && es_commit "$b" && echo "$b"
  return 0
}

cambio_desde() {
  local base="$1" motor="$2" rutas
  mapfile -t rutas < <(rutas_de "$motor")
  ! git diff --quiet "$base" HEAD -- "${rutas[@]}"
}

# ── 1. motores a desplegar ──────────────────────────────────────────────────
SEL=()
if [[ $# -gt 0 ]]; then
  declare -A PEDIDO
  for m in "$@"; do
    [[ -n "${CONSTRUIDO[$m]+x}" ]] || { echo "deploy-mail: '$m' no es un servicio de $COMPOSE_MOTORES (${ORDEN[*]})" >&2; exit 2; }
    PEDIDO[$m]=1
  done
  for m in "${ORDEN[@]}"; do [[ -n "${PEDIDO[$m]:-}" ]] && SEL+=("$m"); done
else
  desconocidos=()
  for m in "${ORDEN[@]}"; do
    # Un motor que nunca se levanto en el servidor no entra solo: se pide por nombre.
    [[ -n "${VIVO[$m]:-}" ]] || continue
    base="$(base_de "$m")"
    if [[ -z "$base" ]]; then desconocidos+=("$m"); continue; fi
    cambio_desde "$base" "$m" && SEL+=("$m")
  done
  if [[ ${#desconocidos[@]} -gt 0 && "$DEPLOY_PLAN" != 1 ]]; then
    echo "deploy-mail: no se sabe que commit corren: ${desconocidos[*]}" >&2
    echo "  (imagen sin etiqueta de commit y sin entrada en $MAIL_DEPLOY_PATH/.deployed-tags)" >&2
    echo "  indica los motores explicitamente: $0 ${desconocidos[*]}" >&2
    exit 1
  fi
fi
# Modo plan: una linea por motor levantado en el servidor (motor, commit que corre, estado) y la
# seleccion que haria el despliegue. Con motores explicitos, la seleccion es la pedida.
if [[ "$DEPLOY_PLAN" == 1 ]]; then
  for m in "${ORDEN[@]}"; do
    [[ -n "${VIVO[$m]:-}" ]] || continue
    if [[ "${CONSTRUIDO[$m]}" == 0 ]]; then
      printf '%-22s %-9s %s\n' "$m" "-" "imagen fijada por version (${IMAGEN[$m]})"; continue
    fi
    base="$(base_de "$m")"
    if [[ -z "$base" ]]; then
      printf '%-22s %-9s %s\n' "$m" "?" "no se sabe que commit corre"
    elif cambio_desde "$base" "$m"; then
      printf '%-22s %-9s %s\n' "$m" "${base:0:9}" "PENDIENTE: cambio desde entonces"
    else
      printf '%-22s %-9s %s\n' "$m" "${base:0:9}" "al dia"
    fi
  done
  echo "motores: ${SEL[*]:-ninguno}"
  exit 0
fi
if [[ ${#SEL[@]} -eq 0 ]]; then
  echo "nada que desplegar"; exit 0
fi
echo ">> tag $TAG | motores (${#SEL[@]}): ${SEL[*]}"

# netfilter reescribe el cortafuegos del host, dockerapi monta su socket de docker, watchdog los
# reinicia y acme pide certificados reales: contra el docker del puesto de trabajo, nunca. Se
# comparan los identificadores de los dos demonios, que es lo que decide donde corre el
# contenedor (un DEPLOY_HOST distinto puede seguir llegando al docker local).
solo=()
for m in "${SEL[@]}"; do [[ "${SOLO_SERVIDOR[$m]}" == 1 ]] && solo+=("$m"); done
if [[ ${#solo[@]} -gt 0 ]]; then
  id_local="$(docker info --format '{{.ID}}' 2>/dev/null || true)"
  id_remoto="$("${SSH[@]}" "docker info --format '{{.ID}}'" 2>/dev/null || true)"
  if [[ -z "$id_remoto" || "$id_remoto" == "$id_local" ]]; then
    echo "deploy-mail: ${solo[*]} solo se levantan en un servidor, y el destino es el docker de esta maquina" >&2
    echo "  (o no se pudo identificar su demonio). deploy/mail/README.md, Despliegue en un servidor." >&2
    exit 1
  fi
fi

despliegue_comprobar_regresion mail-engines.yml || exit 1
guardia_retroceso "$MAIL_PROJECT" "$TAG" "${SEL[@]}" || exit 1

# ── 2. build local y transporte ─────────────────────────────────────────────
# Solo lo que el servidor no tiene ya con esta etiqueta: la etiqueta es el commit y el arbol esta
# limpio, asi que es la misma imagen (con DEPLOY_ALLOW_DIRTY=1 no se puede asumir y se envia todo).
imagen_de() { echo "core-force-mail/$1:$TAG"; }
CONSTRUIR=()
for m in "${SEL[@]}"; do
  [[ "${CONSTRUIDO[$m]}" == 1 ]] || continue
  if [[ "${DEPLOY_ALLOW_DIRTY:-0}" != 1 ]] && "${SSH[@]}" "docker image inspect $(imagen_de "$m") >/dev/null 2>&1"; then
    echo ">> $m: el servidor ya tiene $(imagen_de "$m")"
    continue
  fi
  CONSTRUIR+=("$m")
done

if [[ ${#CONSTRUIR[@]} -gt 0 ]]; then
  export DOCKER_BUILDKIT=1
  for ((i = 0; i < ${#CONSTRUIR[@]}; i += MAIL_BUILD_LOTE)); do
    echo ">> build: ${CONSTRUIR[*]:i:MAIL_BUILD_LOTE}"
    compose_local build "${CONSTRUIR[@]:i:MAIL_BUILD_LOTE}"
  done
  IMAGENES=()
  for m in "${CONSTRUIR[@]}"; do IMAGENES+=("$(imagen_de "$m")"); done
  echo ">> enviando ${#IMAGENES[@]} imagen(es) al servidor"
  docker save "${IMAGENES[@]}" | gzip -1 | "${SSH[@]}" 'gunzip | docker load' >/dev/null
  "${SSH[@]}" "docker image inspect ${IMAGENES[*]} >/dev/null" || {
    echo "!! el servidor no tiene las imagenes tras el docker load" >&2
    exit 1
  }
fi

# ── 3. en el servidor, bajo candado ─────────────────────────────────────────
adquirir_candado motores || exit 1
guardia_retroceso "$MAIL_PROJECT" "$TAG" "${SEL[@]}" || exit 1

# El bind mount es de ida y vuelta: los motores reescriben su configuracion en el arbol
# sincronizado. rspamd deja local.d, override.d, plugins.d y custom/* con el uid de su contenedor y
# sus directorios sin permiso de escritura para el usuario de despliegue; postfix y dovecot dejan
# ahi los ficheros que generan (sql/*.cf 640 root:postfix, sni.map, main.cf...). Con eso, un tar
# lanzado por el usuario de despliegue no puede reemplazar lo versionado: falla con "File exists"
# (no puede desenlazar dentro de un directorio ajeno) y "Cannot utime", y el despliegue se detiene
# -antes de recrear nada, que es lo correcto, pero sin desplegar-.
#
# Se extrae y se borra con tar y rm DENTRO de un contenedor efimero como root, que es lo unico que
# el usuario de despliegue tiene de mas (docker, sin sudo). Solo toca las rutas del archivo: lo que
# los motores GENERAN no esta versionado (.gitignore), asi que no se reemplaza ni cambia de dueno, y
# los 640 root:postfix de los mapas que Postfix lee mientras corre siguen intactos. Lo versionado
# queda de root con los modos del repositorio (legible por todos) y cada motor vuelve a poner lo
# suyo al arrancar (rspamd/docker-entrypoint.sh rehace local.d, override.d, plugins.d y custom/*).
# Reemplazar lo versionado con HEAD es lo correcto: ningun motor guarda ahi estado que no rehaga
# solo -los mapas de rspamd/custom versionados son listas base y el entrypoint solo los `touch`ea-.
# Si algun dia un servicio escribe esos mapas (deploy/mail/README.md, requisito 4), hay que
# desplegar rspamd-mail en la misma ventana: su arranque es lo que devuelve custom/* a uid 82.
#
# git archive escribe 664/775 salvo que se le fije tar.umask (su defecto es 002): como la extraccion
# es de root, Postfix veia /opt/postfix/conf, master.cf y postscreen_access.cidr escribibles por el
# grupo y lo avisaba en cada arranque. Con 022 quedan 644/755, que es lo que espera.
#
# Idempotente: correrlo dos veces deja el mismo arbol.
ARBOL_MONTA="--network none -v $MAIL_DEPLOY_PATH:/arbol -w /arbol $IMAGEN_UTIL"
"${SSH[@]}" "mkdir -p $MAIL_DEPLOY_PATH"
if ! git -c tar.umask=022 archive HEAD -- "${FICHEROS_MOTORES[@]}" | "${SSH[@]}" "docker run --rm -i $ARBOL_MONTA tar -x"; then
  echo "!! no se pudo sincronizar deploy/mail en $MAIL_DEPLOY_PATH (detalle arriba)" >&2
  echo "   los motores no se han tocado: siguen con su version anterior." >&2
  exit 1
fi

# tar no borra: un fichero retirado del repositorio seguiria montado y leido. Se retiran los que
# se borraron desde cualquiera de los commits que corren los motores.
BORRADOS=()
declare -A BASES_VISTAS
for m in "${ORDEN[@]}"; do
  b="$(base_de "$m")"
  [[ -z "$b" || -n "${BASES_VISTAS[$b]:-}" ]] && continue
  BASES_VISTAS[$b]=1
  while IFS= read -r f; do [[ -n "$f" ]] && BORRADOS+=("$(printf '%q' "$f")"); done \
    < <(git diff --name-only --no-renames --diff-filter=D "$b" HEAD -- "${FICHEROS_MOTORES[@]}")
done
if [[ ${#BORRADOS[@]} -gt 0 ]]; then
  echo ">> retirando ${#BORRADOS[@]} fichero(s) borrados del repositorio"
  "${SSH[@]}" "docker run --rm $ARBOL_MONTA rm -f -- ${BORRADOS[*]}"
fi

# Uno a uno y en orden de arranque: nunca caen Postfix y Dovecot a la vez, y el pico de CPU y
# memoria (ClamAV al cargar firmas) no se suma al de otro motor en una maquina pequena. El
# compose va por el envoltorio de secretos con el .env del servidor, que es tambien el que usa la
# plataforma. Si un motor no arranca se para aqui: los siguientes siguen con su version anterior.
for m in "${SEL[@]}"; do
  renovar_candado
  echo ">> $m: recreando con $TAG"
  remote_mail "export MAIL_DEPLOY_TAG=$TAG WITH_SECRETS_ENV_FILE=$DEPLOY_PATH/.env && ops/security/secrets/with-secrets.sh docker compose -p $MAIL_PROJECT --env-file $DEPLOY_PATH/.env -f $COMPOSE_MOTORES -f $COMPOSE_IMAGENES up -d --no-deps --no-build --force-recreate $m"
  # Solo los construidos: redis-mail corre una imagen publica fijada por version, sin commit.
  if [[ "${CONSTRUIDO[$m]}" == 1 ]]; then
    verificar_imagen_desplegada "$MAIL_PROJECT" "$TAG" "$m" || exit 1
  fi
  remote_mail "ops/maintenance/esperar-sanos.sh --proyecto $MAIL_PROJECT --plazo $MAIL_DEPLOY_PLAZO --estable $MAIL_DEPLOY_ESTABLE $m" || {
    echo "!! $m no arranco con $TAG (detalle arriba); los motores posteriores no se tocaron." >&2
    echo "   rollback: desde el commit anterior, DEPLOY_ALLOW_ROLLBACK=1 $0 $m" >&2
    exit 1
  }
  remote_mail "{ grep -v '^$m ' .deployed-tags 2>/dev/null || true; echo '$m $TAG'; } > .deployed-tags.tmp && mv .deployed-tags.tmp .deployed-tags"
  registrar_despliegue "$MAIL_DEPLOY_PATH" motores "$TAG" "$m"
done

# Lo que se sincronizo tambien llega a los motores que no se recrearon; lo leeran al reiniciar.
pendientes=()
for m in "${ORDEN[@]}"; do
  [[ -n "${VIVO[$m]:-}" && " ${SEL[*]} " != *" $m "* ]] || continue
  b="$(base_de "$m")"
  if [[ -z "$b" ]] || cambio_desde "$b" "$m"; then pendientes+=("$m"); fi
done
if [[ ${#pendientes[@]} -gt 0 ]]; then
  echo ">> aviso: tienen en disco configuracion de $TAG sin recrear: ${pendientes[*]}" >&2
  echo ">>        despliegalos tambien ($0 ${pendientes[*]}) para que no la lean en un reinicio cualquiera" >&2
fi

remote_mail "ops/ecr/prune-local-images.sh --apply" >/dev/null || echo ">> no se pudo retirar las imagenes viejas del servidor" >&2

echo ">> DEPLOY MOTORES $TAG COMPLETO: ${SEL[*]}"
