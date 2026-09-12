#!/usr/bin/env bash
# R1 (PLAN-MAESTRO-ARQUITECTURA): congela el acoplamiento de datos entre bounded contexts.
# Detecta referencias SQL a schemas AJENOS en el codigo Go de cada servicio y las compara
# contra la allowlist del acoplamiento historico sancionado (coupling-allowlist.txt).
# Un acceso cross-schema NUEVO (no listado) hace fallar el CI: el acoplamiento existente
# no puede crecer sin decision explicita. Retirar entradas de la allowlist al desacoplar.
#
# Heuristica: lineas de .go bajo services/*/internal/ con patron SQL
#   (FROM|JOIN|INTO|UPDATE) <schema>.<tabla>
# Los servicios SIN carpeta internal/ son los Python (intelligence, agent-sales,
# procurement-ai*, ai-*): en ellos se escanean los .py de todo el servicio con el
# mismo patron. No captura SQL construido dinamicamente (raro en el codigo actual).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ALLOW="$ROOT/ops/scaffold/coupling-allowlist.txt"
ALLOW_W="$ROOT/ops/scaffold/coupling-writes-allowlist.txt"

# Schemas conocidos (registro, celda y tenant), extraidos de migrations (CREATE SCHEMA).
SCHEMAS="organization identity access_control audit scheduler mail mail_security \
domains transactional templates suppression reputation contacts segments campaigns \
automations analytics billing policy"

# Schemas que posee cada servicio. El plano de control (identity/organization/
# access-control) comparte los esquemas del registro por diseno: son tres servicios sobre
# una sola base de plataforma y se leen entre si por sus tablas de catalogo.
owned_schemas() {
  case "$1" in
    identity|organization|access-control) echo "identity organization access_control" ;;
    # mail-auth es la mitad de autenticacion del directorio: verifica contrasenas de
    # buzon y de aplicacion que solo estan en mail.*; no tiene tablas propias.
    mail-directory|mail-auth) echo "mail" ;;
    domain-service) echo "domains" ;;
    # domain-service posee el esquema domains (no domain_service): dominios de correo,
    # verificacion DNS y custodia DKIM en la base de cada empresa.
    domain-service) echo "domains" ;;
    *) echo "${1//-/_}" ;;
  esac
}

FAIL=0
FOUND="$(mktemp)"; FOUND_W="$(mktemp)"; trap 'rm -f "$FOUND" "$FOUND_W"' EXIT

for d in "$ROOT/services"/*/; do
  svc="$(basename "$d")"
  # Servicios Go: el SQL vive bajo internal/. Servicios sin internal/ (Python): se
  # escanea el .py de todo el servicio, mismo patron y misma exclusion de vistas.
  if [ -d "${d}internal" ]; then
    scan_dir="${d}internal"; scan_glob='*.go'
  else
    scan_dir="$d"; scan_glob='*.py'
  fi
  owned=" $(owned_schemas "$svc") "
  for schema in $SCHEMAS; do
    case "$owned" in *" $schema "*) continue ;; esac
    # Leer la VISTA publicada de otro contexto (schema.v_*) no es lo mismo que leer su
    # tabla: la vista es un contrato que su dueño declara y sostiene, y la tabla es un
    # detalle interno que puede cambiar sin avisar. Solo lo segundo se cuenta como
    # acoplamiento. Por eso el patron excluye las referencias a v_*.
    # Los *_test.go quedan fuera: un test de integracion siembra las tablas de otro
    # contexto en su Postgres DESECHABLE para montar el escenario; eso no acopla a
    # produccion. Lo que si acopla es el codigo que se despliega.
    if grep -rqiP "(from|join|into|update)\s+${schema}\.(?!v_)" "$scan_dir" --include="$scan_glob" --exclude='*_test.go' --exclude='*_test.py' 2>/dev/null; then
      echo "$svc $schema" >> "$FOUND"
    fi
    # La ESCRITURA cruzada se mide aparte: leer la tabla de otro contexto se retira
    # publicando una vista, escribirla obliga a rehacer quien es el dueño del dato.
    if grep -rqiE "(insert into|update|delete from)[[:space:]]+${schema}\.[a-z_]" "$scan_dir" --include="$scan_glob" --exclude='*_test.go' --exclude='*_test.py' 2>/dev/null; then
      echo "$svc $schema" >> "$FOUND_W"
    fi
  done
done

sort -u "$FOUND" -o "$FOUND"

echo "== Acoplamiento cross-schema detectado vs allowlist =="
NEW=0
while read -r svc schema; do
  [ -z "$svc" ] && continue
  if ! grep -qE "^${svc}[[:space:]]+${schema}([[:space:]]|$)" "$ALLOW" 2>/dev/null; then
    echo "  FALLA: acceso NUEVO no sancionado: ${svc} -> ${schema}.*"
    echo "         (integrar por API/eventos, o agregar a coupling-allowlist.txt con justificacion)"
    NEW=1; FAIL=1
  fi
done < "$FOUND"
[ $NEW -eq 0 ] && echo "  OK: sin acoplamiento nuevo ($(wc -l < "$FOUND") accesos historicos sancionados)."

sort -u "$FOUND_W" -o "$FOUND_W"

echo "== Escrituras en schema ajeno vs allowlist =="
NEW_W=0
while read -r svc schema; do
  [ -z "$svc" ] && continue
  if ! grep -qE "^${svc}[[:space:]]+${schema}([[:space:]]|$)" "$ALLOW_W" 2>/dev/null; then
    echo "  FALLA: ESCRITURA nueva en schema ajeno: ${svc} -> ${schema}.*"
    echo "         (el dueño del schema debe exponer la operacion; sancionarla exige"
    echo "          justificacion en coupling-writes-allowlist.txt)"
    NEW_W=1; FAIL=1
  fi
done < "$FOUND_W"
[ $NEW_W -eq 0 ] && echo "  OK: sin escrituras nuevas ($(wc -l < "$FOUND_W") sancionadas)."

# Entradas de la allowlist que ya no existen en el codigo: avisar para retirarlas.
while read -r line; do
  case "$line" in ''|'#'*) continue ;; esac
  svc="$(echo "$line" | awk '{print $1}')"; schema="$(echo "$line" | awk '{print $2}')"
  if ! grep -qE "^${svc} ${schema}$" "$FOUND"; then
    echo "  AVISO: la allowlist tiene '${svc} ${schema}' pero ya no se detecta; retirar la entrada."
  fi
done < "$ALLOW" 2>/dev/null

exit $FAIL
