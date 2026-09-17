#!/usr/bin/env bash
# Guardarrail: PgBouncer solo relaja el TLS y las credenciales en desarrollo y pruebas.
#
# pgbouncer/entrypoint.sh decide, segun ENVIRONMENT, el modo TLS hacia la base y de donde
# salen las credenciales de los clientes. Aqui se ejecuta de verdad contra la plantilla del
# repositorio, con un pgbouncer falso, y se mira lo que renderiza en cada entorno:
#   - fuera de development|test: verify-full, userlist.txt obligatorio y legible, y cualquier
#     modo mas debil impide arrancar;
#   - en development|test: disable por defecto y, sin userlist.txt, uno efimero con el rol de
#     plataforma fuera del directorio montado desde el repositorio.
# Basta volver a fijar verify-full en la plantilla para que `make dev` deje de conectar, o un
# valor por defecto equivocado para que produccion vaya a la base sin TLS.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENTRY="$ROOT/pgbouncer/entrypoint.sh"
TEMPLATE="$ROOT/pgbouncer/pgbouncer.ini.template"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nexit 0\n' >"$TMP/bin/pgbouncer"
chmod +x "$TMP/bin/pgbouncer"
FALLOS=0
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

for marca in __DB_UPSTREAM_SSLMODE__ __AUTH_FILE__; do
  grep -q "$marca" "$TEMPLATE" || mal "la plantilla no usa $marca: el entorno ya no decide"
done

# preparar <caso> [userlist]: directorio limpio del caso, con userlist.txt si se pide.
preparar() {
  rm -rf "${TMP:?}/$1"
  mkdir -p "$TMP/$1"
  [[ "${2:-}" == userlist ]] && printf '"mail_admin" "del-almacen"\n' >"$TMP/$1/userlist.txt"
  return 0
}
# correr <caso> [VAR=valor...]: el entrypoint en un entorno limpio; deja el codigo en RC.
correr() {
  local caso=$1
  shift
  set +e
  env -i PATH="$TMP/bin:/usr/bin:/bin" \
    PGBOUNCER_TEMPLATE="$TEMPLATE" PGBOUNCER_RENDERED="$TMP/$caso/pgbouncer.ini" \
    PGBOUNCER_USERLIST="$TMP/$caso/userlist.txt" PGBOUNCER_LOCAL_USERLIST="$TMP/$caso/local.txt" \
    "$@" sh "$ENTRY" >"$TMP/$caso/salida" 2>&1
  RC=$?
  set -e
}
valor() { sed -n -E "s/^$1[[:space:]]*=[[:space:]]*//p" "$TMP/$2/pgbouncer.ini" 2>/dev/null | head -1; }
arranca() { [[ $RC -eq 0 ]] || mal "$1: no arranco ($(tr '\n' ' ' <"$TMP/$2/salida"))"; }
no_arranca() { [[ $RC -ne 0 ]] || mal "$1: arranco y no debia"; }

preparar prod userlist
correr prod ENVIRONMENT=production
arranca "produccion con userlist" prod
[[ "$(valor server_tls_sslmode prod)" == verify-full ]] || mal "produccion: TLS $(valor server_tls_sslmode prod), se esperaba verify-full"
[[ "$(valor auth_file prod)" == "$TMP/prod/userlist.txt" ]] || mal "produccion: no usa el userlist.txt montado"

preparar sin-entorno userlist
correr sin-entorno
arranca "sin ENVIRONMENT" sin-entorno
[[ "$(valor server_tls_sslmode sin-entorno)" == verify-full ]] || mal "sin ENVIRONMENT: TLS $(valor server_tls_sslmode sin-entorno), se esperaba verify-full"

preparar prod-sin-userlist
correr prod-sin-userlist ENVIRONMENT=production POSTGRES_PASSWORD=x
no_arranca "produccion sin userlist.txt (aunque haya POSTGRES_PASSWORD)" prod-sin-userlist
grep -q 'falta' "$TMP/prod-sin-userlist/salida" || mal "produccion sin userlist.txt: el mensaje no nombra la causa"

preparar staging-sin-userlist
correr staging-sin-userlist ENVIRONMENT=staging POSTGRES_PASSWORD=x
no_arranca "staging sin userlist.txt" staging-sin-userlist

for modo in disable allow prefer require verify-ca; do
  preparar "prod-$modo" userlist
  correr "prod-$modo" ENVIRONMENT=production DB_UPSTREAM_SSLMODE="$modo"
  no_arranca "produccion con DB_UPSTREAM_SSLMODE=$modo" "prod-$modo"
done

preparar dev
correr dev ENVIRONMENT=development POSTGRES_PASSWORD='clave"con-comillas'
arranca "desarrollo sin userlist.txt" dev
[[ "$(valor server_tls_sslmode dev)" == disable ]] || mal "desarrollo: TLS $(valor server_tls_sslmode dev), se esperaba disable"
[[ "$(valor auth_file dev)" == "$TMP/dev/local.txt" ]] || mal "desarrollo: no usa el userlist efimero"
[[ "$(cat "$TMP/dev/local.txt" 2>/dev/null)" == '"mail_admin" "clave""con-comillas"' ]] || mal "desarrollo: userlist efimero mal formado (comillas sin doblar)"
[[ "$(stat -c %a "$TMP/dev/local.txt" 2>/dev/null)" == 600 ]] || mal "desarrollo: el userlist efimero no es 0600"
[[ ! -e "$TMP/dev/userlist.txt" ]] || mal "desarrollo: escribio en el directorio montado"

preparar test
correr test ENVIRONMENT=test POSTGRES_PASSWORD=x
arranca "pruebas sin userlist.txt" test
[[ "$(valor server_tls_sslmode test)" == disable ]] || mal "pruebas: se esperaba disable"

preparar dev-montado userlist
correr dev-montado ENVIRONMENT=development POSTGRES_PASSWORD=x
arranca "desarrollo con userlist.txt" dev-montado
[[ "$(valor auth_file dev-montado)" == "$TMP/dev-montado/userlist.txt" ]] || mal "desarrollo: con userlist.txt montado debe usarlo"
[[ ! -e "$TMP/dev-montado/local.txt" ]] || mal "desarrollo: genero un userlist efimero habiendo uno montado"

preparar dev-sin-clave
correr dev-sin-clave ENVIRONMENT=development
no_arranca "desarrollo sin userlist.txt ni POSTGRES_PASSWORD" dev-sin-clave

preparar dev-estricto
correr dev-estricto ENVIRONMENT=development POSTGRES_PASSWORD=x DB_UPSTREAM_SSLMODE=verify-full
arranca "desarrollo con verify-full pedido" dev-estricto
[[ "$(valor server_tls_sslmode dev-estricto)" == verify-full ]] || mal "desarrollo: no respeta verify-full pedido"

preparar dev-invalido
correr dev-invalido ENVIRONMENT=development POSTGRES_PASSWORD=x DB_UPSTREAM_SSLMODE=nada
no_arranca "modo TLS no valido" dev-invalido

if [[ "$(id -u)" -ne 0 ]]; then
  preparar ilegible userlist
  chmod 000 "$TMP/ilegible/userlist.txt"
  correr ilegible ENVIRONMENT=production
  no_arranca "produccion con userlist.txt ilegible" ilegible
  chmod 600 "$TMP/ilegible/userlist.txt"
fi

if [[ $FALLOS -ne 0 ]]; then
  echo "check-pgbouncer-entrypoint: FALLA" >&2
  exit 1
fi
echo "  OK: PgBouncer exige verify-full y userlist.txt fuera de development|test, y en ellos arranca sin los dos."
