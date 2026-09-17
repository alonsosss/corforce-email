#!/usr/bin/env bash
# Guardarrail: el despliegue no da por bueno un servicio que no arranca.
#
# Ejecuta de verdad las tres piezas que rodean la recreacion en el servidor y comprueba que
# siguen enganchadas en los dos caminos de despliegue (scripts/deploy-ecr.sh y release.yml):
#   - ops/maintenance/esperar-sanos.sh, con un docker falso: sano, estable sin chequeo, en bucle
#     de reinicios, sin contenedor y sin salud;
#   - ops/maintenance/claves-env.sh: nombra las claves ausentes y nunca un valor;
#   - ops/db/pgbouncer-userlist.sh --ensure: genera si falta y no reescribe uno distinto.
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
echo "  OK: el despliegue genera el userlist que falta, avisa de claves ausentes y no da por bueno un servicio que no arranca."
