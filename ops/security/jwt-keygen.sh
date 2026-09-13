#!/usr/bin/env bash
# Genera un par Ed25519 para firmar el token de acceso de identity, con su kid.
#
# Por la salida estandar imprime solo configuracion NO secreta:
#   JWT_SIGNING_KID=<kid>
#   JWT_PUBLIC_KEY_ENTRY=<kid>:<publica>     (entrada que se anade a JWT_PUBLIC_KEYS)
# La clave PRIVADA queda en un fichero en memoria (tmpfs, 0600) con la forma
# JWT_SIGNING_KEY=<privada>, que es lo que lee add-secret.sh --desde-env. La privada no se
# imprime, no viaja como argumento de ningun proceso y nunca se escribe dentro del
# repositorio.
#
# Formatos: la privada es PKCS#8 DER y la publica SubjectPublicKeyInfo DER, las dos en
# base64 estandar de una sola linea (el almacen de secretos no admite saltos de linea).
# El kid es la fecha UTC y 8 caracteres hexadecimales al azar: 20260913-a1b2c3d4.
#
# Uso:
#   ops/security/jwt-keygen.sh                      # privada en /dev/shm/core-force-mail/jwt-signing-key.<kid>
#   ops/security/jwt-keygen.sh --privada <fichero>  # otro destino, siempre fuera del repositorio
#
# Pasos de alta y rotacion en docs/Operacion_Despliegue.md (2. Secretos).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
command -v openssl >/dev/null || { echo "jwt-keygen: falta openssl (1.1.1 o posterior)" >&2; exit 1; }

KID="$(date -u +%Y%m%d)-$(openssl rand -hex 4)"
DESTINO="/dev/shm/core-force-mail/jwt-signing-key.$KID"
while [ $# -gt 0 ]; do
  case "$1" in
    --privada) DESTINO="${2:?--privada necesita un fichero}"; shift 2 ;;
    *) echo "opcion desconocida: $1" >&2; exit 1 ;;
  esac
done

DIR="$(realpath -m "$(dirname "$DESTINO")")"
case "$DIR/" in
  "$ROOT/"*) echo "jwt-keygen: la clave privada no se escribe dentro del repositorio ($DIR)" >&2; exit 1 ;;
esac
if [ -e "$DESTINO" ]; then
  echo "jwt-keygen: $DESTINO ya existe; no se sobrescribe" >&2
  exit 1
fi

umask 077
mkdir -p "$DIR"

PRIVADA="$(openssl genpkey -algorithm ed25519 -outform DER | openssl base64 -A)"
PUBLICA="$(printf '%s' "$PRIVADA" | openssl base64 -d -A | openssl pkey -inform DER -pubout -outform DER | openssl base64 -A)"
if [ -z "$PRIVADA" ] || [ -z "$PUBLICA" ]; then
  echo "jwt-keygen: openssl no genero el par" >&2
  exit 1
fi

printf 'JWT_SIGNING_KEY=%s\n' "$PRIVADA" > "$DESTINO"
PRIVADA=""

printf 'JWT_SIGNING_KID=%s\n' "$KID"
printf 'JWT_PUBLIC_KEY_ENTRY=%s:%s\n' "$KID" "$PUBLICA"
{
  echo
  echo "Clave privada en $DESTINO (0600). Orden de publicacion:"
  echo "  1. Anadir la entrada publica a JWT_PUBLIC_KEYS en el .env y recrear el gateway."
  echo "  2. ops/security/secrets/add-secret.sh JWT_SIGNING_KEY --desde-env $DESTINO --apply"
  echo "  3. shred -u $DESTINO"
  echo "  4. JWT_SIGNING_KID=$KID en el .env y recrear identity."
} >&2
