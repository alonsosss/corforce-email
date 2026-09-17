#!/usr/bin/env bash
# Prueba con docker de scripts/deploy-mail.sh contra un servidor simulado: un contenedor con su
# propio demonio de docker (docker:dind) y sshd, al que el despliegue entra por ssh como a
# produccion. Nada de lo que despliega toca el docker de esta maquina: redes, volumenes, proyecto
# mail y puertos viven dentro del contenedor. Recorre:
#   - un servidor nuevo: la plataforma no tiene su red externa (recursos-externos.sh sale 1);
#   - el estado de produccion antes de automatizar: imagenes mail-<motor>:latest, la red
#     mail-engines creada a mano con las etiquetas de compose y los motores levantados a mano;
#   - la migracion con motores explicitos: cada motor pasa a core-force-mail/<motor>:<commit> sin
#     recrear la red, que la plataforma ya encuentra;
#   - un commit nuevo desplegado sin argumentos (detecta los motores afectados) y una segunda
#     pasada sin nada que desplegar;
#   - un retroceso: rechazado, y con DEPLOY_ALLOW_ROLLBACK=1 aplicado sin reconstruir (la imagen
#     sigue en el servidor);
#   - un motor que no arranca: el despliegue falla, no registra su commit y los motores que iban
#     detras no se tocan.
#
#   bash ops/scaffold/test-deploy-mail.sh      # DEPLOY_MAIL_TEST_KEEP=1 deja el servidor en pie
#
# Trabaja sobre un clon temporal con el arbol actual commiteado alli (el despliegue exige un
# commit), nunca sobre este repositorio. Construye unbound-mail y olefy-mail en el docker local
# (con su cache) y retira al final las etiquetas core-force-mail/* que creo. Una sola ejecucion a
# la vez: otra sale con 3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
NOMBRE="${DEPLOY_MAIL_TEST_NAME:-cfm-deploy-mail-prueba}"
IMG_DIND="docker:29.8.0-dind"
MOTORES=(unbound-mail olefy-mail)

CERROJO="${TMPDIR:-/tmp}/$NOMBRE.lock"
exec 9>"$CERROJO"
if ! flock -n 9; then
  echo "test-deploy-mail: otra ejecucion (pid $(cat "$CERROJO.pid" 2>/dev/null || echo '?')) usa $NOMBRE" >&2
  exit 3
fi
echo $$ >"$CERROJO.pid"

W="$(mktemp -d "${TMPDIR:-/tmp}/$NOMBRE.XXXXXX")"
REPO="$W/repo"
FALLOS=0
ETIQUETAS=()
ok() { echo "  OK: $*"; }
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

limpiar() {
  local rc=$?
  if [[ "${DEPLOY_MAIL_TEST_KEEP:-0}" == 1 ]]; then
    echo "DEPLOY_MAIL_TEST_KEEP=1: se deja en pie el contenedor $NOMBRE ($W)"
  else
    docker rm -f -v "$NOMBRE" >/dev/null 2>&1 || true
    docker rmi "$NOMBRE-servidor:prueba" >/dev/null 2>&1 || true
    for e in "${ETIQUETAS[@]}"; do
      for m in "${MOTORES[@]}"; do docker rmi "core-force-mail/$m:$e" >/dev/null 2>&1 || true; done
    done
    rm -rf "$W"
  fi
  rm -f "$CERROJO.pid"
  exit $rc
}
trap limpiar EXIT

# --- servidor simulado --------------------------------------------------------------------------
echo "== Servidor simulado ($NOMBRE) =="
mkdir -p "$W/imagen" "$W/bin"
ssh-keygen -q -t ed25519 -N '' -f "$W/clave" >/dev/null
cp "$W/clave.pub" "$W/imagen/authorized_keys"
cat >"$W/imagen/Dockerfile" <<EOF
FROM $IMG_DIND
RUN apk add --no-cache openssh-server bash python3 tar gzip \\
  && ssh-keygen -A && sed -i 's/^root:[^:]*:/root:*:/' /etc/shadow \\
  && mkdir -p /root/.ssh && chmod 700 /root/.ssh
COPY authorized_keys /root/.ssh/authorized_keys
ENV DOCKER_TLS_CERTDIR=
ENTRYPOINT ["sh", "-c", "/usr/sbin/sshd && exec dockerd-entrypoint.sh"]
EOF
docker build -q -t "$NOMBRE-servidor:prueba" "$W/imagen" >/dev/null
docker rm -f -v "$NOMBRE" >/dev/null 2>&1 || true
docker run -d --privileged --name "$NOMBRE" "$NOMBRE-servidor:prueba" >/dev/null
IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NOMBRE")"

# ssh no lee $HOME para known_hosts: un envoltorio de la prueba los deja en su carpeta. Las
# opciones anteriores ganan a las que anade el despliegue.
cat >"$W/bin/ssh" <<EOF
#!/usr/bin/env bash
exec $(command -v ssh) -o UserKnownHostsFile="$W/known_hosts" -o LogLevel=ERROR "\$@"
EOF
chmod +x "$W/bin/ssh"
export PATH="$W/bin:$PATH" DEPLOY_HOST="$IP" DEPLOY_USER=root DEPLOY_SSH_KEY="$W/clave"
srv() { ssh -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i "$W/clave" "root@$IP" "$@"; }
for _ in $(seq 1 60); do srv 'docker info >/dev/null 2>&1' 2>/dev/null && break; sleep 1; done
srv 'docker info >/dev/null' 2>/dev/null || { echo "el demonio del servidor simulado no arranco" >&2; exit 1; }
ok "servidor simulado con sshd y su propio docker en $IP"

# --- clon con el arbol actual commiteado ----------------------------------------------------------
git clone -q -c advice.detachedHead=false --shared "$ROOT" "$REPO"
git -C "$ROOT" diff --binary HEAD | git -C "$REPO" apply --allow-empty
(cd "$ROOT" && git ls-files --others --exclude-standard -z) | while IFS= read -r -d '' f; do
  mkdir -p "$REPO/$(dirname "$f")"; cp -p "$ROOT/$f" "$REPO/$f"
done
commit() { git -C "$REPO" add -A && git -C "$REPO" -c user.name=prueba -c user.email=prueba@prueba.test commit -q --allow-empty -m "$1"; git -C "$REPO" rev-parse --short HEAD; }
T1="$(commit "arbol de la prueba")"
ETIQUETAS+=("$T1" manual)

# .env del servidor: produccion y todas las credenciales del inventario, aleatorias y nunca impresas.
{
  echo "ENVIRONMENT=production"
  echo "MAIL_HOSTNAME=mail.prueba.test"
  echo "SKIP_UNBOUND_HEALTHCHECK=y"
  sed -e '/^#/d' -e '/^$/d' -e 's/?$//' "$ROOT/ops/security/secrets/secret-keys.txt" "$ROOT/ops/security/secrets/secret-keys-db.txt" |
    while read -r k; do echo "$k=$(openssl rand -hex 16)"; done
} >"$W/server.env"
srv 'mkdir -p /opt/core-force-mail/app && cat > /opt/core-force-mail/app/.env && chmod 600 /opt/core-force-mail/app/.env' <"$W/server.env"
git -C "$REPO" archive HEAD docker-compose.yml ops/security ops/maintenance | srv 'tar -x -C /opt/core-force-mail/app'

externos() { srv "cd /opt/core-force-mail/app && WITH_SECRETS_ENV_FILE=.env ops/security/secrets/with-secrets.sh ops/maintenance/recursos-externos.sh -f docker-compose.yml" >"$W/externos.out" 2>&1; }
if externos; then mal "recursos-externos: la plataforma no echa en falta la red de los motores en un servidor nuevo"; else
  grep -q "red externo mail-engines" "$W/externos.out" && ok "servidor nuevo: la plataforma se detiene por la red mail-engines antes de recrear" ||
    mal "recursos-externos: no nombra la red que falta"
fi

# --- estado de produccion antes de automatizar --------------------------------------------------
echo "== Motores levantados a mano con :latest =="
(cd "$REPO" && MAIL_DEPLOY_TAG=manual docker compose -p mail --env-file /dev/null -f deploy/mail/docker-compose.mail.yml \
  -f deploy/mail/docker-compose.mail.images.yml build -q "${MOTORES[@]}") >"$W/build-manual.out" 2>&1
docker save "core-force-mail/unbound-mail:manual" "core-force-mail/olefy-mail:manual" | gzip -1 | srv 'gunzip | docker load' >/dev/null
srv 'for m in unbound-mail olefy-mail; do docker tag core-force-mail/$m:manual mail-$m:latest && docker rmi core-force-mail/$m:manual; done' >/dev/null
srv 'docker network create --driver bridge --subnet 172.22.1.0/24 --opt com.docker.network.bridge.name=br-mail --label com.docker.compose.network=mail-engines --label com.docker.compose.project=mail mail-engines' >/dev/null
git -C "$REPO" archive HEAD deploy/mail | srv 'mkdir -p /opt/core-force-mail/mail-src && tar -x -C /opt/core-force-mail/mail-src'
srv "cd /opt/core-force-mail/mail-src/deploy/mail && WITH_SECRETS_ENV_FILE=/opt/core-force-mail/app/.env /opt/core-force-mail/app/ops/security/secrets/with-secrets.sh docker compose -p mail -f docker-compose.mail.yml --env-file /opt/core-force-mail/app/.env up -d --no-build --no-deps ${MOTORES[*]}" >"$W/manual.out" 2>&1 ||
  { sed 's/^/    /' "$W/manual.out" >&2; echo "no se pudo reproducir el estado manual" >&2; exit 1; }
RED_ANTES="$(srv 'docker network inspect -f {{.Id}} mail-engines')"
ok "estado manual: $(srv "docker ps --format '{{.Image}}' | sort | tr '\n' ' '")"

imagen() { srv "docker ps -a --filter label=com.docker.compose.project=mail --filter label=com.docker.compose.service=$1 --format '{{.Image}}'"; }
desplegar() {
  (cd "$REPO" && MAIL_DEPLOY_PLAZO="${PLAZO:-240}" MAIL_DEPLOY_ESTABLE=10 bash scripts/deploy-mail.sh "$@") >"$W/deploy.out" 2>&1
}
mostrar() { sed 's/^/    /' "$W/deploy.out" >&2; }

# --- migracion ------------------------------------------------------------------------------------
echo "== Migracion con motores explicitos ($T1) =="
rc=0; desplegar || rc=$?
[[ $rc == 1 ]] && grep -q "no se sabe que commit corren" "$W/deploy.out" && ok "sin argumentos no despliega motores de commit desconocido" ||
  { mal "sin argumentos y motores en :latest sale $rc"; mostrar; }
rc=0; desplegar "${MOTORES[@]}" || rc=$?
if [[ $rc == 0 ]]; then ok "despliegue de ${MOTORES[*]} con salida 0"; else mal "la migracion sale $rc"; mostrar; fi
for m in "${MOTORES[@]}"; do
  [[ "$(imagen "$m")" == "core-force-mail/$m:$T1" ]] && ok "$m corre core-force-mail/$m:$T1" || mal "$m corre $(imagen "$m")"
done
[[ "$(srv 'docker network inspect -f {{.Id}} mail-engines')" == "$RED_ANTES" ]] && ok "la red mail-engines creada a mano se conserva (no se recrea)" ||
  mal "la red mail-engines se recreo"
[[ "$(srv "docker network inspect -f '{{len .Containers}}' mail-engines")" == 2 ]] || mal "los motores no estan en mail-engines"
srv 'cat /opt/core-force-mail/mail-src/.deployed-tags' | grep -q "^olefy-mail $T1$" && ok ".deployed-tags registra el commit de cada motor" ||
  mal ".deployed-tags sin el commit de olefy-mail"
externos && ok "con los motores desplegados la plataforma encuentra la red mail-engines" || mal "recursos-externos sigue fallando tras desplegar los motores"

# --- commit nuevo, deteccion automatica -------------------------------------------------------------
echo "== Commit nuevo sin argumentos =="
echo "# cambio de la prueba" >>"$REPO/deploy/mail/docker-compose.mail.images.yml"
T2="$(commit "cambio que afecta a todos los motores")"
ETIQUETAS+=("$T2")
rc=0; desplegar || rc=$?
if [[ $rc == 0 ]] && grep -q "motores (2): " "$W/deploy.out"; then ok "detecta y despliega los dos motores afectados"; else mal "deteccion automatica sale $rc"; mostrar; fi
[[ "$(imagen olefy-mail)" == "core-force-mail/olefy-mail:$T2" ]] || mal "olefy-mail no paso a $T2"
rc=0; desplegar || rc=$?
[[ $rc == 0 ]] && grep -q "nada que desplegar" "$W/deploy.out" && ok "segunda pasada: nada que desplegar" || { mal "segunda pasada sale $rc"; mostrar; }

# --- retroceso ------------------------------------------------------------------------------------
echo "== Retroceso a $T1 =="
git -C "$REPO" checkout -q "$T1"
rc=0; desplegar olefy-mail || rc=$?
[[ $rc == 1 ]] && grep -q "RETROCESO DE VERSION" "$W/deploy.out" && ok "rechaza desplegar un commit anterior" || { mal "retroceso sin permiso sale $rc"; mostrar; }
[[ "$(imagen olefy-mail)" == "core-force-mail/olefy-mail:$T2" ]] || mal "el retroceso rechazado toco olefy-mail"
rc=0; DEPLOY_ALLOW_ROLLBACK=1 desplegar olefy-mail || rc=$?
if [[ $rc == 0 ]] && grep -q "el servidor ya tiene core-force-mail/olefy-mail:$T1" "$W/deploy.out" && ! grep -q ">> build" "$W/deploy.out"; then
  ok "rollback con DEPLOY_ALLOW_ROLLBACK=1 sin reconstruir ni enviar la imagen"
else
  mal "rollback permitido sale $rc"; mostrar
fi
[[ "$(imagen olefy-mail)" == "core-force-mail/olefy-mail:$T1" ]] || mal "olefy-mail no volvio a $T1"
git -C "$REPO" checkout -q -

# --- motor que no arranca -----------------------------------------------------------------------------
echo "== Motor que no arranca =="
# unbound-mail va delante de olefy-mail en el orden de arranque (acme depende de el): se rompe ese
# para comprobar que el despliegue se detiene y no toca al siguiente.
sed -i 's|supervisord.conf"\]$|no-existe.conf"]|' "$REPO/deploy/mail/unbound/Dockerfile"
T3="$(commit "unbound roto")"
ETIQUETAS+=("$T3")
rc=0; PLAZO=40 desplegar unbound-mail olefy-mail || rc=$?
if [[ $rc == 1 ]] && grep -q "unbound-mail no arranco con $T3" "$W/deploy.out"; then ok "el despliegue falla si el motor no arranca, con su registro"; else mal "motor roto sale $rc"; mostrar; fi
srv 'cat /opt/core-force-mail/mail-src/.deployed-tags' | grep -q "^unbound-mail $T3$" && mal "registra el commit de un motor que no arranco"
[[ "$(imagen olefy-mail)" == "core-force-mail/olefy-mail:$T1" ]] && ok "el motor siguiente (olefy-mail) no se toco" ||
  mal "olefy-mail corre $(imagen olefy-mail) tras el fallo de unbound"
srv 'test ! -e /tmp/core-deploy.lock' || mal "el despliegue fallido deja el candado puesto"

echo ""
if [[ $FALLOS -ne 0 ]]; then
  echo "test-deploy-mail: FALLA" >&2
  exit 1
fi
echo "test-deploy-mail: OK"
