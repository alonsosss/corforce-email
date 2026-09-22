#!/usr/bin/env bash
# Resuelve el almacen cifrado de secretos: un JSON con todas las claves de secret-keys.txt y
# secret-keys-db.txt, cifrado con gpg simetrico (mismo patron que ops/backup/destino-externo.sh:
# AES-256, frase por --passphrase-fd 0, nunca en un argumento de linea de comandos).
#
# Lo sourcean fetch-secrets.sh, push-secrets.sh, add-secret.sh, rotate-key.sh e init-store.sh. Una
# sola implementacion del cifrado y la lectura de la frase: cambiar el algoritmo o el sitio donde
# vive el almacen se hace aqui una vez, no en cada script.
#
# SECRETS_STORE_PASSPHRASE_FILE  frase de descifrado, 0600 del usuario que despliega (por
#                                 defecto /opt/core-force-mail/secrets/passphrase). NUNCA en el
#                                 .env ni en un argumento visible en `ps`.
# SECRETS_STORE_FILE           fichero cifrado; por defecto store.json.gpg EN EL MISMO DIRECTORIO que
#                              la frase, fuera de cualquier arbol que sincronice el despliegue. No
#                              puede vivir junto a estos scripts: el servidor tiene DOS copias de
#                              ops/security/secrets (la de la plataforma en /opt/core-force-mail/app y
#                              la de los motores en /opt/core-force-mail/mail-src, que rellena
#                              scripts/deploy-mail.sh), y un almacen relativo al script solo existia
#                              en una de ellas: el despliegue de un motor abortaba por "no existe el
#                              almacen" (comprobado en produccion el 2026-09-21).
#
# Sin `set -e`: se sourcea desde scripts con sus propias opciones.

STORE_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STORE_PASSPHRASE_FILE="${SECRETS_STORE_PASSPHRASE_FILE:-/opt/core-force-mail/secrets/passphrase}"
STORE_FILE="${SECRETS_STORE_FILE:-$(dirname "$STORE_PASSPHRASE_FILE")/store.json.gpg}"
STORE_FRASE_MINIMA=20

# _store_frase: lee la frase del fichero, exigiendo que sea 0600 del usuario que ejecuta. Una
# frase legible por otros o de un fichero ajeno no protege nada: cualquiera con esa lectura
# descifra el almacen entero. Se imprime por stdout para alimentar --passphrase-fd 0; nunca se
# exporta ni queda en el entorno de un proceso hijo mas que el propio gpg.
_store_frase() {
  [[ -f "$STORE_PASSPHRASE_FILE" ]] || {
    echo "el almacen no tiene frase en $STORE_PASSPHRASE_FILE (corre init-store.sh)" >&2
    return 1
  }
  local modo dueno
  modo="$(stat -c '%a' "$STORE_PASSPHRASE_FILE" 2>/dev/null)" || { echo "no se pudo leer los permisos de $STORE_PASSPHRASE_FILE" >&2; return 1; }
  dueno="$(stat -c '%u' "$STORE_PASSPHRASE_FILE" 2>/dev/null)"
  if [[ "$dueno" != "$(id -u)" || "$modo" != "600" ]]; then
    echo "$STORE_PASSPHRASE_FILE debe ser 0600 del usuario $(id -un) (es $modo de uid $dueno); no se usa" >&2
    return 1
  fi
  local frase
  frase="$(cat "$STORE_PASSPHRASE_FILE")"
  [[ ${#frase} -ge $STORE_FRASE_MINIMA ]] || {
    echo "la frase de $STORE_PASSPHRASE_FILE tiene menos de $STORE_FRASE_MINIMA caracteres; genera una con init-store.sh" >&2
    return 1
  }
  printf '%s' "$frase"
}

# _store_gpg <argumentos de gpg>: homedir efimero (no se usa el llavero del usuario) y la frase
# por la entrada estandar, que gpg lee y no queda en la lista de procesos.
_store_gpg() {
  local home rc frase
  frase="$(_store_frase)" || return 1
  home="$(mktemp -d)" || return 1
  printf '%s' "$frase" | gpg --homedir "$home" --batch --yes --quiet --no-tty \
    --pinentry-mode loopback --passphrase-fd 0 "$@"
  rc=$?
  rm -rf "$home"
  return $rc
}

# store_leer_json [--permitir-vacio]: descifra el almacen y escribe el JSON por stdout. Sin
# --permitir-vacio, un almacen inexistente es un error (para fetch-secrets.sh, que necesita el
# almacen real). Con --permitir-vacio, un almacen inexistente devuelve "{}" (para add-secret.sh y
# push-secrets.sh, cuando el almacen todavia no se ha creado).
store_leer_json() {
  if [[ ! -f "$STORE_FILE" ]]; then
    if [[ "${1:-}" == "--permitir-vacio" ]]; then
      printf '{}'
      return 0
    fi
    echo "no existe el almacen $STORE_FILE (corre ops/security/secrets/init-store.sh)" >&2
    return 1
  fi
  local payload err_tmp rc
  err_tmp="$(mktemp)"
  payload="$(_store_gpg --decrypt "$STORE_FILE" 2>"$err_tmp")"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    # Nunca se imprime la traza de gpg: mezcla razones (frase incorrecta, fichero corrompido,
    # gpg no instalado) que solo confunden a quien opera. El mensaje aqui es la unica salida.
    echo "no se pudo descifrar $STORE_FILE." >&2
    echo "  Revisa que $STORE_PASSPHRASE_FILE tenga la frase correcta y que el fichero no este danado." >&2
    rm -f "$err_tmp"
    return 1
  fi
  rm -f "$err_tmp"
  printf '%s' "$payload"
}

# store_escribir_json: cifra el JSON que llega por stdin y lo publica de forma atomica. Comprueba
# que lo cifrado descifra al mismo contenido antes de reemplazar el almacen: un cifrado corrupto
# publicado sin esa verificacion deja el almacen entero irrecuperable.
store_escribir_json() {
  local json tmp_out rc descifrado
  json="$(cat)"
  [[ -f "$STORE_PASSPHRASE_FILE" ]] || {
    echo "no hay frase en $STORE_PASSPHRASE_FILE (corre ops/security/secrets/init-store.sh)" >&2
    return 1
  }
  mkdir -p "$(dirname "$STORE_FILE")"
  umask 077
  tmp_out="$(mktemp "${STORE_FILE}.XXXXXX")" || return 1
  # El JSON en claro se pasa por sustitucion de proceso (una tuberia anonima), nunca por un
  # fichero en disco: gpg lo lee como si fuera un fichero de entrada normal.
  _store_gpg --symmetric --cipher-algo AES256 --s2k-digest-algo SHA512 --s2k-count 65011712 \
    --compress-algo none -o "$tmp_out" <(printf '%s' "$json")
  rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "no se pudo cifrar el almacen (revisa $STORE_PASSPHRASE_FILE)" >&2
    rm -f "$tmp_out"
    return 1
  fi
  descifrado="$(_store_gpg --decrypt "$tmp_out" 2>/dev/null)"
  if [[ "$descifrado" != "$json" ]]; then
    echo "el cifrado del almacen no descifra al contenido esperado; no se publica" >&2
    rm -f "$tmp_out"
    return 1
  fi
  chmod 600 "$tmp_out"
  mv -f "$tmp_out" "$STORE_FILE"
}
