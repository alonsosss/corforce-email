#!/usr/bin/env bash
# Guardarrail: el despliegue de los motores de correo (scripts/deploy-mail.sh) sigue siendo
# reproducible y seguro, sin docker.
#
# Los motores se desplegaron a mano con :latest: sin etiqueta por commit, sin candado, sin guardia
# de retroceso ni espera de arranque, y con la red mail-engines creada a mano porque la plataforma
# arrancaba antes. Aqui se comprueba:
#   - todo motor con build tiene su imagen por commit en docker-compose.mail.images.yml, que nunca
#     se busca en un registro, y todo motor privilegiado, en la red del host o con el socket de
#     docker lleva la etiqueta core-force-mail.solo-servidor;
#   - scripts/lib/despliegue.sh de verdad: guardia de retroceso en un repositorio de prueba y
#     candado ocupado, abandonado y liberado;
#   - ops/maintenance/recursos-externos.sh con un docker falso;
#   - scripts/deploy-mail.sh de punta a punta con ssh, docker y aws falsos: motor desconocido,
#     motor solo-servidor contra el docker local, motores de commit desconocido, y un despliegue
#     que construye solo lo que falta, sincroniza el arbol con tar dentro de un contenedor como
#     root (el usuario de despliegue no puede reemplazar lo que reescribio un motor), levanta uno a
#     uno con --no-build por el envoltorio de secretos, verifica la imagen, espera el arranque y
#     registra el commit de cada motor;
#   - los dos despliegues comparten el candado y la guardia, y la plataforma exige sus recursos
#     externos antes de recrear.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FALLOS=0
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

MOTORES="$ROOT/deploy/mail/docker-compose.mail.yml"
IMAGENES="$ROOT/deploy/mail/docker-compose.mail.images.yml"
DM="$ROOT/scripts/deploy-mail.sh"
DE="$ROOT/scripts/deploy-ecr.sh"
LIB="$ROOT/scripts/lib/despliegue.sh"

# --- 1. compose de los motores e imagenes de despliegue ------------------------------------
python3 - "$MOTORES" "$IMAGENES" <<'PY' || FALLOS=1
import re, sys

def bloques(ruta):
    texto = open(ruta, encoding="utf-8").read()
    m = re.search(r"^services:\n(.*?)(?=^\S)", texto + "\nfin:\n", re.S | re.M)
    partes = re.split(r"^  ([a-z0-9-]+):\n", m.group(1), flags=re.M)
    return {partes[i]: partes[i + 1] for i in range(1, len(partes), 2)}

motores, imagenes = bloques(sys.argv[1]), bloques(sys.argv[2])
fallos = []
construidos = {n for n, b in motores.items() if re.search(r"^    build:", b, re.M)}
for n in sorted(construidos):
    b = imagenes.get(n)
    if b is None:
        fallos.append(f"{n} tiene build y no esta en docker-compose.mail.images.yml")
        continue
    if not re.search(r"^    image: core-force-mail/" + re.escape(n) + r":\$\{MAIL_DEPLOY_TAG:\?[^}]*\}\s*$", b, re.M):
        fallos.append(f"{n}: la imagen de despliegue no es core-force-mail/{n}:${{MAIL_DEPLOY_TAG:?...}}")
    if not re.search(r"^    pull_policy: never\s*$", b, re.M):
        fallos.append(f"{n}: la imagen de despliegue no lleva pull_policy: never")
for n in sorted(set(imagenes) - construidos):
    fallos.append(f"{n} esta en docker-compose.mail.images.yml y no se construye en docker-compose.mail.yml")
for n, b in sorted(motores.items()):
    peligroso = (re.search(r"^    privileged: true", b, re.M) or re.search(r"^    network_mode: \"?host", b, re.M)
                 or "/var/run/docker.sock" in b)
    if peligroso and not re.search(r"^      core-force-mail\.solo-servidor: \"true\"", b, re.M):
        fallos.append(f"{n} es privilegiado, usa la red del host o el socket de docker y no lleva core-force-mail.solo-servidor")
for f in fallos:
    print("  FALLA: " + f, file=sys.stderr)
sys.exit(1 if fallos else 0)
PY

# --- 2. scripts/lib/despliegue.sh -------------------------------------------------------------
# ssh falso: ejecuta la orden remota (el ultimo argumento) en local y, cuando hay servidor de
# prueba, la registra para poder comprobar por donde va cada cosa.
mkdir -p "$TMP/bin"
cat >"$TMP/bin/ssh" <<'STUB'
#!/usr/bin/env bash
[[ -n "${STUB_SRV:-}" ]] && { echo "${!#}" >>"$STUB_SRV/estado/ssh" 2>/dev/null || true; }
exec bash -c "${!#}"
STUB
chmod +x "$TMP/bin/ssh"

REPO="$TMP/repo"
git init -q "$REPO"
git -C "$REPO" -c user.name=p -c user.email=p@p commit -q --allow-empty -m uno
VIEJO="$(git -C "$REPO" rev-parse --short HEAD)"
git -C "$REPO" -c user.name=p -c user.email=p@p commit -q --allow-empty -m dos
NUEVO="$(git -C "$REPO" rev-parse --short HEAD)"
cat >"$TMP/bin/docker" <<'STUB'
#!/usr/bin/env bash
[[ "$1" == ps ]] && cat "$STUB_PS"
STUB
chmod +x "$TMP/bin/docker"
lib() {
  (cd "$REPO" && PATH="$TMP/bin:$PATH" STUB_PS="$TMP/ps" DEPLOY_LOCK_DIR="$TMP/candado" DEPLOY_LOCK_PAUSA=0 \
    bash -c ". '$LIB'; $1") >"$TMP/lib.out" 2>&1
}
printf 'dovecot-mail core-force-mail/dovecot-mail:%s\n' "$NUEVO" >"$TMP/ps"
if lib "guardia_retroceso mail $VIEJO dovecot-mail"; then mal "guardia_retroceso: acepta desplegar un commit anterior al que corre"; fi
grep -q "RETROCESO DE VERSION" "$TMP/lib.out" || mal "guardia_retroceso: no explica el retroceso"
lib "DEPLOY_ALLOW_ROLLBACK=1 guardia_retroceso mail $VIEJO dovecot-mail" || mal "guardia_retroceso: no respeta DEPLOY_ALLOW_ROLLBACK=1"
printf 'dovecot-mail core-force-mail/dovecot-mail:%s\n' "$VIEJO" >"$TMP/ps"
lib "guardia_retroceso mail $NUEVO dovecot-mail" || mal "guardia_retroceso: rechaza un avance"
printf 'dovecot-mail mail-dovecot-mail:latest\nredis-mail redis:7.4.10-alpine\n' >"$TMP/ps"
lib "guardia_retroceso mail $VIEJO dovecot-mail redis-mail" || mal "guardia_retroceso: juzga imagenes sin etiqueta de commit"
printf 'otro-mail core-force-mail/otro-mail:%s\n' "$NUEVO" >"$TMP/ps"
lib "guardia_retroceso mail $VIEJO dovecot-mail" || mal "guardia_retroceso: mira un servicio que no se despliega"

lib "TAG=$NUEVO adquirir_candado motores && test -d '$TMP/candado' && grep -q motores '$TMP/candado/info'" || mal "candado: no se adquiere libre"
if lib "DEPLOY_LOCK_INTENTOS=0 adquirir_candado plataforma"; then mal "candado: se adquiere ocupado por otro despliegue vivo"; fi
touch -d '2 hours ago' "$TMP/candado"
lib "DEPLOY_LOCK_INTENTOS=0 adquirir_candado plataforma && grep -q plataforma '$TMP/candado/info'" || mal "candado: no roba uno abandonado"
lib "CANDADO_TOMADO=1 liberar_candado; test ! -e '$TMP/candado'" || mal "candado: liberar_candado no lo retira"

# --- 2b. puerta de regresion de los motores (make e2e-mail en verde) -----------------------------
# gh falso: devuelve las ejecuciones que prepara cada caso. El repositorio de prueba lleva un flujo con
# los mismos `paths` que el real (con una exclusion) y tres commits: el que da el verde, uno que solo
# toca documentacion y uno que toca lo que el flujo vigila.
cat >"$TMP/bin/gh" <<'STUB'
#!/usr/bin/env bash
[[ -n "${STUB_GH_FALLA:-}" ]] && { echo "gh: sin sesion" >&2; exit 1; }
cat "$STUB_GH_RUNS"
STUB
chmod +x "$TMP/bin/gh"
RG="$TMP/repo-regresion"
git init -q "$RG"
gitrg() { git -C "$RG" -c user.name=p -c user.email=p@p "$@"; }
mkdir -p "$RG/.github/workflows" "$RG/deploy/mail" "$RG/docs"
cat >"$RG/.github/workflows/mail-engines.yml" <<'YML'
name: Motores
on:
  push:
    branches: [main]
    paths:
      - 'deploy/mail/**'
      - '!deploy/mail/UPSTREAM.md'
      - 'ops/e2e/**'
  pull_request:
    paths:
      - 'otra/**'
YML
echo a >"$RG/deploy/mail/x"; echo a >"$RG/deploy/mail/UPSTREAM.md"; echo a >"$RG/docs/a.md"
gitrg add -A; gitrg commit -q -m verde
VERDE="$(gitrg rev-parse HEAD)"
echo b >"$RG/docs/a.md"; gitrg commit -q -am docs
DOCS="$(gitrg rev-parse HEAD)"
echo b >"$RG/deploy/mail/UPSTREAM.md"; gitrg commit -q -am libro
LIBRO="$(gitrg rev-parse HEAD)"
echo b >"$RG/deploy/mail/x"; gitrg commit -q -am motor
MOTOR="$(gitrg rev-parse HEAD)"
regresion() { # regresion <json de ejecuciones> [VAR=valor...]: deja la salida en $TMP/rg.out
  local json="$1"; shift
  printf '%s' "$json" >"$TMP/gh.json"
  (cd "$RG" && env PATH="$TMP/bin:$PATH" STUB_GH_RUNS="$TMP/gh.json" "$@" bash -c ". '$LIB'; despliegue_comprobar_regresion mail-engines.yml") >"$TMP/rg.out" 2>&1
}
corrida() { printf '[{"headSha":"%s","conclusion":"%s"}]' "$1" "$2"; }

git -C "$RG" checkout -q "$DOCS"
regresion "$(corrida "$VERDE" success)" || mal "regresion: rechaza un cambio de documentacion tras un verde"
git -C "$RG" checkout -q "$LIBRO"
regresion "$(corrida "$VERDE" success)" || mal "regresion: vigila un fichero que el flujo excluye (UPSTREAM.md)"
git -C "$RG" checkout -q "$MOTOR"
if regresion "$(corrida "$VERDE" success)"; then mal "regresion: acepta un cambio en deploy/mail sin una ejecucion que lo cubra"; fi
grep -q "hay cambios en lo que prueba make e2e-mail" "$TMP/rg.out" && grep -q "deploy/mail/x" "$TMP/rg.out" || mal "regresion: no dice que cambio ni por que rechaza"
regresion "$(corrida "$MOTOR" success)" || mal "regresion: rechaza un commit con su propia ejecucion verde"
if regresion "$(printf '[{"headSha":"%s","conclusion":"failure"},{"headSha":"%s","conclusion":"success"}]' "$MOTOR" "$VERDE")"; then
  mal "regresion: acepta un commit cuya ejecucion FALLO por haber un verde anterior"
fi
grep -q "FALLA para el commit" "$TMP/rg.out" || mal "regresion: no dice que la ejecucion del commit fallo"
if regresion "[]"; then mal "regresion: acepta sin ninguna ejecucion"; fi
grep -q "no hay ninguna ejecucion verde" "$TMP/rg.out" || mal "regresion: no explica que no hay ejecuciones"
if regresion "$(corrida "$(printf '0%.0s' {1..40})" success)"; then mal "regresion: acepta una ejecucion de un commit que no es ancestro"; fi
if regresion "[]" STUB_GH_FALLA=1; then mal "regresion: acepta si gh falla"; fi
grep -q "gh no pudo consultar" "$TMP/rg.out" || mal "regresion: no explica el fallo de gh"
regresion "[]" MAIL_DEPLOY_REGRESION=avisar || mal "regresion: avisar no deja seguir"
grep -q "PUERTA DE REGRESION" "$TMP/rg.out" || mal "regresion: avisar no dice lo que falta"
regresion "[]" MAIL_DEPLOY_REGRESION=omitir || mal "regresion: omitir no deja seguir"
grep -q "no se comprueba make e2e-mail" "$TMP/rg.out" || mal "regresion: omitir no lo deja dicho"
if regresion "[]" MAIL_DEPLOY_REGRESION=quiza; then mal "regresion: acepta un modo desconocido"; fi
# El despliegue de motores la llama antes de construir.
grep -q "despliegue_comprobar_regresion mail-engines.yml" "$DM" || mal "deploy-mail: no llama a la puerta de regresion"
lin_puerta="$(grep -n "despliegue_comprobar_regresion mail-engines.yml" "$DM" | head -1 | cut -d: -f1)"
lin_build="$(grep -n "^# ── 2. build local" "$DM" | head -1 | cut -d: -f1)"
[[ -n "$lin_puerta" && -n "$lin_build" && "$lin_puerta" -lt "$lin_build" ]] || mal "deploy-mail: la puerta de regresion no va antes de construir"

# --- 3. ops/maintenance/recursos-externos.sh ----------------------------------------------------
cat >"$TMP/bin/docker" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  compose) echo '{"networks":{"mail-engines":{"external":true,"name":"red-motores"},"interna":{"name":"x_interna"}},"volumes":{"mail-ssl":{"external":true,"name":"mail_ssl-vol"}}}' ;;
  network | volume) [[ " $STUB_EXISTEN " == *" $3 "* ]] ;;
esac
STUB
externos() { PATH="$TMP/bin:$PATH" STUB_EXISTEN="$1" bash "$ROOT/ops/maintenance/recursos-externos.sh" -f x.yml >"$TMP/ext.out" 2>&1; }
externos "red-motores mail_ssl-vol" || mal "recursos-externos: falla con todo presente"
if externos "red-motores"; then mal "recursos-externos: no detecta un volumen externo ausente"; fi
grep -q "volumen externo mail_ssl-vol" "$TMP/ext.out" || mal "recursos-externos: no nombra el volumen ausente"
if externos "mail_ssl-vol"; then mal "recursos-externos: no detecta una red externa ausente"; fi
grep -q "x_interna" "$TMP/ext.out" && mal "recursos-externos: exige una red que no es externa"

# --- 4. scripts/deploy-mail.sh con ssh, docker y aws falsos ----------------------------------
S="$TMP/srv"
mkdir -p "$S/app" "$S/estado"
{
  echo "ENVIRONMENT=production"
  sed -e '/^#/d' -e '/^$/d' -e '/?$/d' -e 's/$/=valor-de-prueba/' "$ROOT/ops/security/secrets/secret-keys.txt" \
    "$ROOT/ops/security/secrets/secret-keys-db.txt"
} >"$S/app/.env"
D="$ROOT/deploy/mail"
cat >"$S/config.json" <<JSON
{"services":{
 "unbound-mail":{"build":{"context":"$D/unbound"},"volumes":[{"type":"bind","source":"$D/unbound/unbound.conf"}]},
 "redis-mail":{"image":"redis:7.4.10-alpine","depends_on":{"netfilter-mail":{}},"volumes":[{"type":"bind","source":"$D/redis/redis-conf.sh"},{"type":"volume","source":"redis-vol"}]},
 "dovecot-mail":{"build":{"context":"$D/dovecot"},"depends_on":{"redis-mail":{},"netfilter-mail":{}}},
 "netfilter-mail":{"build":{"context":"$D/netfilter"},"labels":{"core-force-mail.solo-servidor":"true"}}
}}
JSON
cat >"$TMP/bin/aws" <<'STUB'
#!/usr/bin/env bash
exit 1
STUB
cat >"$TMP/bin/docker" <<'STUB'
#!/usr/bin/env bash
E="$STUB_SRV/estado"
echo "docker $*" >>"$E/ordenes"
servicio_filtrado() { local a; for a in "$@"; do [[ "$a" == label=com.docker.compose.service=* ]] && echo "${a##*=}"; done; }
case "$1" in
  buildx) exit 0 ;;
  info) echo daemon-unico ;;
  compose)
    for a in "$@"; do :; done
    case " $* " in
      *" config "*) cat "$STUB_SRV/config.json" ;;
      *" build "*) ;;
      *" up "*)
        svc="${!#}"
        img="core-force-mail/$svc:$MAIL_DEPLOY_TAG"; [[ "$svc" == redis-mail ]] && img=redis:7.4.10-alpine
        { grep -v "^$svc " "$E/ps" 2>/dev/null || true; echo "$svc $img"; } >"$E/ps.tmp"; mv "$E/ps.tmp" "$E/ps"
        env | grep -E '^(MAIL_DEPLOY_TAG|MAIL_REDIS_PASSWORD)=' | sed 's/=.*/=presente/' >>"$E/ordenes" ;;
    esac ;;
  ps)
    svc="$(servicio_filtrado "$@")"
    if [[ -z "$svc" ]]; then cat "$E/ps" 2>/dev/null; exit 0; fi
    linea="$(grep "^$svc " "$E/ps" 2>/dev/null)" || exit 0
    [[ "$*" == *'{{.ID}}'* ]] && echo "$svc" || echo "${linea#* }" ;;
  inspect) echo "running healthy 0" ;;
  image) shift 2; for i in "$@"; do grep -qx "$i" "$E/cargadas" 2>/dev/null || exit 1; done ;;
  run)
    # Se ejecuta la orden dentro del directorio montado, que es lo que hace el contenedor.
    shift; monta=""; img=""; cmd=()
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --rm | -i) shift ;;
        --network | -w) shift 2 ;;
        -v) monta="${2%%:*}"; shift 2 ;;
        *) if [[ -z "$img" ]]; then img="$1"; else cmd+=("$1"); fi; shift ;;
      esac
    done
    [[ -n "$monta" ]] || exit 1
    mkdir -p "$monta" && cd "$monta" && "${cmd[@]}" ;;
  save) shift; echo "$*" ;;
  load) tr ' ' '\n' >>"$E/cargadas" ;;
  images | logs) ;;
esac
STUB
chmod +x "$TMP/bin/aws" "$TMP/bin/docker"
TAG="$(git -C "$ROOT" rev-parse --short HEAD)"
desplegar() {
  PATH="$TMP/bin:$PATH" STUB_SRV="$S" DEPLOY_HOST=servidor-de-prueba DEPLOY_PATH="$S/app" MAIL_DEPLOY_PATH="$S/mail-src" \
    DEPLOY_LOCK_DIR="$S/candado" MAIL_DEPLOY_REGRESION=omitir DEPLOY_ALLOW_DIRTY="${DEPLOY_ALLOW_DIRTY:-1}" MAIL_DEPLOY_PLAZO=5 MAIL_DEPLOY_ESTABLE=0 ESPERAR_SANOS_PAUSA=1 \
    WITH_SECRETS_ENV_FILE="$S/app/.env" SECRETS_ENV_FILE="$S/secrets.env" SECRETS_DB_ENV_FILE="$S/secrets-db.env" \
    bash "$DM" "$@" >"$TMP/dm.out" 2>&1
}

rc=0; desplegar noexiste-mail || rc=$?
[[ $rc == 2 ]] && grep -q "'noexiste-mail' no es un servicio" "$TMP/dm.out" || mal "deploy-mail: acepta un motor que no existe (salida $rc)"
rc=0; desplegar netfilter-mail || rc=$?
[[ $rc == 1 ]] && grep -q "solo se levantan en un servidor" "$TMP/dm.out" || mal "deploy-mail: levanta un motor solo-servidor contra el docker local (salida $rc)"
grep -q " build " "$S/estado/ordenes" 2>/dev/null && mal "deploy-mail: construye antes de rechazar un motor solo-servidor"

printf 'dovecot-mail mail-dovecot-mail:latest\n' >"$S/estado/ps"
rc=0; desplegar || rc=$?
[[ $rc == 1 ]] && grep -q "no se sabe que commit corren: dovecot-mail" "$TMP/dm.out" || mal "deploy-mail: despliega sin saber que commit corre un motor (salida $rc)"

: >"$S/estado/ordenes"
rc=0; desplegar dovecot-mail unbound-mail redis-mail || rc=$?
if [[ $rc != 0 ]]; then
  mal "deploy-mail: el despliegue de prueba falla (salida $rc)"; sed 's/^/    /' "$TMP/dm.out" >&2
fi
O="$S/estado/ordenes"
grep -q " build dovecot-mail unbound-mail$" "$O" || mal "deploy-mail: no construye (en orden) los motores con build"
grep -E " build .*redis-mail" "$O" >/dev/null && mal "deploy-mail: intenta construir un motor sin build"
grep -q "^docker save core-force-mail/dovecot-mail:$TAG core-force-mail/unbound-mail:$TAG$" "$O" || mal "deploy-mail: no envia las imagenes etiquetadas por commit"
mapfile -t UPS < <(grep " up " "$O")
[[ ${#UPS[@]} == 3 ]] || mal "deploy-mail: no recrea exactamente los tres motores pedidos (${#UPS[@]})"
for linea in "${UPS[@]}"; do
  for marca in "-p mail " "--env-file $S/app/.env " "docker-compose.mail.yml " "docker-compose.mail.images.yml " " --no-deps " " --no-build " " --force-recreate "; do
    [[ "$linea" == *"$marca"* ]] || mal "deploy-mail: compose up sin '$marca': $linea"
  done
done
[[ "${UPS[0]:-}" == *" redis-mail" && "${UPS[1]:-}" == *" dovecot-mail" && "${UPS[2]:-}" == *" unbound-mail" ]] ||
  mal "deploy-mail: no recrea en orden de dependencias"
[[ "$(grep -c '^MAIL_REDIS_PASSWORD=presente$' "$O")" == 3 ]] || mal "deploy-mail: compose up sin los secretos del envoltorio"
grep -q "^dovecot-mail $TAG$" "$S/mail-src/.deployed-tags" 2>/dev/null && grep -q "^redis-mail $TAG$" "$S/mail-src/.deployed-tags" ||
  mal "deploy-mail: no registra el commit de cada motor en .deployed-tags"
[[ -f "$S/mail-src/deploy/mail/docker-compose.mail.yml" && -x "$S/mail-src/ops/maintenance/esperar-sanos.sh" ]] ||
  mal "deploy-mail: no sincroniza deploy/mail y sus guiones"
# La sincronizacion va con tar dentro de un contenedor como root y con la imagen fijada de un motor
# que no se construye: el usuario de despliegue no puede reemplazar lo que un motor reescribio en el
# bind mount (rspamd deja sus directorios de otro uid).
grep -q "^docker run --rm -i --network none -v $S/mail-src:/arbol -w /arbol redis:7.4.10-alpine tar -x$" "$O" ||
  mal "deploy-mail: no extrae el arbol con tar dentro de un contenedor como root (imagen fijada del compose)"
if grep "tar -x" "$S/estado/ssh" 2>/dev/null | grep -qv "docker run"; then mal "deploy-mail: extrae el arbol fuera del contenedor (el usuario de despliegue no puede)"; fi
linea_tar="$(grep -n " tar -x$" "$O" | head -1 | cut -d: -f1)"
linea_up="$(grep -n " up -d " "$O" | head -1 | cut -d: -f1)"
[[ -n "$linea_tar" && -n "$linea_up" && "$linea_tar" -lt "$linea_up" ]] ||
  mal "deploy-mail: la sincronizacion no va antes de recrear el primer motor"
# Idempotente: repetir el mismo despliegue no falla ni cambia lo sincronizado.
huella="$(find "$S/mail-src/deploy/mail" -type f | LC_ALL=C sort | xargs cat | cksum)"
rc=0; desplegar dovecot-mail unbound-mail redis-mail || rc=$?
[[ $rc == 0 ]] || mal "deploy-mail: repetir el despliegue falla (salida $rc)"
[[ "$(find "$S/mail-src/deploy/mail" -type f | LC_ALL=C sort | xargs cat | cksum)" == "$huella" ]] ||
  mal "deploy-mail: repetir el despliegue cambia el arbol sincronizado"
[[ -e "$S/candado" ]] && mal "deploy-mail: deja el candado puesto"
grep -q "unbound-mail: sano" "$TMP/dm.out" || mal "deploy-mail: no espera a que cada motor arranque"
if grep -q "valor-de-prueba" "$TMP/dm.out" "$O"; then mal "deploy-mail: imprime un secreto"; fi

# Segunda pasada sin cambios: nada que construir ni recrear.
: >"$O"
rc=0; desplegar || rc=$?
[[ $rc == 0 ]] && grep -q "nada que desplegar" "$TMP/dm.out" || mal "deploy-mail: sin cambios no dice 'nada que desplegar' (salida $rc)"
# Imagen ya en el servidor: se recrea sin reconstruir.
: >"$O"
desplegar_limpio() { DEPLOY_ALLOW_DIRTY=0 desplegar "$@"; }
if [[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=no)" ]]; then
  rc=0; desplegar_limpio unbound-mail || rc=$?
  [[ $rc == 0 ]] && ! grep -q " build " "$O" || mal "deploy-mail: reconstruye una imagen que el servidor ya tiene (salida $rc)"
fi

# Rechazo por candado ocupado.
mkdir -p "$S/candado"; echo "otro plataforma" >"$S/candado/info"
rc=0; DEPLOY_LOCK_INTENTOS=0 desplegar unbound-mail || rc=$?
[[ $rc == 1 ]] && grep -q "otro despliegue lleva demasiado" "$TMP/dm.out" || mal "deploy-mail: no respeta el candado de otro despliegue (salida $rc)"
[[ -e "$S/candado/info" ]] || mal "deploy-mail: retira un candado que no es suyo"
rm -rf "$S/candado"

# --- 5. los dos despliegues comparten piezas ----------------------------------------------------
for f in "$DE" "$DM"; do
  grep -q '^\. "$ROOT/scripts/lib/despliegue.sh"$' "$f" || mal "$(basename "$f"): no carga scripts/lib/despliegue.sh"
  grep -q -E '^(adquirir_candado|guardia_retroceso|despliegue_comprobar_[a-z]+)\(\)' "$f" && mal "$(basename "$f"): redefine una pieza comun"
done
[[ "$(grep -c 'guardia_retroceso app "\$TAG" "\${SVCS\[@\]}" || exit 1' "$DE")" == 4 ]] || mal "deploy-ecr.sh: la guardia de retroceso no esta antes del build y en los tres caminos"
python3 - "$DM" <<'PY' || mal "deploy-mail.sh: la guardia de retroceso no va antes del build y otra vez con el candado tomado"
import sys
t = open(sys.argv[1]).read()
g = 'guardia_retroceso "$MAIL_PROJECT" "$TAG" "${SEL[@]}" || exit 1'
p1 = t.find(g)
p2 = t.find(g, p1 + 1)
build, candado = t.find("compose_local build"), t.find("adquirir_candado motores || exit 1")
sys.exit(0 if t.count(g) == 2 and -1 < p1 < build < candado < p2 else 1)
PY
[[ "$(grep -c 'adquirir_candado plataforma || exit 1' "$DE")" == 3 ]] || mal "deploy-ecr.sh: algun camino no toma el candado"
python3 - "$DE" <<'PY' || mal "deploy-ecr.sh: leer_perfil no exige los recursos externos antes de recrear"
import re, sys
m = re.search(r"^leer_perfil\(\) \{\n(.*?)^\}", open(sys.argv[1]).read(), re.S | re.M)
sys.exit(0 if m and "with-secrets.sh ops/maintenance/recursos-externos.sh $COMPOSE_ARGS" in m.group(1) else 1)
PY
python3 - "$DM" <<'PY' || mal "deploy-mail.sh: el borrado de ficheros retirados no va dentro del contenedor como root"
import re, sys
t = open(sys.argv[1]).read()
m = re.search(r'^\s*"\$\{SSH\[@\]\}" "(.*)rm -f --', t, re.M)
sys.exit(0 if m and "docker run --rm" in m.group(1) else 1)
PY
[[ -x "$ROOT/ops/maintenance/recursos-externos.sh" && -x "$DM" ]] || mal "deploy-mail.sh o recursos-externos.sh no son ejecutables"

# Los motores que ABREN Redis al arrancar tienen que declararlo: el despliegue recrea de uno en uno
# y ordena por depends_on. mailcow los levantaba todos a la vez y la dependencia quedaba implicita:
# dockerapi-mail salio con ConnectionError en un servidor nuevo. No entran netfilter-mail (redis-mail
# depende de el: seria un ciclo) ni postfix, postfix-tlspol y acme, que abren Redis ya en marcha.
python3 - "$ROOT/deploy/mail/docker-compose.mail.yml" <<'PY' || FALLOS=1
import re, sys
texto = open(sys.argv[1], encoding="utf-8").read()
cuerpo = texto.split("\nservices:\n", 1)[1].split("\nnetworks:\n", 1)[0]
bloques = re.split(r"\n(?=  [a-z][a-z0-9-]*:\n)", "\n" + cuerpo)
por_servicio = {}
for b in bloques:
    m = re.match(r"\n?  ([a-z][a-z0-9-]*):\n", b)
    if m:
        por_servicio[m.group(1)] = b
fallos = []
for svc in ("dockerapi-mail", "rspamd-mail"):
    b = por_servicio.get(svc)
    if b is None:
        fallos.append(f"{svc} no existe en el compose")
        continue
    dep = re.search(r"\n    depends_on:\n((?:\s{6,}.*\n)+)", b)
    if not dep or "redis-mail" not in dep.group(1):
        fallos.append(f"{svc} no declara depends_on redis-mail")
if fallos:
    for f in fallos:
        print("  FALLA: " + f, file=sys.stderr)
    print("    Sin eso el despliegue por motor los recrea antes que Redis y no arrancan.", file=sys.stderr)
    sys.exit(1)
PY

# Postfix tiene que llegar a Dovecot sin DNS: con el contenedor de Dovecot parado (reinicio, despliegue) el
# nombre deja de resolverse, Postfix lo toma por un error permanente y REBOTA el correo entrante en vez de
# dejarlo en cola (comprobado en produccion el 2026-09-21: dsn=5.4.4 "Host not found" para name=dovecot).
python3 - "$ROOT/deploy/mail" <<'PY' || FALLOS=1
import re, sys
base = sys.argv[1]
compose = open(f"{base}/docker-compose.mail.yml", encoding="utf-8").read()
cf = open(f"{base}/postfix/conf/main.cf.base", encoding="utf-8").read()
fallos = []
postfix = re.search(r"\n  postfix-mail:\n(.*?)(?=\n  [a-z][a-z0-9-]*:\n)", compose, re.S)
dovecot = re.search(r"\n  dovecot-mail:\n(.*?)(?=\n  [a-z][a-z0-9-]*:\n)", compose, re.S)
ip = re.search(r"ipv4_address: (\$\{IPV4_NETWORK:-[0-9.]+\}\.\d+)", dovecot.group(1)) if dovecot else None
if not ip:
    fallos.append("dovecot-mail no tiene ipv4_address fija")
if not postfix or not re.search(r"extra_hosts:\n\s+- \"dovecot:" + (re.escape(ip.group(1)) if ip else r"\S+") + r"\"", postfix.group(1)):
    fallos.append("postfix-mail no declara extra_hosts dovecot:<la IP fija de dovecot-mail>")
if not re.search(r"^lmtp_host_lookup = native$", cf, re.M):
    fallos.append("main.cf.base no fija lmtp_host_lookup = native: Postfix buscaria dovecot por DNS")
if not re.search(r"^virtual_transport = lmtp:inet:dovecot:24$", cf, re.M):
    fallos.append("virtual_transport ya no es lmtp:inet:dovecot:24: revisar que sigue resolviendose por /etc/hosts")
if fallos:
    for f in fallos:
        print("  FALLA: " + f, file=sys.stderr)
    print("    Sin eso, un reinicio de Dovecot hace que Postfix rebote el correo entrante como permanente.", file=sys.stderr)
    sys.exit(1)
PY

if [[ $FALLOS -ne 0 ]]; then
  echo "check-deploy-mail: FALLA" >&2
  exit 1
fi
echo "  OK: los motores se despliegan por commit, bajo el candado y la guardia comunes, uno a uno con --no-build y esperando su arranque; la plataforma exige sus recursos externos."
