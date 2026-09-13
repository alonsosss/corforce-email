#!/usr/bin/env bash
# Comprueba que no queda ningun resto de las dos bases de las que nace este repositorio:
# la plataforma de referencia (modulo capitalpillar, prefijos erp_, sucursales, SUNAT) y la
# capa PHP/MySQL/SOGo de mailcow. Encontrar uno de estos terminos no es estilo: es codigo
# que asume otro producto y falla en silencio (una consulta contra una columna de
# sucursal que aqui no existe, una ruta que apunta a un servicio que no se copio).
#
# Se excluyen las carpetas que por definicion hablan de esas bases: la documentacion de
# la copia y los ficheros de configuracion de los motores, que conservan sus nombres.
set -euo pipefail
cd "$(dirname "$0")/../.."

fallo=0

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

if [[ $fallo -eq 0 ]]; then
  echo "clean-copy: sin restos"
fi
exit $fallo
