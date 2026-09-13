#!/usr/bin/env bash
# Comprueba que no queda ningun resto de las dos bases de las que nace este repositorio:
# la plataforma de referencia (modulo capitalpillar, prefijos erp_, sucursales, SUNAT) y la
# capa PHP/MySQL/SOGo de mailcow. Encontrar uno de estos terminos no es estilo: es codigo
# que asume otro producto y falla en silencio (una consulta contra una columna de
# sucursal que aqui no existe, una ruta que apunta a un servicio que no se copio).
#
# Se excluyen las carpetas que por definicion hablan de esas bases: la documentacion de
# la copia y los ficheros de configuracion de los motores, que conservan sus nombres.
#
# Tambien falla con una cuenta de AWS concreta en un ARN o en un registro de ECR: la
# cuenta sale de STS o de una variable al aplicar, y una fijada en el repositorio es la de
# otro producto o una fuga.
set -euo pipefail
cd "$(dirname "$0")/../.."

fallo=0
versionados="$(mktemp)"; trap 'rm -f "$versionados"' EXIT
git ls-files -z -co --exclude-standard > "$versionados"

buscar() {
  local patron="$1"; shift
  local hits
  hits="$(grep -rniE "$patron" "$@" 2>/dev/null | grep -v 'ops/scaffold/check-clean-copy.sh' || true)"
  if [[ -n "$hits" ]]; then
    echo "resto encontrado ($patron):" >&2
    echo "$hits" | sed 's/^/  /' >&2
    fallo=1
  fi
}

# Codigo y migraciones: nada de la base de referencia.
buscar 'capitalpillar|\berp\b|erp_|erp-' pkg services migrations ops/scaffold/*.sh ops/scaffold/eventcontracts ops/security/secrets ops/db ops/backup ops/ecr ops/observability ops/server-template ops/maintenance ops/aws ops/e2e pgbouncer scripts .github Makefile docker-compose.yml .env.example
buscar 'sunat|sucursal|\bsede\b|branch_id|branchscope|socio_id|employee_id' pkg services migrations
# Paquetes del frontend federado de la base de referencia (@cp/<paquete>): aqui no existen.
buscar '@cp/' pkg services migrations ops scripts .github web/src web/package.json Makefile docker-compose.yml
# Codigo Go y migraciones: nada de la capa que mailcow reemplaza.
buscar 'mysql|mariadb|sogo|phpfpm|mailcowauth' pkg services migrations

# Todos los ficheros versionados o por versionar, no solo el codigo. Se admiten el
# marcador de la documentacion de AWS (123456789012) y los de un solo digito repetido
# (000000000000), que usan los tests.
cuentas="$(xargs -0 -r grep -nIoE 'arn:aws[a-z-]*:[a-z0-9-]+:[a-z0-9-]*:[0-9]{12}:|\b[0-9]{12}\.dkr\.ecr\.' < "$versionados" 2>/dev/null \
  | grep -vE ':(123456789012|0{12}|1{12}|2{12}|3{12}|4{12}|5{12}|6{12}|7{12}|8{12}|9{12})[:.]' || true)"
if [[ -n "$cuentas" ]]; then
  echo "cuenta de AWS fijada en el repositorio (debe salir de STS o de una variable):" >&2
  echo "$cuentas" | sed 's/^/  /' >&2
  fallo=1
fi

if [[ $fallo -eq 0 ]]; then
  echo "clean-copy: sin restos"
fi
exit $fallo
