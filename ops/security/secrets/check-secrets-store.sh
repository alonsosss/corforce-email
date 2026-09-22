#!/usr/bin/env bash
# Prueba de extremo a extremo del almacen cifrado (ops/security/secrets/store.sh y los guiones
# que lo usan): sin red, con gpg real (lo instala ya la plantilla del servidor, igual que
# ops/backup), todo en un directorio temporal que se borra al salir. Nada de esto toca el
# almacen real ni genera un secreto que quede en el repositorio.
#
# make check-secrets-store (dentro de make checks).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
command -v gpg >/dev/null 2>&1 || { echo "  se omite: falta gpg" >&2; exit 0; }
command -v openssl >/dev/null 2>&1 || { echo "  se omite: falta openssl" >&2; exit 0; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAIL=0
falla() { echo "  FALLA: $*" >&2; FAIL=1; }
paso() { echo "  OK: $*"; }

STORE="$TMP/store.json.gpg"
FRASE="$TMP/passphrase"
K1="$TMP/keys.txt"
K2="$TMP/keys-db.txt"
printf 'REQUERIDA\nOPCIONAL?\n' > "$K1"
printf 'DB_REQUERIDA\n' > "$K2"

correr() {
  SECRETS_STORE_FILE="$STORE" SECRETS_STORE_PASSPHRASE_FILE="$FRASE" \
  SECRET_KEYS_FILE="$K1" SECRET_KEYS_DB_FILE="$K2" "$@"
}

# ── 1. init-store crea el almacen y la frase ────────────────────────────────────────────────
if correr bash "$SCRIPT_DIR/init-store.sh" >/dev/null 2>&1; then
  paso "init-store crea el almacen"
else
  falla "init-store deberia crear el almacen vacio"
fi
[[ -f "$STORE" ]] || falla "init-store no dejo $STORE"
[[ -f "$FRASE" ]] || falla "init-store no dejo $FRASE"
[[ "$(stat -c '%a' "$FRASE" 2>/dev/null)" == "600" ]] || falla "la frase deberia quedar en 0600"

# init-store no reescribe un almacen que ya existe. (pipefail: se captura la salida antes de
# comprobarla, para no perder el exit de init-store bajo un pipe con grep).
MSG="$(correr bash "$SCRIPT_DIR/init-store.sh" 2>&1)"
if grep -q "ya existe" <<<"$MSG"; then
  paso "init-store no toca un almacen existente"
else
  falla "init-store deberia negarse a pisar un almacen existente: $MSG"
fi

# ── 2. add-secret anade sin imprimir el valor ───────────────────────────────────────────────
SALIDA="$(correr env VALOR="valor-secreto-de-prueba-001" bash "$SCRIPT_DIR/add-secret.sh" REQUERIDA --apply 2>&1)"
if grep -qF "valor-secreto-de-prueba-001" <<<"$SALIDA"; then
  falla "add-secret imprimio el valor en su salida"
else
  paso "add-secret no imprime el valor en su salida"
fi
correr env VALOR="valor-opcional-002" bash "$SCRIPT_DIR/add-secret.sh" OPCIONAL --apply >/dev/null 2>&1
correr env VALOR="valor-db-003" bash "$SCRIPT_DIR/add-secret.sh" DB_REQUERIDA --apply >/dev/null 2>&1

# Una clave no declarada en ninguno de los dos ficheros se rechaza.
MSG="$(correr env VALOR="x" bash "$SCRIPT_DIR/add-secret.sh" NO_DECLARADA --apply 2>&1)"
if grep -q "no esta en secret-keys" <<<"$MSG"; then
  paso "add-secret rechaza una clave no declarada"
else
  falla "add-secret deberia rechazar una clave que no esta en ninguna lista: $MSG"
fi

# ── 3. fetch-secrets: todo o nada ───────────────────────────────────────────────────────────
OUT="$TMP/secrets.env"; OUT_DB="$TMP/secrets-db.env"
rm -f "$OUT" "$OUT_DB"
if SECRETS_ENV_FILE="$OUT" SECRETS_DB_ENV_FILE="$OUT_DB" correr bash "$SCRIPT_DIR/fetch-secrets.sh" >/dev/null 2>&1; then
  paso "fetch-secrets materializa con todas las obligatorias presentes"
else
  falla "fetch-secrets deberia tener exito con REQUERIDA y DB_REQUERIDA puestas"
fi
[[ -f "$OUT" && -f "$OUT_DB" ]] || falla "fetch-secrets no dejo los dos ficheros"
grep -q '^REQUERIDA=valor-secreto-de-prueba-001$' "$OUT" 2>/dev/null || falla "secrets.env no trae REQUERIDA"
grep -q '^OPCIONAL=valor-opcional-002$' "$OUT" 2>/dev/null || falla "secrets.env no trae la opcional presente"
grep -q '^DB_REQUERIDA=valor-db-003$' "$OUT_DB" 2>/dev/null || falla "secrets-db.env no trae DB_REQUERIDA"
[[ "$(stat -c '%a' "$OUT" 2>/dev/null)" == "600" ]] || falla "secrets.env deberia quedar en 0600"

# Falta una obligatoria en un almacen nuevo: falla todo o nada, sin dejar ficheros.
STORE2="$TMP/store2.json.gpg"; FRASE2="$TMP/passphrase2"
SECRETS_STORE_FILE="$STORE2" SECRETS_STORE_PASSPHRASE_FILE="$FRASE2" bash "$SCRIPT_DIR/init-store.sh" >/dev/null 2>&1
rm -f "$TMP/out-incompleto.env" "$TMP/out-incompleto-db.env"
MSG="$(SECRETS_STORE_FILE="$STORE2" SECRETS_STORE_PASSPHRASE_FILE="$FRASE2" SECRET_KEYS_FILE="$K1" \
   SECRET_KEYS_DB_FILE="$K2" SECRETS_ENV_FILE="$TMP/out-incompleto.env" \
   SECRETS_DB_ENV_FILE="$TMP/out-incompleto-db.env" bash "$SCRIPT_DIR/fetch-secrets.sh" 2>&1)"
if grep -q "faltan secretos" <<<"$MSG"; then
  paso "fetch-secrets falla todo o nada si falta una obligatoria"
else
  falla "fetch-secrets deberia fallar con el almacen vacio: $MSG"
fi
[[ -f "$TMP/out-incompleto.env" ]] && falla "fetch-secrets no deberia dejar ficheros a medias"

# ── 4. frase incorrecta y almacen corrompido: mensaje claro, no una traza de gpg ────────────
FRASE_MALA="$TMP/passphrase-mala"
echo "esta-frase-no-es-la-correcta-del-almacen" > "$FRASE_MALA"; chmod 600 "$FRASE_MALA"
MSG="$(SECRETS_STORE_FILE="$STORE" SECRETS_STORE_PASSPHRASE_FILE="$FRASE_MALA" SECRET_KEYS_FILE="$K1" \
   SECRET_KEYS_DB_FILE="$K2" SECRETS_ENV_FILE="$TMP/x.env" SECRETS_DB_ENV_FILE="$TMP/x-db.env" \
   bash "$SCRIPT_DIR/fetch-secrets.sh" 2>&1)"
if grep -q "no se pudo descifrar" <<<"$MSG" && ! grep -qi "gpg:" <<<"$MSG"; then
  paso "frase incorrecta: mensaje claro, sin traza de gpg"
else
  falla "el mensaje de frase incorrecta no es el esperado: $MSG"
fi

CORRUPTO="$TMP/store-corrupto.json.gpg"
printf 'esto-no-es-un-fichero-gpg' > "$CORRUPTO"
MSG="$(SECRETS_STORE_FILE="$CORRUPTO" SECRETS_STORE_PASSPHRASE_FILE="$FRASE" SECRET_KEYS_FILE="$K1" \
   SECRET_KEYS_DB_FILE="$K2" SECRETS_ENV_FILE="$TMP/y.env" SECRETS_DB_ENV_FILE="$TMP/y-db.env" \
   bash "$SCRIPT_DIR/fetch-secrets.sh" 2>&1)"
if grep -q "no se pudo descifrar" <<<"$MSG" && ! grep -qi "gpg:" <<<"$MSG"; then
  paso "almacen corrompido: mensaje claro, sin traza de gpg"
else
  falla "el mensaje de almacen corrompido no es el esperado: $MSG"
fi

# ── 5. rotate-key preserva las demas claves ─────────────────────────────────────────────────
K3="$TMP/keys-rotacion.txt"
printf 'ACTIVA\nVIEJAS?\n' > "$K3"
correr env SECRET_KEYS_FILE="$K3" VALOR="$(openssl rand -hex 32)" bash "$SCRIPT_DIR/add-secret.sh" ACTIVA --apply >/dev/null 2>&1
SECRETS_STORE_FILE="$STORE" SECRETS_STORE_PASSPHRASE_FILE="$FRASE" SECRET_KEYS_FILE="$K3" \
  bash "$SCRIPT_DIR/rotate-key.sh" ACTIVA VIEJAS --apply >/dev/null 2>&1
DESPUES="$(correr bash -c ". '$SCRIPT_DIR/store.sh' && store_leer_json")"
if python3 -c "
import json,sys
d=json.loads('''$DESPUES''')
assert d.get('REQUERIDA') == 'valor-secreto-de-prueba-001', 'REQUERIDA se perdio'
assert 'ACTIVA' in d and len(d['ACTIVA']) == 64, 'ACTIVA no quedo con 64 hex'
assert 'VIEJAS' in d and len(d['VIEJAS']) == 64, 'VIEJAS no recibio la llave anterior'
" 2>/tmp/rotacion-check.err; then
  paso "rotate-key rota preservando las demas claves"
else
  falla "rotate-key no dejo el almacen como se esperaba: $(cat /tmp/rotacion-check.err 2>/dev/null)"
fi
rm -f /tmp/rotacion-check.err

# ── 6. push-secrets: simulacion no toca nada, --apply migra y limpia el .env ────────────────
STORE3="$TMP/store3.json.gpg"; FRASE3="$TMP/passphrase3"
SECRETS_STORE_FILE="$STORE3" SECRETS_STORE_PASSPHRASE_FILE="$FRASE3" bash "$SCRIPT_DIR/init-store.sh" >/dev/null 2>&1
ENV_PRUEBA="$TMP/fake.env"
printf 'REQUERIDA=valor-de-env-1\nDB_REQUERIDA=valor-de-env-2\nAPP_PORT=8080\n' > "$ENV_PRUEBA"
cp "$ENV_PRUEBA" "$ENV_PRUEBA.orig"
SECRETS_STORE_FILE="$STORE3" SECRETS_STORE_PASSPHRASE_FILE="$FRASE3" SECRET_KEYS_FILE="$K1" \
  SECRET_KEYS_DB_FILE="$K2" bash "$SCRIPT_DIR/push-secrets.sh" "$ENV_PRUEBA" >/dev/null 2>&1
diff -q "$ENV_PRUEBA" "$ENV_PRUEBA.orig" >/dev/null 2>&1 && paso "push-secrets sin --apply no modifica el .env" \
  || falla "push-secrets sin --apply no deberia tocar el .env"

SECRETS_STORE_FILE="$STORE3" SECRETS_STORE_PASSPHRASE_FILE="$FRASE3" SECRET_KEYS_FILE="$K1" \
  SECRET_KEYS_DB_FILE="$K2" bash "$SCRIPT_DIR/push-secrets.sh" "$ENV_PRUEBA" --apply >/dev/null 2>&1
if grep -q '^REQUERIDA=' "$ENV_PRUEBA" 2>/dev/null; then
  falla "push-secrets --apply deberia quitar REQUERIDA del .env"
elif grep -q '^APP_PORT=8080$' "$ENV_PRUEBA" 2>/dev/null; then
  paso "push-secrets --apply migra los secretos y conserva la configuracion no sensible"
else
  falla "push-secrets --apply no dejo el .env como se esperaba"
fi

if SECRETS_STORE_FILE="$STORE3" SECRETS_STORE_PASSPHRASE_FILE="$FRASE3" SECRET_KEYS_FILE="$K1" \
   SECRET_KEYS_DB_FILE="$K2" SECRETS_ENV_FILE="$TMP/migrado.env" SECRETS_DB_ENV_FILE="$TMP/migrado-db.env" \
   bash "$SCRIPT_DIR/fetch-secrets.sh" >/dev/null 2>&1 \
   && grep -q '^REQUERIDA=valor-de-env-1$' "$TMP/migrado.env"; then
  paso "lo migrado por push-secrets se materializa igual con fetch-secrets"
else
  falla "fetch-secrets no recupero lo que push-secrets migro"
fi

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: el almacen cifrado de secretos se comporta como documenta el README."
