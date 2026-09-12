#!/usr/bin/env bash
# Genera docs/arquitectura/EVENTS.md: el registro de contratos de eventos NATS (subjects publicados y
# consumidos por servicio) extraido del codigo. Base para versionar subjects sin roturas
# y para ver el acoplamiento por eventos. Regenerar tras cambios: make gen-events.
#
# Heuristica: extrae subjects LITERALES de llamadas .Publish*("...") y
# (Subscribe|QueueSubscribe|DurableQueueSubscribe)("..."). Los subjects pasados como
# variable/constante no se capturan (raros en el codigo actual).
set -uo pipefail
# Orden deterministico: sin esto, el sort depende del locale de la maquina (es_PE vs C
# en los runners) y el check de drift del CI falla con contenido identico.
export LC_ALL=C
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/docs/arquitectura/EVENTS.md"
PUB="$(mktemp)"; CON="$(mktemp)"
trap 'rm -f "$PUB" "$CON"' EXIT

for d in "$ROOT/services"/*/; do
  svc="$(basename "$d")"
  # Se escanea todo el servicio (el gateway publica desde su raiz, sin internal/).
  # Subject = primer string literal con punto (patron <dominio>.<entidad>.<accion>) dentro
  # de la llamada. Cubre Publish("s",...) y PublishEvent(ctx,"s",...) (subject en 2do arg).
  grep -rhoE '\.Publish[A-Za-z]*\([^)]*"[a-z][a-z0-9_.-]*\.[a-z0-9_.-]+"' "${d}" --include="*.go" 2>/dev/null \
    | sed -E 's/.*"([a-z][a-z0-9_.-]*\.[a-z0-9_.-]+)".*/\1/' | sort -u | sed "s/^/${svc}|/" >> "$PUB"
  grep -rhoE '(Subscribe|QueueSubscribe|DurableQueueSubscribe)\([^)]*"[a-z][a-z0-9_.-]*\.[a-z0-9_.-]+"' "${d}" --include="*.go" 2>/dev/null \
    | sed -E 's/.*"([a-z][a-z0-9_.-]*\.[a-z0-9_.-]+)".*/\1/' | sort -u | sed "s/^/${svc}|/" >> "$CON"
done

npub=$(wc -l < "$PUB"); ncon=$(wc -l < "$CON")
nsubj=$(cut -d'|' -f2 "$PUB" "$CON" | sort -u | wc -l)

{
  echo "# Registro de contratos de eventos (NATS)"
  echo ""
  echo "Generado por \`ops/scaffold/gen-events.sh\` desde el codigo. NO editar a mano."
  echo "Convencion de subject: \`<dominio>.<entidad>.<accion>\`. Un subject tiene UN dueno"
  echo "(el servicio que lo publica); los demas solo lo consumen (regla no-fork)."
  echo ""
  echo "Resumen: ${npub} publicaciones, ${ncon} suscripciones, ${nsubj} subjects distintos."
  echo ""
  echo "## Cruce por subject (dueno -> consumidores)"
  echo ""
  echo "| Subject | Publica | Consumen |"
  echo "|---|---|---|"
  cut -d'|' -f2 "$PUB" "$CON" | sort -u | while read -r subj; do
    pubs=$(awk -F'|' -v s="$subj" '$2==s{print $1}' "$PUB" | sort -u | paste -sd', ' -)
    cons=$(awk -F'|' -v s="$subj" '$2==s{print $1}' "$CON" | sort -u | paste -sd', ' -)
    [ -z "$pubs" ] && pubs="(ninguno: subject huerfano)"
    [ -z "$cons" ] && cons="-"
    echo "| \`${subj}\` | ${pubs} | ${cons} |"
  done
  echo ""
  echo "## Por servicio"
  echo ""
  for d in "$ROOT/services"/*/; do
    svc="$(basename "$d")"
    p=$(awk -F'|' -v s="$svc" '$1==s{print $2}' "$PUB" | sort -u)
    c=$(awk -F'|' -v s="$svc" '$1==s{print $2}' "$CON" | sort -u)
    [ -z "$p" ] && [ -z "$c" ] && continue
    echo "### ${svc}"
    if [ -n "$p" ]; then echo "- Publica: $(echo "$p" | sed 's/^/\`/; s/$/\`/' | paste -sd', ' -)"; fi
    if [ -n "$c" ]; then echo "- Consume: $(echo "$c" | sed 's/^/\`/; s/$/\`/' | paste -sd', ' -)"; fi
    echo ""
  done
} > "$OUT"

echo "generado: $OUT (${nsubj} subjects, ${npub} pub, ${ncon} sub)"

# Aviso: subjects consumidos que nadie publica (posible rotura de contrato).
echo ""
echo "== Subjects consumidos SIN publicador (revisar) =="
found=0
comm -13 <(cut -d'|' -f2 "$PUB" | sort -u) <(cut -d'|' -f2 "$CON" | sort -u) | while read -r s; do
  echo "  - $s"; found=1
done
