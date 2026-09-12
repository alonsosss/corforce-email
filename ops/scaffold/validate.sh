#!/usr/bin/env bash
# Validador de consistencia de la plataforma. Detecta las clases de error tipicas al
# escalar:
#   - colision de puertos en .env.example
#   - modulos gateados en el gateway que no tienen permisos sembrados (015) o no estan
#     en el catalogo de modulos -> el gateo/menu no reconoceria el modulo.
#   - servicios que leen tablas de otro contexto, streams solapados, imagenes sin fijar
# Salida distinta de 0 si hay problemas (apto para CI).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FAIL=0

echo "== 1. Colision de puertos de servicio (.env.example) =="
# Se excluyen puertos de infraestructura (Postgres/Redis/NATS/PgBouncer), que legitimamente
# se repiten (p. ej. POSTGRES_PORT y DB_UPSTREAM_PORT ambos 5432).
DUPS=$(grep -oE '^[A-Z0-9_]+_PORT=[0-9]+' "$ROOT/.env.example" 2>/dev/null \
  | grep -vE '^(POSTGRES|DB_UPSTREAM|REDIS|NATS|PGBOUNCER)_PORT=' \
  | sed -E 's/^[A-Z0-9_]+_PORT=//' | sort | uniq -d)
if [[ -n "$DUPS" ]]; then
  echo "  FALLA: puertos de servicio duplicados:"; echo "$DUPS" | sed 's/^/    - /'; FAIL=1
else
  echo "  OK: sin colisiones de puerto de servicio."
fi

echo "== 2. Coherencia de modulos gateados (gateway -> permisos -> catalogo de modulos) =="
ROUTES="$ROOT/services/gateway/routes.json"
# El catalogo se siembra en 001 y se amplia en migraciones posteriores (un modulo nuevo
# trae la suya); los permisos los siembra cada servicio en su propia migracion de registro.
# Rutas con espacios: se recorren como arrays, nunca por expansion de palabras.
mapfile -t CAT_FILES < <(grep -rlE "INSERT INTO organization.module_catalog" "$ROOT/migrations/registry/"*.sql)
mapfile -t PERM_FILES < <(grep -rlE "INSERT INTO access_control.permissions" "$ROOT/migrations/registry/"*.sql)
# modulos = valor de "module" de cada ruta de la tabla del gateway
MODULES=$(python3 -c '
import json,sys
t=json.load(open(sys.argv[1]))
print("\n".join(sorted({r["module"] for r in t["routes"] if r.get("module")})))' "$ROUTES")
for m in $MODULES; do
  if ! grep -qhE "'${m}'" -- "${PERM_FILES[@]}"; then
    echo "  FALLA: modulo '$m' gateado pero SIN permisos sembrados en migrations/registry"; FAIL=1
  fi
  # Los modulos del plano de control no se contratan por empresa: no van en el catalogo.
  # Los de correo si, y ahi la relacion es por permission_modules del catalogo.
  if ! grep -qhE "\"${m}\"|'${m}'" -- "${CAT_FILES[@]}"; then
    echo "  AVISO: modulo '$m' gateado y fuera del catalogo de modulos (no habilitable por empresa)"
  fi
done
[[ $FAIL -eq 0 ]] && echo "  OK: todos los modulos gateados tienen permisos sembrados."

echo "== 3. Acoplamiento cross-schema congelado (R1) =="
if ! bash "$ROOT/ops/scaffold/check-coupling.sh"; then
  FAIL=1
fi

echo "== 4. Streams de JetStream sin solapamiento =="
if ! bash "$ROOT/ops/scaffold/check-streams.sh"; then
  FAIL=1
fi

echo "== 5. Imagenes fijadas a una version concreta =="
if ! bash "$ROOT/ops/scaffold/check-base-images.sh"; then
  FAIL=1
fi

echo "== 6. Aridad de los INSERT =="
if ! bash "$ROOT/ops/scaffold/check-sql-arity.sh"; then
  FAIL=1
fi

echo ""
if [[ $FAIL -ne 0 ]]; then
  echo "VALIDACION: FALLA"; exit 1
fi
echo "VALIDACION: OK"
