#!/usr/bin/env bash
# Guardarrail: las migraciones canonicas de tenant deben ser IDEMPOTENTES.
#
# Por que: el flujo real de este proyecto aplica a mano en produccion la migracion nueva y
# deja que el runner del servicio organization la aplique despues en los demas tenants. Si
# la migracion no tolera re-ejecutarse, ese tenant aborta en ella y se queda SIN las
# migraciones posteriores. Lo mismo ocurre con bases restauradas de un backup.
#
# Los archivos historicos que no cumplen viven en el allowlist (sus tenants ya los tienen
# registrados y no se re-ejecutan). Cualquier archivo NUEVO que no cumpla falla el CI.
#
# Uso: bash ops/scaffold/check-migrations.sh
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Por defecto las canonicas por empresa; CANON_DIR=migrations/cell/canonical para las de celda.
CANON_DIR="${CANON_DIR:-$ROOT/migrations/tenant/canonical}"
ALLOWLIST="$ROOT/ops/scaffold/migration-idempotency-allowlist.txt"

[ -d "$CANON_DIR" ] || { echo "No existe $CANON_DIR"; exit 1; }

# Detecta sentencias que fallan al re-ejecutarse. Analiza por SENTENCIA (no por linea):
# el estilo pg_dump parte "ALTER TABLE ONLY x" y "ADD CONSTRAINT y" en lineas distintas.
# Los cuerpos $$ ... $$ (bloques DO, funciones) se retiran antes: ahi la idempotencia se
# resuelve con manejo de excepciones, que es el patron aceptado.
#
# Tambien se acepta DROP CONSTRAINT IF EXISTS seguido de ADD CONSTRAINT con el MISMO
# nombre: es como se cambia un CHECK, es idempotente de verdad, y obligar a envolverlo en
# un bloque DO daria una migracion peor. Se exige el mismo nombre para que el DROP de una
# restriccion no tape el ADD de otra.
#
# Por lo mismo se acepta DROP VIEW|TYPE IF EXISTS seguido de su CREATE, con el mismo nombre
# y en ese orden. Re-ejecutarlo deja el objeto exactamente igual, y para una VISTA es
# ademas MEJOR que el CREATE OR REPLACE que este guardarrail recomendaba: replace falla si
# cambia la lista de columnas (42P16), que es justo el fallo que persigue el guardarrail 3
# de mas abajo. Para un TIPO es la unica forma, porque no admite OR REPLACE.
detect() {
  perl -0777 -ne '
    s/\$\$.*?\$\$/ /gs;               # cuerpos dollar-quoted
    s/^\s*--[^\n]*$//gm;              # comentarios de linea
    s/\/\*.*?\*\// /gs;               # comentarios de bloque
    my %dropped;
    while (/ALTER\s+TABLE\s+(?:ONLY\s+)?\S+\s+DROP\s+CONSTRAINT\s+IF\s+EXISTS\s+(\w+)/gi) {
      $dropped{lc $1} = 1;
    }
    # Vistas y tipos soltados ANTES de recrearse. Se acumula recorriendo las sentencias en
    # orden: un DROP posterior al CREATE no lo justifica.
    my %soltado;
    for my $stmt (split /;/) {
      $stmt =~ s/\s+/ /g;
      $stmt =~ s/^ //;
      next unless length $stmt;
      if ($stmt =~ /^DROP\s+(VIEW|TYPE)\s+IF\s+EXISTS\s+([\w.]+)/i) {
        $soltado{lc($1) . ":" . lc($2)} = 1;
        next;
      }
      if ($stmt =~ /^CREATE\s+(VIEW|TYPE)\s+([\w.]+)/i && $soltado{lc($1) . ":" . lc($2)}) {
        next;
      }
      # Los lookahead van pegados al literal para que un grupo opcional no pueda
      # retroceder y dar un falso positivo (p.ej. ADD COLUMN IF NOT EXISTS).
      if ($stmt =~ /^CREATE\s+(?:GLOBAL\s+|LOCAL\s+|TEMP(?:ORARY)?\s+|UNLOGGED\s+)*TABLE\s+(?!IF\s+NOT\s+EXISTS\b)/i
       || $stmt =~ /^CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?!(?:CONCURRENTLY\s+)?IF\s+NOT\s+EXISTS\b)/i
       || $stmt =~ /^CREATE\s+SCHEMA\s+(?!IF\s+NOT\s+EXISTS\b)/i
       || $stmt =~ /^CREATE\s+TYPE\s+/i
       || $stmt =~ /^CREATE\s+MATERIALIZED\s+VIEW\s+(?!IF\s+NOT\s+EXISTS\b)/i
       || $stmt =~ /^CREATE\s+VIEW\s+/i
       || ($stmt =~ /^ALTER\s+TABLE\s+(?:ONLY\s+)?\S+\s+ADD\s+(?!COLUMN\s+IF\s+NOT\s+EXISTS\b)/i
            && !($stmt =~ /ADD\s+CONSTRAINT\s+(\w+)/i && $dropped{lc $1}))
       || $stmt =~ /^DROP\s+(?:TABLE|SCHEMA|INDEX|TYPE|VIEW|MATERIALIZED\s+VIEW)\s+(?!IF\s+EXISTS\b)/i) {
        print "1"; exit 0;
      }
    }
  ' "$1"
}

offenders=()
while IFS= read -r file; do
  if [ -n "$(detect "$file")" ]; then
    offenders+=("${file#"$ROOT"/}")
  fi
done < <(find "$CANON_DIR" -name '*.sql' | sort)

if [ ! -f "$ALLOWLIST" ]; then
  printf '%s\n' "${offenders[@]}" > "$ALLOWLIST"
  echo "Allowlist creado con ${#offenders[@]} archivos historicos: $ALLOWLIST"
  exit 0
fi

new=()
for rel in "${offenders[@]}"; do
  grep -qxF "$rel" "$ALLOWLIST" || new+=("$rel")
done

if [ ${#new[@]} -gt 0 ]; then
  echo "::error::Migraciones canonicas NO idempotentes (deben tolerar re-ejecutarse):" >&2
  printf '  - %s\n' "${new[@]}" >&2
  cat >&2 <<'EOF'

Corrige con: CREATE TABLE IF NOT EXISTS, CREATE INDEX IF NOT EXISTS, CREATE SCHEMA IF NOT
EXISTS, CREATE OR REPLACE VIEW, ALTER TABLE ... ADD COLUMN IF NOT EXISTS, DROP ... IF
EXISTS. Para constraints y tipos, que no admiten IF NOT EXISTS:

  DO $$ BEGIN
    ALTER TABLE x ADD CONSTRAINT y ...;
  EXCEPTION WHEN duplicate_object THEN NULL; END $$;

El runner de organization aplica estas migraciones a cada tenant: una que no tolere
re-ejecutarse deja al tenant sin las migraciones posteriores.
EOF
  exit 1
fi

stale=0
while IFS= read -r rel; do
  [ -z "$rel" ] && continue
  if [ ! -f "$ROOT/$rel" ]; then
    echo "aviso: '$rel' esta en el allowlist pero ya no existe" >&2
    stale=1
  fi
done < "$ALLOWLIST"
[ "$stale" -eq 1 ] && echo "aviso: depura $ALLOWLIST" >&2

# ── Guardarrail 2: orden entre modulos ──────────────────────────────────────
#
# Las canonicas corren en orden numerico sobre TODOS los modulos, asi que una
# migracion puede leer tablas de otro. Lo que no puede es leer un esquema que se
# crea en una migracion POSTERIOR: ahi aborta, y el tenant se queda sin ella y
# sin todas las siguientes.
#
# Hoy no ocurre en ningun archivo. Se comprueba para que siga siendo cierto: es
# el tipo de fallo que solo aparece al provisionar un tenant nuevo, cuando ya
# esta en produccion. Una guarda con to_regclass exime al archivo, porque
# entonces comprueba la tabla en vez de darla por hecha.
python3 - "$CANON_DIR" <<'PYEOF'
import glob, os, re, sys

canon = sorted(glob.glob(os.path.join(sys.argv[1], "*", "*.sql")))
creado = {}
for f in canon:
    n = int(re.match(r"(\d+)", os.path.basename(f)).group(1))
    txt = open(f, encoding="utf-8", errors="ignore").read()
    for m in re.finditer(r"CREATE SCHEMA(?:\s+IF NOT EXISTS)?\s+([a-z_]+)", txt, re.I):
        e = m.group(1).lower()
        creado[e] = min(creado.get(e, 10**9), n)

fallos = []
for f in canon:
    base = os.path.basename(f)
    n = int(re.match(r"(\d+)", base).group(1))
    txt = open(f, encoding="utf-8", errors="ignore").read()
    if "to_regclass" in txt:
        continue
    for e in sorted({m.group(1).lower() for m in re.finditer(r"\b([a-z_]+)\.[a-z_]+", txt)}):
        if creado.get(e, 0) > n:
            fallos.append(f"  {base}: usa el esquema '{e}', que se crea en la {creado[e]}")

if fallos:
    print("Migracion que depende de un esquema creado despues:", file=sys.stderr)
    print("\n".join(sorted(set(fallos))), file=sys.stderr)
    print("", file=sys.stderr)
    print("Renumerala o envuelvela en una guarda: IF to_regclass('esq.tabla') IS NULL THEN RETURN;", file=sys.stderr)
    sys.exit(1)
PYEOF
orden=$?
[ "$orden" -ne 0 ] && exit 1

# ── Guardarrail 3: vista redefinida por una migracion posterior ─────────────
#
# CREATE OR REPLACE VIEW parece idempotente y por eso el guardarrail 1 lo acepta, pero
# solo lo es mientras nadie amplie la vista despues. Si una migracion posterior le anade
# una columna, re-ejecutar la anterior equivale a QUITARLA, y Postgres lo rechaza
# (42P16: cannot drop columns from view).
#
# No es hipotetico: la 200 definia production.v_orders con 13 columnas y la 203 le anadio
# route_id. Al reintentar la 200 sobre una base que ya tenia la 203, el barrido abortaba
# ahi y dejaba al tenant sin NINGUNA canonica posterior. Estuvo asi meses, en los cuatro
# tenants, y solo se vio al contar las migraciones que faltaban.
#
# El guardarrail 1 no puede detectarlo porque mira cada archivo aislado; esto solo existe
# en la relacion entre dos. Una redefinicion envuelta en un bloque dollar-quoted queda
# exenta: ahi la guarda decide si toca la vista o la deja como esta.
VIEW_ALLOWLIST="$ROOT/ops/scaffold/migration-view-allowlist.txt"
python3 - "$CANON_DIR" "$VIEW_ALLOWLIST" <<'PYEOF'
import glob, os, re, sys

canon = sorted(glob.glob(os.path.join(sys.argv[1], "*", "*.sql")))
raiz = os.path.abspath(os.path.join(sys.argv[1], "..", "..", ".."))
try:
    eximidos = {l.strip() for l in open(sys.argv[2], encoding="utf-8")
               if l.strip() and not l.startswith("#")}
except OSError:
    eximidos = set()

def orden(f):
    m = re.match(r"(\d+)", os.path.basename(f))
    return (int(m.group(1)) if m else 10**9, os.path.basename(f))

# Los cuerpos dollar-quoted ($$ ... $$ o $etiqueta$ ... $etiqueta$) se retiran: una
# redefinicion ahi dentro esta condicionada y no se ejecuta a ciegas.
cuerpo = re.compile(r"\$(\w*)\$.*?\$\1\$", re.S)
vista = re.compile(r"CREATE\s+OR\s+REPLACE\s+VIEW\s+([a-z_]+\.[a-z_]+)", re.I)

desnudas, todas = {}, {}
for f in canon:
    txt = open(f, encoding="utf-8", errors="ignore").read()
    todas[f] = {m.group(1).lower() for m in vista.finditer(txt)}
    desnudas[f] = {m.group(1).lower() for m in vista.finditer(cuerpo.sub(" ", txt))}

fallos = []
for f in canon:
    rel = os.path.relpath(f, raiz)
    if rel in eximidos:
        continue
    for v in sorted(desnudas[f]):
        posterior = [g for g in canon if orden(g) > orden(f) and v in todas[g]]
        if posterior:
            fallos.append(
                f"  {rel}: redefine {v} sin guarda, y {os.path.relpath(posterior[-1], raiz)} "
                f"la vuelve a definir despues"
            )

if fallos:
    print("Vista redefinida por una migracion posterior (re-ejecutarla quitaria columnas):",
          file=sys.stderr)
    print("\n".join(fallos), file=sys.stderr)
    print("""
Envuelve la redefinicion en una guarda que la salte si la vista ya publica la columna
que esta migracion anade:

  DO $mig$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_schema='esq' AND table_name='v_x' AND column_name='nueva') THEN
      EXECUTE $vista$ CREATE OR REPLACE VIEW esq.v_x AS SELECT ... $vista$;
    END IF;
  END $mig$;""", file=sys.stderr)
    sys.exit(1)
PYEOF
vistas=$?
[ "$vistas" -ne 0 ] && exit 1

echo "Migraciones canonicas: sin archivos nuevos no idempotentes (${#offenders[@]} historicos en allowlist)."
echo "Migraciones canonicas: ninguna depende de un esquema creado despues."
echo "Migraciones canonicas: ninguna redefine sin guarda una vista que otra amplia despues."
exit 0
