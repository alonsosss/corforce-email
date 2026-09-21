#!/usr/bin/env bash
# Guardarrail: la divergencia de deploy/mail/ frente a mailcow esta registrada y explicada, sin red.
#
# Por que: los motores se copiaron una vez y cada version de mailcow se porta a mano. Si nadie sabe
# que ficheros difieren de mailcow ni por que, portar es investigar; y un cambio nuevo en un fichero
# copiado que no se anota agranda la divergencia sin que nadie lo decida. El manifiesto
# (deploy/mail/upstream-manifest.tsv) guarda la huella de cada fichero identico a mailcow y la
# categoria de cada uno que difiere; deploy/mail/UPSTREAM.md explica cada diferencia.
#
# Comprueba, con ops/upstream/upstream.sh:
#   - todo fichero de deploy/mail esta en el manifiesto, y todo lo del manifiesto existe;
#   - un fichero registrado como identico sigue con la misma huella: editarlo obliga a pasarlo a
#     modificado y a explicarlo, que es el aviso de que la divergencia crece;
#   - todo modificado o nuevo tiene una categoria valida y aparece explicado en UPSTREAM.md.
#
# Y demuestra que el guardarrail muerde. Con un arbol de prueba, cada una de estas mutaciones tiene
# que hacerlo fallar con su mensaje: editar un identico, anadir un fichero sin clasificar, borrar uno
# registrado, quitar su explicacion del libro, una categoria inventada, una sin clasificar y un
# manifiesto sin base. Ademas ejecuta `informe` y `regenerar` de punta a punta contra un mailcow
# simulado (un repositorio git local): cambio limpio, a revisar, sin equivalente, no copiado y
# posible seguridad, y la conservacion de categorias al regenerar.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UP="$ROOT/ops/upstream/upstream.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

echo "  Arbol real:"
bash "$UP" verificar || FAIL=1

sha() { sha256sum "$1" | awk '{print $1}'; }

# fixture <dir>: un arbol minimo con un fichero por estado.
fixture() {
  local d="$1"
  rm -rf "$d"; mkdir -p "$d/deploy/mail/postfix"
  printf 'uno\n' > "$d/deploy/mail/postfix/a.sh"
  printf 'dos\n' > "$d/deploy/mail/postfix/b.conf"
  printf 'tres\n' > "$d/deploy/mail/postfix/c.sh"
  printf 'b.conf: `postfix/b.conf` cambia un nombre. c.sh: `postfix/c.sh` es nuestro.\n' > "$d/deploy/mail/UPSTREAM.md"
  {
    printf '#base\t0123456789ab\t2026-01-01\tprueba\n'
    printf 'identico\tpostfix/a.sh\t%s\n' "$(sha "$d/deploy/mail/postfix/a.sh")"
    printf 'modificado\tpostfix/b.conf\tnombres\tdata/conf/postfix/b.conf\n'
    printf 'nuevo\tpostfix/c.sh\tarranque\t-\n'
    printf 'propio\tUPSTREAM.md\t-\n'
    printf 'propio\tupstream-manifest.tsv\t-\n'
  } > "$d/deploy/mail/upstream-manifest.tsv"
  printf 'postfix/a.sh\npostfix/b.conf\npostfix/c.sh\nUPSTREAM.md\nupstream-manifest.tsv\n' > "$d/lista"
}
correr() { UPSTREAM_RAIZ="$1" UPSTREAM_FICHEROS="$1/lista" bash "$UP" verificar 2>&1; }

# esperar_fallo <nombre> <texto esperado> <comando que muta el arbol $D>
esperar_fallo() {
  local nombre="$1" esperado="$2" salida
  fixture "$TMP/f"
  D="$TMP/f" eval "$3"
  if salida="$(correr "$TMP/f")"; then
    falla "mutacion '$nombre': el guardarrail no la detecto"
  elif ! grep -qF "$esperado" <<< "$salida"; then
    falla "mutacion '$nombre': fallo, pero sin el mensaje '$esperado': $salida"
  else
    echo "  mutacion detectada: $nombre"
  fi
}

fixture "$TMP/f"
if correr "$TMP/f" >/dev/null; then echo "  arbol de prueba sin mutar: OK"; else falla "el arbol de prueba sin mutar deberia pasar"; fi

esperar_fallo "editar un fichero identico a mailcow" "diverge de mailcow sin registrar" \
  'printf "cambio\n" >> "$D/deploy/mail/postfix/a.sh"'
esperar_fallo "anadir un fichero sin clasificar" "sin clasificar: postfix/d.sh" \
  'printf "x\n" > "$D/deploy/mail/postfix/d.sh"; printf "postfix/d.sh\n" >> "$D/lista"'
esperar_fallo "borrar un fichero registrado" "ya no existe: postfix/c.sh" \
  'grep -vx "postfix/c.sh" "$D/lista" > "$D/l2"; mv "$D/l2" "$D/lista"'
esperar_fallo "quitar la explicacion del libro" "no explicado en UPSTREAM.md: postfix/b.conf" \
  'printf "nada\n" > "$D/deploy/mail/UPSTREAM.md"'
esperar_fallo "categoria inventada" "categoria desconocida 'inventada'" \
  'sed -i "s/nombres/inventada/" "$D/deploy/mail/upstream-manifest.tsv"'
esperar_fallo "categoria sin clasificar" "sin categoria: postfix/b.conf" \
  'sed -i "s/nombres/sin-clasificar/" "$D/deploy/mail/upstream-manifest.tsv"'
esperar_fallo "manifiesto sin base" "no declara la base" \
  'sed -i "1d" "$D/deploy/mail/upstream-manifest.tsv"'

# ---- informe y regenerar contra un mailcow simulado ----
git_mc() { git -C "$TMP/mc" -c user.email=t@t -c user.name=t "$@"; }
rm -rf "$TMP/mc"; mkdir -p "$TMP/mc/data/Dockerfiles/postfix" "$TMP/mc/data/Dockerfiles/sogo" "$TMP/mc/data/conf/postfix"
git init -q -b principal "$TMP/mc"
printf 'v1\n' > "$TMP/mc/data/Dockerfiles/postfix/a.sh"
printf 'v1\n' > "$TMP/mc/data/conf/postfix/b.conf"
printf 'v1\n' > "$TMP/mc/data/Dockerfiles/sogo/sogo.sh"
git_mc add -A; git_mc commit -q -m "base"
BASE="$(git_mc rev-parse --short=12 HEAD)"
printf 'v2\n' > "$TMP/mc/data/Dockerfiles/postfix/a.sh"
printf 'v2\n' > "$TMP/mc/data/conf/postfix/b.conf"
printf 'v2\n' > "$TMP/mc/data/Dockerfiles/sogo/sogo.sh"
printf 'n1\n' > "$TMP/mc/data/Dockerfiles/postfix/nuevo.sh"
git_mc add -A; git_mc commit -q -m "fix CVE-2026-0001 en postfix"

# local: a.sh igual que la base, b.conf distinto (lo modificamos), c.sh nuestro.
fixture "$TMP/g"
printf 'v1\n' > "$TMP/g/deploy/mail/postfix/a.sh"
printf 'v1 nuestro\n' > "$TMP/g/deploy/mail/postfix/b.conf"
{
  printf '#base\t%s\t2026-01-01\tprueba\n' "$BASE"
  printf 'identico\tpostfix/a.sh\t%s\n' "$(sha "$TMP/g/deploy/mail/postfix/a.sh")"
  printf 'modificado\tpostfix/b.conf\tnombres\tdata/conf/postfix/b.conf\n'
  printf 'nuevo\tpostfix/c.sh\tarranque\t-\n'
  printf 'propio\tUPSTREAM.md\t-\n'
  printf 'propio\tupstream-manifest.tsv\t-\n'
} > "$TMP/g/deploy/mail/upstream-manifest.tsv"

INF="$(UPSTREAM_RAIZ="$TMP/g" UPSTREAM_FICHEROS="$TMP/g/lista" bash "$UP" informe "$TMP/mc" principal 2>&1)"
comprobar() { grep -qF -- "$1" <<< "$INF" && echo "  informe: $2" || falla "informe: falta '$1' ($2)"; }
comprobar "RELEVANTES: 3" "cuenta los relevantes (limpio, a revisar y sin equivalente) y no el no copiado"
comprobar "### Cambio limpio" "separa el cambio limpio"
comprobar '`postfix/a.sh` <- `data/Dockerfiles/postfix/a.sh`' "cruza el fichero identico con su origen"
comprobar "### A revisar" "separa lo que modificamos nosotros"
comprobar '`postfix/b.conf` <- `data/conf/postfix/b.conf`' "cruza el fichero modificado con su origen"
comprobar "### Sin equivalente local" "avisa de lo que mailcow anade"
comprobar '`data/Dockerfiles/postfix/nuevo.sh`' "nombra el fichero nuevo de mailcow"
comprobar "### No copiado por diseno" "lista lo no copiado como informativo"
comprobar '`data/Dockerfiles/sogo/sogo.sh`' "nombra lo no copiado"
comprobar "### Posible seguridad" "marca los commits con palabras de seguridad"
comprobar "CVE-2026-0001" "muestra el commit de seguridad"

# regenerar conserva categorias y marca lo que empieza a diferir.
git_mc checkout -q "$BASE" 2>/dev/null
printf 'v1 cambiado\n' > "$TMP/g/deploy/mail/postfix/a.sh"
UPSTREAM_RAIZ="$TMP/g" UPSTREAM_FICHEROS="$TMP/g/lista" bash "$UP" regenerar "$TMP/mc" >/dev/null 2>&1
grep -qP '^modificado\tpostfix/b.conf\tnombres\t' "$TMP/g/deploy/mail/upstream-manifest.tsv" \
  && echo "  regenerar: conserva la categoria de lo ya clasificado" || falla "regenerar perdio la categoria de postfix/b.conf"
grep -qP '^modificado\tpostfix/a.sh\tsin-clasificar\t' "$TMP/g/deploy/mail/upstream-manifest.tsv" \
  && echo "  regenerar: lo que empieza a diferir queda sin clasificar" || falla "regenerar no marco postfix/a.sh como sin clasificar"
if UPSTREAM_RAIZ="$TMP/g" UPSTREAM_FICHEROS="$TMP/g/lista" bash "$UP" verificar >/dev/null 2>&1; then
  falla "tras regenerar, lo sin clasificar no rompio verificar"
else
  echo "  regenerar: verificar exige clasificarlo antes de seguir"
fi

echo ""
[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: la divergencia con mailcow esta registrada; 7 mutaciones detectadas, informe y regenerar probados."
