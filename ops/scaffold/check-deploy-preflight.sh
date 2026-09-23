#!/usr/bin/env bash
# Guardarrail: el despliegue no da por bueno un servicio que no arranca.
#
# Ejecuta de verdad las tres piezas que rodean la recreacion en el servidor y comprueba que
# siguen enganchadas en los dos caminos de despliegue (scripts/deploy-ecr.sh y release.yml):
#   - ops/maintenance/esperar-sanos.sh, con un docker falso: sano, estable sin chequeo, en bucle
#     de reinicios, sin contenedor y sin salud;
#   - ops/maintenance/claves-env.sh: nombra las claves ausentes y nunca un valor;
#   - ops/db/pgbouncer-userlist.sh --ensure: genera si falta y no reescribe uno distinto;
#   - y las piezas comunes de scripts/lib/despliegue.sh que atan el despliegue a un commit:
#     verificar_imagen_desplegada y servicios_sin_commit. El transporte save -el del perfil
#     autoalojado, que es produccion- enviaba app-<svc>:latest, y con una etiqueta flotante ni la
#     guardia de retroceso ni la verificacion podian saber que commit corria cada servicio: el
#     despliegue podia dejar codigo viejo dentro sin que nada lo dijera.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FALLOS=0
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

# --- esperar-sanos.sh con un docker falso -------------------------------------------------
mkdir -p "$TMP/bin"
cat >"$TMP/bin/docker" <<'STUB'
#!/usr/bin/env bash
var() { local v="STUB_${1//-/_}"; printf '%s' "${!v:-}"; }
case "$1" in
  ps) for a in "$@"; do case "$a" in label=com.docker.compose.service=*) s="${a#*=}"; s="${s#*=}" ;; esac; done
      [[ -n "$(var "$s")" ]] && echo "$s" ;;
  inspect) var "${!#}"; echo ;;
  logs) echo "registro de ${!#}: motivo del fallo" ;;
esac
STUB
chmod +x "$TMP/bin/docker"
sanos() { PATH="$TMP/bin:$PATH" ESPERAR_SANOS_PAUSA=1 bash "$ROOT/ops/maintenance/esperar-sanos.sh" --plazo 3 --estable 1 "$@" >"$TMP/sanos.out" 2>&1; }

STUB_a="running healthy 0" sanos a && grep -q 'a: sano' "$TMP/sanos.out" || mal "esperar-sanos: no acepta un servicio sano"
STUB_b="running sin-chequeo 0" sanos b || mal "esperar-sanos: no acepta uno estable sin chequeo"
if STUB_c="restarting sin-chequeo 7" sanos c; then mal "esperar-sanos: acepta un servicio en bucle de reinicios"; fi
grep -q 'registro de c: motivo del fallo' "$TMP/sanos.out" || mal "esperar-sanos: no muestra el registro del que falla"
if STUB_d="running unhealthy 0" sanos d; then mal "esperar-sanos: acepta un servicio sin salud"; fi
if sanos inexistente; then mal "esperar-sanos: acepta un servicio sin contenedor"; fi
grep -q 'no hay contenedor' "$TMP/sanos.out" || mal "esperar-sanos: no dice que falta el contenedor"
if STUB_e="running healthy 0" STUB_mail_auth="restarting sin-chequeo 3" sanos e mail-auth; then mal "esperar-sanos: acepta la mezcla con uno caido"; fi
grep -q 'no arrancaron: mail-auth$' "$TMP/sanos.out" || mal "esperar-sanos: no senala solo al servicio caido"
STUB_f="exited sin-chequeo 0 no 0" sanos f && grep -q 'f: trabajo completado' "$TMP/sanos.out" || mal "esperar-sanos: no acepta un trabajo de arranque que salio con 0"
if STUB_g="exited sin-chequeo 0 no 1" sanos g; then mal "esperar-sanos: acepta un trabajo de arranque que salio con error"; fi
grep -q 'salieron con error: g$' "$TMP/sanos.out" && grep -q 'registro de g: motivo del fallo' "$TMP/sanos.out" ||
  mal "esperar-sanos: no senala el trabajo fallido con su registro"
if STUB_h="exited sin-chequeo 0 always 0" sanos h; then mal "esperar-sanos: acepta un servicio con restart always que salio con 0"; fi

# --- claves-env.sh ------------------------------------------------------------------------
printf 'ENVIRONMENT=production\n# SCHEDULER_URL=comentada\nPOSTGRES_PASSWORD=valor-secreto-no-imprimir\n' >"$TMP/env"
printf 'ENVIRONMENT\nSCHEDULER_URL\nPOSTGRES_PASSWORD\nDB_UPSTREAM_SSLMODE\nminuscula\n' |
  bash "$ROOT/ops/maintenance/claves-env.sh" "$TMP/env" >"$TMP/claves.out" 2>&1 || mal "claves-env: no debe bloquear"
grep -q '^  SCHEDULER_URL$' "$TMP/claves.out" || mal "claves-env: no nombra una clave ausente (o cuenta la comentada)"
grep -q '^  DB_UPSTREAM_SSLMODE$' "$TMP/claves.out" || mal "claves-env: no nombra todas las ausentes"
if grep -q -E 'ENVIRONMENT$|POSTGRES_PASSWORD$|minuscula' "$TMP/claves.out"; then mal "claves-env: nombra claves presentes o nombres invalidos"; fi
if grep -q 'valor-secreto' "$TMP/claves.out"; then mal "claves-env: imprime un valor del .env"; fi
printf 'ENVIRONMENT\n' | bash "$ROOT/ops/maintenance/claves-env.sh" "$TMP/env" 2>&1 | grep -q 'todas las claves' || mal "claves-env: no reconoce un .env completo"

# --- pgbouncer-userlist.sh --ensure -------------------------------------------------------
ul() { env -i PATH="$PATH" HOME="$TMP" PGBOUNCER_USERLIST="$TMP/userlist.txt" POSTGRES_PASSWORD=clave-de-prueba \
  bash "$ROOT/ops/db/pgbouncer-userlist.sh" "$@" >"$TMP/ul.out" 2>&1; }
ul --ensure || mal "userlist --ensure: falla sin fichero"
grep -q '"mail_admin" "clave-de-prueba"' "$TMP/userlist.txt" 2>/dev/null || mal "userlist --ensure: no genera el fichero que falta"
antes="$(stat -c %Y "$TMP/userlist.txt")"
sleep 1
ul --ensure || mal "userlist --ensure: falla con el fichero al dia"
[[ "$(stat -c %Y "$TMP/userlist.txt")" == "$antes" ]] || mal "userlist --ensure: reescribe un fichero al dia"
printf '"mail_svc_x" "otra"\n' >>"$TMP/userlist.txt"
ul --ensure || mal "userlist --ensure: bloquea ante un fichero distinto (debe avisar)"
grep -q 'mail_svc_x' "$TMP/userlist.txt" || mal "userlist --ensure: reescribio un fichero distinto"
grep -q 'AVISO' "$TMP/ul.out" || mal "userlist --ensure: no avisa de un fichero distinto"

# --- require-server-environment.sh: entorno declarado y marcadores de .env.example ----------
entorno() { printf '%b' "$1" >"$TMP/srv.env"; WITH_SECRETS_ENV_FILE="$TMP/srv.env" bash "$ROOT/ops/security/secrets/require-server-environment.sh" >"$TMP/srv.out" 2>&1; }
entorno 'ENVIRONMENT=production\nMAIL_SPF_INCLUDE=include:spf.plataforma.com\n' || mal "entorno: rechaza un servidor limpio"
entorno 'ENVIRONMENT=staging\n' || mal "entorno: rechaza staging"
if entorno 'ENVIRONMENT=development\n'; then mal "entorno: acepta development en un servidor"; fi
if entorno 'MAIL_HOSTNAME=mail.plataforma.com\n'; then mal "entorno: acepta un .env sin ENVIRONMENT"; fi
if entorno 'ENVIRONMENT=production\nMAIL_SPF_INCLUDE=include:spf.YOUR_DOMAIN.com\nPUBLIC_BASE_URL=https://app.YOUR_DOMAIN.com\n'; then
  mal "entorno: acepta el marcador YOUR_DOMAIN"
fi
grep -q 'MAIL_SPF_INCLUDE PUBLIC_BASE_URL' "$TMP/srv.out" || mal "entorno: no nombra todas las claves con YOUR_DOMAIN"
if grep -q 'spf.YOUR_DOMAIN\|app.YOUR_DOMAIN' "$TMP/srv.out"; then mal "entorno: imprime un valor del .env"; fi
entorno 'ENVIRONMENT=production\n# MAIL_SPF_INCLUDE=include:spf.YOUR_DOMAIN.com\n' || mal "entorno: cuenta un marcador comentado"
entorno 'ENVIRONMENT=production\nPOSTGRES_PASSWORD=CHANGE_ME_IN_PRODUCTION\n' || mal "entorno: bloquea por CHANGE_ME (debe avisar)"
grep -q 'aviso:.*POSTGRES_PASSWORD' "$TMP/srv.out" || mal "entorno: no avisa de CHANGE_ME"
if grep -q 'IN_PRODUCTION' "$TMP/srv.out"; then mal "entorno: imprime el valor de un secreto"; fi

# --- etiqueta por commit y verificacion de la imagen --------------------------------------
LIB="$ROOT/scripts/lib/despliegue.sh"
cat >"$TMP/bin/ssh" <<'STUB'
#!/usr/bin/env bash
exec bash -c "${!#}"
STUB
cat >"$TMP/bin/docker" <<'STUB'
#!/usr/bin/env bash
# Solo `docker ps`: devuelve "<servicio> <imagen>" por linea, del fichero de la prueba.
[[ "$1" == ps ]] || exit 0
svc=""
for a in "$@"; do case "$a" in label=com.docker.compose.service=*) svc="${a##*=}" ;; esac; done
if [[ -n "$svc" ]]; then
  awk -v s="$svc" '$1==s {print $2; exit}' "$STUB_PS"
else
  cat "$STUB_PS"
fi
STUB
chmod +x "$TMP/bin/ssh" "$TMP/bin/docker"
REPO="$TMP/repo"
git init -q "$REPO"
git -C "$REPO" -c user.name=p -c user.email=p@p commit -q --allow-empty -m uno
COMMIT="$(git -C "$REPO" rev-parse --short HEAD)"
lib() { (cd "$REPO" && PATH="$TMP/bin:$PATH" STUB_PS="$TMP/ps" bash -c ". '$LIB'; $1") >"$TMP/lib.out" 2>&1; }

printf 'gateway core-force-mail/gateway:%s\nidentity core-force-mail/identity:%s\n' "$COMMIT" "$COMMIT" >"$TMP/ps"
lib "verificar_imagen_desplegada app $COMMIT gateway identity" || mal "verificar_imagen_desplegada: no acepta contenedores con la imagen del commit"
if lib "servicios_sin_commit app gateway identity | grep ."; then mal "servicios_sin_commit: senala servicios que si corren su commit"; fi
printf 'gateway app-gateway:latest\nidentity core-force-mail/identity:%s\n' "$COMMIT" >"$TMP/ps"
if lib "verificar_imagen_desplegada app $COMMIT gateway identity"; then mal "verificar_imagen_desplegada: da por aplicado un servicio que corre otra imagen"; fi
grep -q "gateway corre 'app-gateway:latest'" "$TMP/lib.out" || mal "verificar_imagen_desplegada: no dice que imagen corre el servicio"
grep -q identity "$TMP/lib.out" && mal "verificar_imagen_desplegada: senala un servicio que si esta al dia"
lib "servicios_sin_commit app gateway identity" && [[ "$(cat "$TMP/lib.out")" == gateway ]] ||
  mal "servicios_sin_commit: no selecciona solo al que corre una etiqueta flotante"
: >"$TMP/ps"
lib "verificar_imagen_desplegada app $COMMIT gateway" || mal "verificar_imagen_desplegada: falla cuando no hay contenedor (debe avisar)"
grep -q "no se pudo verificar" "$TMP/lib.out" || mal "verificar_imagen_desplegada: no avisa de que no hay contenedor"
if lib "servicios_sin_commit app gateway | grep ."; then mal "servicios_sin_commit: incluye un servicio sin contenedor"; fi

python3 "$ROOT/ops/scaffold/check-deploy-preflight.py" "$ROOT" || FALLOS=1

# --- enganchados en los dos despliegues ---------------------------------------------------
D="$ROOT/scripts/deploy-ecr.sh"
[[ "$(grep -c '^  preparar_servidor$' "$D")" == 2 ]] || mal "deploy-ecr.sh: preparar_servidor no esta en los dos transportes"
[[ "$(grep -c '^  esperar_sanos$' "$D")" == 2 ]] || mal "deploy-ecr.sh: esperar_sanos no esta en los dos transportes"
R="$ROOT/.github/workflows/release.yml"
for marca in 'esperar-sanos.sh" --proyecto app' 'claves-env.sh" .env' 'pgbouncer-userlist.sh --write'; do
  grep -q -F "$marca" "$R" || mal "release.yml: falta el paso con '$marca'"
done

if [[ $FALLOS -ne 0 ]]; then
  echo "check-deploy-preflight: FALLA" >&2
  exit 1
fi
echo "  OK: el despliegue genera el userlist que falta, avisa de claves ausentes, rechaza marcadores de .env.example, ata cada servicio a la imagen de su commit en los dos transportes y no da por bueno un servicio que no arranca."
