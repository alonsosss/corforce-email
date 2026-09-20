#!/usr/bin/env bash
# Guardarrail: PgBouncer solo relaja el TLS y las credenciales en desarrollo y pruebas.
#
# pgbouncer/entrypoint.sh decide, segun ENVIRONMENT, el modo TLS hacia la base y de donde
# salen las credenciales de los clientes. Aqui se ejecuta de verdad contra la plantilla del
# repositorio, con un pgbouncer falso, y se mira lo que renderiza en cada entorno:
#   - fuera de development|test: verify-full, userlist.txt obligatorio y legible, y cualquier
#     modo mas debil impide arrancar;
#   - en development|test: disable por defecto y, sin userlist.txt, uno efimero con el rol de
#     plataforma fuera del directorio montado desde el repositorio;
#   - la CA de verify-full: por defecto el bundle de RDS que viaja junto a la plantilla (y que el
#     repositorio versiona), DB_UPSTREAM_CA_FILE la sustituye (produccion autoalojada) y, con
#     verificacion, una CA ausente, que no es PEM o con una ruta no admitida impide arrancar.
# Basta volver a fijar verify-full en la plantilla para que `make dev` deje de conectar, o un
# valor por defecto equivocado para que produccion vaya a la base sin TLS.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENTRY="$ROOT/pgbouncer/entrypoint.sh"
BUNDLE="$ROOT/pgbouncer/rds-global-bundle.pem"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
# La plantilla y el bundle se copian juntos, como estan en /etc/pgbouncer del contenedor: la CA por
# defecto se busca junto a la plantilla, y la ruta del repositorio puede llevar espacios, que el
# entrypoint no admite en una ruta de CA.
mkdir -p "$TMP/etc"
TEMPLATE="$TMP/etc/pgbouncer.ini.template"
cp "$ROOT/pgbouncer/pgbouncer.ini.template" "$TEMPLATE"
[[ -f "$BUNDLE" ]] && cp "$BUNDLE" "$TMP/etc/rds-global-bundle.pem"
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nexit 0\n' >"$TMP/bin/pgbouncer"
chmod +x "$TMP/bin/pgbouncer"
FALLOS=0
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

for marca in __DB_UPSTREAM_SSLMODE__ __AUTH_FILE__ __DB_UPSTREAM_CA_FILE__; do
  grep -q "$marca" "$TEMPLATE" || mal "la plantilla no usa $marca: el entorno ya no decide"
done

# El bundle de RDS es la CA por defecto: tiene que existir, ser PEM y viajar en el git archive del
# despliegue (*.pem esta ignorado salvo esta excepcion).
if [[ ! -s "$BUNDLE" ]] || ! grep -q 'BEGIN CERTIFICATE' "$BUNDLE"; then
  mal "falta pgbouncer/rds-global-bundle.pem o no es PEM: verify-full contra RDS no verificaria nada"
elif git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1 && git -C "$ROOT" check-ignore -q "$BUNDLE"; then
  mal "pgbouncer/rds-global-bundle.pem esta ignorado por git: no viajaria al servidor"
fi
printf -- '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n' >"$TMP/ca-interna.pem"

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
[[ "$(valor server_tls_ca_file prod)" == "$TMP/etc/rds-global-bundle.pem" ]] ||
  mal "produccion: CA $(valor server_tls_ca_file prod), se esperaba el bundle de RDS junto a la plantilla"

preparar autoalojado userlist
correr autoalojado ENVIRONMENT=production DB_UPSTREAM_CA_FILE="$TMP/ca-interna.pem"
arranca "produccion con CA interna" autoalojado
[[ "$(valor server_tls_ca_file autoalojado)" == "$TMP/ca-interna.pem" ]] || mal "produccion: no usa DB_UPSTREAM_CA_FILE"
[[ "$(valor server_tls_sslmode autoalojado)" == verify-full ]] || mal "produccion con CA interna: se esperaba verify-full"

preparar ca-ausente userlist
correr ca-ausente ENVIRONMENT=production DB_UPSTREAM_CA_FILE="$TMP/no-existe.pem"
no_arranca "produccion con una CA que no existe" ca-ausente
grep -q 'DB_UPSTREAM_CA_FILE' "$TMP/ca-ausente/salida" || mal "produccion con CA ausente: el mensaje no nombra la causa"

printf 'no es un certificado\n' >"$TMP/no-pem.txt"
preparar ca-no-pem userlist
correr ca-no-pem ENVIRONMENT=production DB_UPSTREAM_CA_FILE="$TMP/no-pem.txt"
no_arranca "produccion con una CA que no es PEM" ca-no-pem

preparar ca-relativa userlist
correr ca-relativa ENVIRONMENT=production DB_UPSTREAM_CA_FILE="ca-interna.pem"
no_arranca "CA con ruta relativa" ca-relativa

# Un & en la ruta: sed lo sustituiria por el texto buscado y la plantilla apuntaria a otra CA sin
# ningun error. El fichero existe y es PEM: solo lo para la comprobacion de la ruta.
cp "$TMP/ca-interna.pem" "$TMP/ca&x.pem"
preparar ca-inyeccion userlist
correr ca-inyeccion ENVIRONMENT=production DB_UPSTREAM_CA_FILE="$TMP/ca&x.pem"
no_arranca "CA con caracteres que alteran la plantilla" ca-inyeccion

# client_idle_timeout: por defecto el de siempre (600), y el perfil autoalojado lo desactiva porque
# Dovecot mantiene conexiones largas por PgBouncer (con el corte, el primer inicio de sesion IMAP
# tras un rato sin uso fallaba con "client_idle_timeout"). Un valor que no sea un entero no arranca.
[[ "$(valor client_idle_timeout prod)" == 600 ]] ||
  mal "client_idle_timeout por defecto: $(valor client_idle_timeout prod), se esperaba 600"
preparar idle-cero userlist
correr idle-cero ENVIRONMENT=production PGBOUNCER_CLIENT_IDLE_TIMEOUT=0
arranca "client_idle_timeout desactivado" idle-cero
[[ "$(valor client_idle_timeout idle-cero)" == 0 ]] ||
  mal "PGBOUNCER_CLIENT_IDLE_TIMEOUT=0: se renderizo $(valor client_idle_timeout idle-cero)"
for malo in abc -1 "10;x" ""; do
  preparar idle-malo userlist
  correr idle-malo ENVIRONMENT=production PGBOUNCER_CLIENT_IDLE_TIMEOUT="$malo"
  if [[ -n "$malo" ]]; then no_arranca "PGBOUNCER_CLIENT_IDLE_TIMEOUT='$malo'" idle-malo; fi
done

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

preparar dev-sin-ca
correr dev-sin-ca ENVIRONMENT=development POSTGRES_PASSWORD=x DB_UPSTREAM_CA_FILE="$TMP/no-existe.pem"
arranca "desarrollo sin TLS y sin CA" dev-sin-ca
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
echo "  OK: PgBouncer exige verify-full, una CA legible y userlist.txt fuera de development|test, y en ellos arranca sin los tres."
