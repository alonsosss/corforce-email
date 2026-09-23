#!/usr/bin/env bash
# Prueba de extremo a extremo del almacen en OpenBao (make check-openbao; necesita docker).
#
# Con la imagen fijada en docker-compose.yml, en un directorio temporal y en un puerto libre de
# loopback: instalar.sh desde cero y otra vez (idempotente), desbloqueo solo tras reiniciar, minimo
# privilegio de cada credencial, migrar.sh de gpg a OpenBao y de vuelta con los mismos ficheros
# materializados, add/remove sobre OpenBao, instantanea que restaura en una instancia desechable,
# y que la verificacion se niega a restaurar sobre una instancia inicializada.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SECRETS_SCRIPTS="$(cd "$DIR/../secrets" && pwd)"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "  se omite: docker no disponible" >&2
  exit 0
fi
command -v gpg >/dev/null || { echo "check-openbao: falta gpg" >&2; exit 1; }

TMP="$(mktemp -d)"
export OPENBAO_PROYECTO="cf-openbao-check-$$" OPENBAO_CONTENEDOR="cf-openbao-check-$$"
limpiar() {
  docker rm -f "$OPENBAO_CONTENEDOR" >/dev/null 2>&1 || true
  docker network rm "${OPENBAO_PROYECTO}_default" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap limpiar EXIT

fallos=0
paso() { echo "  OK: $*"; }
falla() { echo "  FALLA: $*" >&2; fallos=$((fallos + 1)); }

puerto_libre() {
  python3 - <<'PY'
import socket
for p in range(20000, 30000, 2):
    try:
        a = socket.socket(); a.bind(("127.0.0.1", p))
        b = socket.socket(); b.bind(("127.0.0.1", p + 1))
        a.close(); b.close(); print(p); break
    except OSError:
        continue
PY
}

export OPENBAO_PUERTO="$(puerto_libre)"
export OPENBAO_BASE="$TMP/base" SECRETS_DIR="$TMP/secrets"
export OPENBAO_ADDR="http://127.0.0.1:$OPENBAO_PUERTO" OPENBAO_CRED_DIR="$TMP/secrets/openbao"
export SECRETS_STORE_PASSPHRASE_FILE="$TMP/secrets/passphrase"
export SECRET_KEYS_FILE="$TMP/claves.txt" SECRET_KEYS_DB_FILE="$TMP/claves-db.txt"
printf 'CLAVE_A\nCLAVE_B\nOPCIONAL_C?\n' >"$SECRET_KEYS_FILE"
printf 'CLAVE_DB\n' >"$SECRET_KEYS_DB_FILE"
BAO=(python3 "$SECRETS_SCRIPTS/openbao.py")

# ── 1. instalacion desde cero, idempotente, desbloqueo tras reinicio ─────────────────────────
salida="$(bash "$DIR/instalar.sh" 2>&1)" || { echo "$salida" >&2; falla "instalar.sh desde cero"; exit 1; }
grep -q "token root revocado" <<<"$salida" && paso "instalar.sh inicializa y revoca el token root" || falla "instalar.sh no revoco el root"
[[ ! -e "$OPENBAO_CRED_DIR/root.token" ]] && paso "no queda token root en disco" || falla "queda root.token"
for rol in despliegue administracion respaldo; do
  [[ "$(stat -c '%a' "$OPENBAO_CRED_DIR/$rol.secret_id")" == 600 ]] || falla "$rol.secret_id no es 0600"
done
[[ "$(stat -c '%a %s' "$SECRETS_DIR/openbao-llave/desbloqueo.key")" == "600 32" ]] && paso "llave de desbloqueo de 32 bytes, 0600" || falla "llave de desbloqueo"
bash "$DIR/instalar.sh" >/dev/null 2>&1 && paso "instalar.sh es idempotente (y el reinicio desbloquea solo)" || falla "segunda corrida de instalar.sh"
docker inspect -f '{{.HostConfig.NetworkMode}} {{.Config.User}} {{.HostConfig.ReadonlyRootfs}}' "$OPENBAO_CONTENEDOR" |
  grep -q "^host $(id -u):$(id -g) true$" && paso "contenedor en red del host, sin root y de solo lectura" || falla "endurecimiento del contenedor"

# ── 2. minimo privilegio ─────────────────────────────────────────────────────────────────────
privilegios="$(cd "$SECRETS_SCRIPTS" && python3 - <<'PY'
import openbao as o
casos = [
    ("despliegue", "POST", "cf/data/plataforma", {"data": {"x": "y"}}),
    ("despliegue", "GET", "cf/metadata/plataforma", None),
    ("despliegue", "GET", "sys/storage/raft/snapshot", None),
    ("respaldo", "GET", "cf/data/plataforma", None),
    ("administracion", "POST", "sys/mounts/otro", {"type": "kv"}),
    ("administracion", "POST", "sys/policies/acl/otra", {"policy": 'path "*" { capabilities = ["sudo"] }'}),
    ("administracion", "GET", "sys/storage/raft/snapshot", None),
]
for rol, metodo, ruta, cuerpo in casos:
    with o.Sesion(rol) as s:
        try:
            o._peticion(metodo, ruta, token=s.token, cuerpo=cuerpo, crudo=True)
            print(f"PERMITIDO {rol} {metodo} {ruta}")
        except o.Fallo:
            pass
PY
)"
[[ -z "$privilegios" ]] && paso "cada credencial solo alcanza lo suyo" || falla "privilegio de mas: $privilegios"
OPENBAO_ADDR="http://10.0.0.1:$OPENBAO_PUERTO" "${BAO[@]}" salud >/dev/null 2>&1 && falla "el cliente acepta una direccion no loopback" || paso "el cliente solo habla con loopback"

# ── 3. migracion gpg -> OpenBao -> gpg ───────────────────────────────────────────────────────
openssl rand -base64 30 >"$SECRETS_STORE_PASSPHRASE_FILE"; chmod 600 "$SECRETS_STORE_PASSPHRASE_FILE"
bash "$SECRETS_SCRIPTS/init-store.sh" >/dev/null
for k in CLAVE_A CLAVE_B CLAVE_DB; do VALOR="valor-$k-$RANDOM" bash "$SECRETS_SCRIPTS/add-secret.sh" "$k" --apply >/dev/null; done
SECRETS_ENV_FILE="$TMP/gpg.env" SECRETS_DB_ENV_FILE="$TMP/gpg-db.env" bash "$SECRETS_SCRIPTS/fetch-secrets.sh" >/dev/null

bash "$DIR/migrar.sh" >/dev/null && [[ ! -f "$SECRETS_DIR/backend" ]] && paso "migrar.sh sin --apply no cambia de backend" || falla "la simulacion de migrar.sh escribio"
bash "$DIR/migrar.sh" --apply >/dev/null 2>&1 || falla "migrar.sh --apply"
[[ "$(cat "$SECRETS_DIR/backend" 2>/dev/null)" == openbao && -f "$SECRETS_DIR/store.json.gpg.pre-openbao" ]] &&
  paso "migrar.sh fija OpenBao y aparta el fichero gpg" || falla "estado tras migrar"
SECRETS_ENV_FILE="$TMP/bao.env" SECRETS_DB_ENV_FILE="$TMP/bao-db.env" bash "$SECRETS_SCRIPTS/fetch-secrets.sh" >/dev/null
cmp -s "$TMP/gpg.env" "$TMP/bao.env" && cmp -s "$TMP/gpg-db.env" "$TMP/bao-db.env" &&
  paso "fetch-secrets materializa lo mismo desde OpenBao" || falla "OpenBao materializa distinto"
bash "$SECRETS_SCRIPTS/init-store.sh" >/dev/null 2>&1 && falla "init-store.sh acepta crear un gpg con backend openbao" || paso "init-store.sh se niega con backend openbao"

salida="$(VALOR=secreto-opcional-no-se-imprime bash "$SECRETS_SCRIPTS/add-secret.sh" OPCIONAL_C --apply 2>&1)"
grep -q "verificada releyendo" <<<"$salida" && ! grep -q "secreto-opcional-no-se-imprime" <<<"$salida" &&
  paso "add-secret sobre OpenBao escribe, verifica y no imprime el valor" || falla "add-secret sobre OpenBao"
bash "$SECRETS_SCRIPTS/remove-secret.sh" OPCIONAL_C --apply >/dev/null 2>&1 && ! "${BAO[@]}" leer | grep -q OPCIONAL_C &&
  paso "remove-secret sobre OpenBao" || falla "remove-secret sobre OpenBao"
[[ "$("${BAO[@]}" versiones | wc -l)" -ge 3 ]] && paso "cada cambio queda como version" || falla "versiones"

bash "$DIR/migrar.sh" --volver-a-gpg --apply >/dev/null 2>&1 && [[ "$(cat "$SECRETS_DIR/backend")" == gpg ]] &&
  paso "migrar.sh --volver-a-gpg regenera el fichero y vuelve" || falla "vuelta a gpg"
bash "$DIR/migrar.sh" --apply >/dev/null 2>&1 || falla "segunda migracion a OpenBao"

# ── 4. instantanea: restaura en otra instancia y nunca sobre una inicializada ───────────────
"${BAO[@]}" instantanea "$TMP/openbao.snap" 2>/dev/null && paso "la credencial de respaldo saca la instantanea" || falla "instantanea"
bash "$DIR/verificar-instantanea.sh" "$TMP/openbao.snap" | grep -q "devuelve 3 secretos" &&
  paso "la instantanea restaura con la llave y devuelve los secretos" || falla "la instantanea no restaura"
"${BAO[@]}" verificar-instantanea "$TMP/openbao.snap" >/dev/null 2>&1 &&
  falla "verificar-instantanea restauro sobre la instancia viva" || paso "verificar-instantanea se niega sobre una instancia inicializada"
head -c 4000 "$TMP/openbao.snap" >"$TMP/rota.snap"
bash "$DIR/verificar-instantanea.sh" "$TMP/rota.snap" >/dev/null 2>&1 && falla "una instantanea truncada paso" || paso "una instantanea truncada falla"

# ── 5. sellado: mensaje claro, sin traza ─────────────────────────────────────────────────────
docker stop "$OPENBAO_CONTENEDOR" >/dev/null
salida="$(bash "$SECRETS_SCRIPTS/fetch-secrets.sh" 2>&1)" && falla "fetch-secrets con OpenBao parado no fallo" ||
  { grep -q "OpenBao no responde" <<<"$salida" && ! grep -q Traceback <<<"$salida" && paso "OpenBao parado: fetch-secrets falla con mensaje claro" || falla "mensaje con OpenBao parado: $salida"; }

if [[ $fallos -ne 0 ]]; then
  echo "check-openbao: $fallos fallos" >&2
  exit 1
fi
echo "  OK: el almacen en OpenBao se comporta como documenta docs/adr/0011."
