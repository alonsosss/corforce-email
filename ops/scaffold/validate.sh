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

echo "== 7. Gracia DKIM frente a la cola de Postfix =="
if ! bash "$ROOT/ops/scaffold/check-dkim-grace.sh"; then
  FAIL=1
fi

echo "== 8. Tamano de mensaje: Postfix, Rspamd y la cuarentena de mail-security =="
if ! bash "$ROOT/ops/scaffold/check-mail-size-limits.sh"; then
  FAIL=1
fi

echo "== 9. PgBouncer: TLS hacia la base y credenciales segun el entorno =="
if ! bash "$ROOT/ops/scaffold/check-pgbouncer-entrypoint.sh"; then
  FAIL=1
fi

echo "== 10. Despliegue: userlist de PgBouncer, claves del .env y servicios arrancados =="
if ! bash "$ROOT/ops/scaffold/check-deploy-preflight.sh"; then
  FAIL=1
fi

echo "== 11. Produccion autoalojada: perfil, TLS interno y proxy de borde =="
if ! bash "$ROOT/ops/scaffold/check-selfhosted-profile.sh"; then
  FAIL=1
fi

echo "== 12. Despliegue de los motores de correo: imagen por commit, candado, guardia y arranque =="
if ! bash "$ROOT/ops/scaffold/check-deploy-mail.sh"; then
  FAIL=1
fi

echo "== 13. Respaldos y acceso a la base desde ops/: herramienta por perfil, secretos por el entorno, copia externa y temporizadores =="
if ! bash "$ROOT/ops/scaffold/check-backups.sh"; then
  FAIL=1
fi

echo "== 14. Divergencia de deploy/mail frente a mailcow: registrada, explicada y con mutaciones que la rompen =="
if ! bash "$ROOT/ops/scaffold/check-upstream-ledger.sh"; then
  FAIL=1
fi

echo "== 15. Herramienta de entregabilidad: MX, SPF, DKIM, DMARC, PTR, SMTP y listas negras con un DNS simulado =="
if ! bash "$ROOT/ops/scaffold/check-deliverability-tool.sh"; then
  FAIL=1
fi

echo "== 16. Escaneo de las imagenes de los motores: el informe cuenta cada CVE una vez y falla ante entradas rotas =="
if ! bash "$ROOT/ops/scaffold/check-motor-scan.sh"; then
  FAIL=1
fi

echo "== 17. Proporcion entre pruebas y codigo Go: ningun servicio baja de su suelo y uno nuevo nace con pruebas =="
if ! bash "$ROOT/ops/scaffold/check-test-ratio.sh"; then
  FAIL=1
fi

echo "== 18. Agente de la cola de Postfix: sin dependencias ni shell, con vet y pruebas =="
if ! bash "$ROOT/ops/scaffold/check-queue-agent.sh"; then
  FAIL=1
fi

echo "== 19. Ejecutor de la migracion de buzones: sin dependencias ni shell, sin borrar en el origen, con vet y pruebas =="
if ! bash "$ROOT/ops/scaffold/check-migration-runner.sh"; then
  FAIL=1
fi

echo "== 19. Paneles de Grafana: JSON valido, origenes aprovisionados y metricas que existen =="
if ! bash "$ROOT/ops/scaffold/check-dashboards.sh"; then
  FAIL=1
fi

echo ""
if [[ $FAIL -ne 0 ]]; then
  echo "VALIDACION: FALLA"; exit 1
fi
echo "VALIDACION: OK"
