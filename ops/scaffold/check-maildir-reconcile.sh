#!/usr/bin/env bash
# Prueba de deploy/mail/dovecot/maildir_reconcile.sh sin docker: un arbol de maildir falso y un psql falso.
#
# Por que: el guion mueve directorios del volumen de correo de produccion. Un fallo suyo por exceso (mover
# el maildir de un buzon vivo, seguir un enlace, vaciar el servidor cuando la base responde vacio) destruye
# correo; uno por defecto (no mover el de un buzon borrado) deja que el siguiente titular de la direccion
# herede el correo del anterior. Aqui se comprueba cada regla con un arbol conocido y una base simulada,
# y se demuestra que la prueba muerde: sin el fail-closed de la consulta vacia y sin la gracia, la prueba
# tiene que fallar.
#
# Uso: bash ops/scaffold/check-maildir-reconcile.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/deploy/mail/dovecot/maildir_reconcile.sh"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

bash -n "$SCRIPT" || falla "el guion no pasa bash -n"

# ── psql falso ────────────────────────────────────────────────────────────────
# Responde a cada consulta del guion con el fichero que le toca (FAKE_DB/<buzones|dominios|marcas>) y
# anota los DELETE en FAKE_DB/borrados. FAKE_PSQL_FAIL=1 hace que toda consulta falle.
mkdir -p "$W/bin"
cat >"$W/bin/psql" <<'EOF'
#!/usr/bin/env bash
sql=""
while [[ $# -gt 0 ]]; do
  case $1 in
    -c) sql=$2; shift 2 ;;
    *) shift ;;
  esac
done
[[ ${FAKE_PSQL_FAIL:-0} == 1 ]] && { echo "psql: fallo simulado" >&2; exit 2; }
case $sql in
  "DELETE FROM mail.mailbox_deletions"*) printf '%s\n' "$sql" >>"$FAKE_DB/borrados"; echo "DELETE 1" ;;
  *"FROM mail.mailbox_deletions"*) cat "$FAKE_DB/marcas" ;;
  *"FROM mail.mailboxes"*) cat "$FAKE_DB/buzones" ;;
  *"FROM mail.domains"*) cat "$FAKE_DB/dominios" ;;
  *) echo "psql falso: consulta desconocida: $sql" >&2; exit 3 ;;
esac
EOF
chmod +x "$W/bin/psql"

# ── Arbol de prueba ───────────────────────────────────────────────────────────
# Todo lo "viejo" tiene mtime de hace dias; "acme.test/nuevo" acaba de crearse (gracia).
VIEJO="2 days ago"
maildir() { # maildir <ruta> [mtime]
  mkdir -p "$1/cur" "$1/new" "$1/tmp"
  printf 'x\n' >"$1/cur/1.msg"
  find "$1" -exec touch -d "${2:-$VIEJO}" {} +
}
arbol() {
  local v=$1
  rm -rf "$v"; mkdir -p "$v/_garbage" "$v/sieve" "$v/sieve_vacation_bindir" "$v/sieve_before_bindir"
  printf 'db\n' >"$v/shared-mailboxes.db"
  maildir "$v/acme.test/ana"
  maildir "$v/acme.test/bea"
  maildir "$v/acme.test/huerfano"
  maildir "$v/acme.test/nuevo" "now"
  maildir "$v/acme.test/borrada"
  maildir "$v/acme.test/recreada"
  maildir "$v/acme.test/fresca" "now"
  maildir "$v/acme.test/anomala"
  maildir "$v/acme.test/Mayus"
  maildir "$v/viejo.test/x"
  maildir "$v/platform.local/maestro"
  maildir "$v/raro_dir/uno"
  ln -s "$v/acme.test" "$v/enlace.test"
  ln -s "$v/acme.test/huerfano" "$v/acme.test/enlace"
  find "$v/acme.test" "$v/viejo.test" "$v/platform.local" "$v/raro_dir" -maxdepth 0 -exec touch -d "$VIEJO" {} +
}
base() { # base <dir>: buzones y dominios que "existen"
  mkdir -p "$1"
  printf 'acme.test\tana\nacme.test\tbea\nacme.test\tnuevo\nacme.test\trecreada\nacme.test\tfresca\nacme.test\tanomala\n' >"$1/buzones"
  printf 'acme.test\n' >"$1/dominios"
  : >"$1/marcas"; : >"$1/borrados"
}
AHORA=$(date +%s)
HACE_1H=$((AHORA - 3600))
HACE_2H=$((AHORA - 7200))
ID1=11111111-1111-4111-8111-111111111111
ID2=22222222-2222-4222-8222-222222222222
ID3=33333333-3333-4333-8333-333333333333
ID4=44444444-4444-4444-8444-444444444444
ID5=55555555-5555-4555-8555-555555555555
marcas() {
  {
    printf '%s\tacme.test\tborrada\t%s\t0\n' "$ID1" "$HACE_1H"            # borrada y no recreada
    printf '%s\tacme.test\trecreada\t%s\t%s\n' "$ID2" "$HACE_2H" "$HACE_1H" # recreada sobre el maildir viejo
    printf '%s\tacme.test\tfresca\t%s\t%s\n' "$ID3" "$HACE_2H" "$HACE_1H"   # recreada; el maildir es del buzon nuevo
    printf '%s\tacme.test\tsinmaildir\t%s\t0\n' "$ID4" "$HACE_1H"          # nada en disco
    printf '%s\tacme.test\tanomala\t%s\t%s\n' "$ID5" "$HACE_1H" "$HACE_2H"  # alta anterior a la baja
  } >"$1/marcas"
}

correr() { # correr <guion> <vmail> <db> [env...]: deja SALIDA y RC
  local guion=$1 vmail=$2 db=$3; shift 3
  SALIDA=$(env PATH="$W/bin:$PATH" FAKE_DB="$db" MAILDIR_RECONCILE_ROOT="$vmail" MAILDIR_RECONCILE_GRACE=10 \
    MAIL_DB_HOST=db MAIL_DB_USER=u MAIL_DB_NAME=n MAIL_DB_PASSWORD=p "$@" bash "$guion" 2>&1)
  RC=$?
}
en_garbage() { # en_garbage <vmail> <sufijo>: hay un <epoch>_<sufijo> en _garbage
  find "$1/_garbage" -mindepth 1 -maxdepth 1 -name "[0-9]*_$2" | grep -q .
}
# nada_movido <vmail>: el arbol no perdio nada
nada_movido() {
  [[ -d $1/acme.test/huerfano && -d $1/viejo.test/x && -d $1/acme.test/borrada && -d $1/acme.test/recreada ]] \
    && [[ -z $(find "$1/_garbage" -mindepth 1 -maxdepth 1 -type d) ]]
}

# ── Escenario principal ───────────────────────────────────────────────────────
echo "  escenario principal (huerfanos, dominio huerfano, marcas, gracia, enlaces, reservados)"
arbol "$W/v"; base "$W/db"; marcas "$W/db"
correr "$SCRIPT" "$W/v" "$W/db"
[[ $RC -eq 0 ]] || falla "la pasada termino con $RC: $SALIDA"
[[ -d $W/v/acme.test/ana && -d $W/v/acme.test/bea ]] || falla "movio un buzon vivo"
[[ -f $W/v/acme.test/ana/cur/1.msg ]] || falla "toco el contenido de un buzon vivo"
en_garbage "$W/v" acme.test_huerfano && [[ ! -e $W/v/acme.test/huerfano ]] || falla "no movio el maildir huerfano"
en_garbage "$W/v" viejo.test && [[ ! -e $W/v/viejo.test ]] || falla "no movio el dominio huerfano entero"
[[ -d $W/v/acme.test/nuevo ]] || falla "movio un buzon vivo recien creado"
[[ -L $W/v/enlace.test && -L $W/v/acme.test/enlace ]] || falla "toco un enlace simbolico"
[[ -d $W/v/acme.test/huerfano ]] && falla "siguio el enlace"
[[ -z $(find "$W/v/_garbage" -mindepth 1 -maxdepth 1 -name '*enlace*') ]] || falla "movio un enlace"
[[ -d $W/v/platform.local/maestro && -d $W/v/raro_dir/uno && -d $W/v/sieve && -d $W/v/sieve_vacation_bindir && -f $W/v/shared-mailboxes.db ]] \
  || falla "toco un directorio reservado o sin forma de dominio"
grep -q "ignorado (no tiene forma de dominio): raro_dir" <<<"$SALIDA" || falla "no anuncio el directorio sin forma de dominio"
[[ -d $W/v/acme.test/Mayus ]] || falla "movio un directorio sin forma de parte local"
grep -q "ignorado (no tiene forma de parte local): acme.test/Mayus" <<<"$SALIDA" || falla "no anuncio el directorio sin forma de parte local"
# Marcas.
en_garbage "$W/v" acme.test_borrada && grep -q "WHERE id = '$ID1'" "$W/db/borrados" || falla "marca de baja sin recrear: no movio o no consumio"
en_garbage "$W/v" acme.test_recreada && grep -q "WHERE id = '$ID2'" "$W/db/borrados" || falla "buzon recreado sobre el maildir viejo: no lo movio o no consumio la marca"
[[ -d $W/v/acme.test/fresca ]] && grep -q "WHERE id = '$ID3'" "$W/db/borrados" || falla "maildir del buzon nuevo: lo movio o no consumio la marca"
grep -q "WHERE id = '$ID4'" "$W/db/borrados" || falla "marca sin maildir en disco no consumida"
[[ -d $W/v/acme.test/anomala ]] && ! grep -q "WHERE id = '$ID5'" "$W/db/borrados" && grep -q "anomalia: acme.test/anomala" <<<"$SALIDA" \
  || falla "anomalia (alta anterior a la baja): la toco o consumio la marca"
grep -qE "movido acme.test/huerfano -> _garbage/[0-9]+_acme.test_huerfano \([0-9]+ KiB, sin fila en mail.mailboxes\)" <<<"$SALIDA" \
  || falla "el registro no lleva nombre, destino y tamano"
grep -q "pasada terminada: 4 movidos" <<<"$SALIDA" || falla "resumen inesperado: $(grep 'pasada terminada' <<<"$SALIDA")"

# Un buzon recreado cuyo maildir viejo recibio ficheros nuevos: se mueve igual y se avisa cuantos van con el.
echo "  buzon recreado con correo nuevo en el maildir del anterior"
arbol "$W/v"; base "$W/db"
printf '%s\tacme.test\trecreada\t%s\t%s\n' "$ID2" "$HACE_2H" "$HACE_1H" >"$W/db/marcas"
touch "$W/v/acme.test/recreada/cur/2.msg" "$W/v/acme.test/recreada/cur/3.msg"
correr "$SCRIPT" "$W/v" "$W/db"
en_garbage "$W/v" acme.test_recreada || falla "no movio el maildir anterior con correo nuevo"
grep -q "aviso: 2 ficheros posteriores al alta del buzon nuevo acme.test/recreada" <<<"$SALIDA" || falla "no aviso de los ficheros del buzon nuevo"

# ── Fail-closed ───────────────────────────────────────────────────────────────
echo "  fail-closed: base vacia, psql caido, salida corrupta"
arbol "$W/v"; base "$W/db"; : >"$W/db/buzones"
correr "$SCRIPT" "$W/v" "$W/db"
[[ $RC -ne 0 ]] && nada_movido "$W/v" || falla "con la consulta de buzones vacia movio algo o termino en 0"
grep -q "ABORTADA sin mover nada: la consulta de buzones no devolvio ninguno" <<<"$SALIDA" || falla "sin el mensaje de abortada por buzones vacios"

arbol "$W/v"; base "$W/db"; : >"$W/db/dominios"
correr "$SCRIPT" "$W/v" "$W/db"
[[ $RC -ne 0 ]] && nada_movido "$W/v" || falla "con la consulta de dominios vacia movio algo"

arbol "$W/v"; base "$W/db"; marcas "$W/db"
correr "$SCRIPT" "$W/v" "$W/db" FAKE_PSQL_FAIL=1
[[ $RC -ne 0 ]] && nada_movido "$W/v" && [[ ! -s $W/db/borrados ]] || falla "con psql caido movio algo o consumio marcas"

arbol "$W/v"; base "$W/db"; printf 'psql: ERROR: algo\n' >>"$W/db/buzones"
correr "$SCRIPT" "$W/v" "$W/db"
[[ $RC -ne 0 ]] && nada_movido "$W/v" || falla "con una linea corrupta en la consulta movio algo"

arbol "$W/v"; base "$W/db"; printf 'no-es-uuid\tacme.test\tborrada\t%s\t0\n' "$HACE_1H" >"$W/db/marcas"
correr "$SCRIPT" "$W/v" "$W/db"
[[ $RC -ne 0 ]] && nada_movido "$W/v" && [[ ! -s $W/db/borrados ]] || falla "con una marca corrupta movio algo o ejecuto un DELETE"

# ── Tope, cerrojo y gracia 0 ──────────────────────────────────────────────────
echo "  tope de movimientos, cerrojo y gracia a cero"
arbol "$W/v"; base "$W/db"
correr "$SCRIPT" "$W/v" "$W/db" MAILDIR_RECONCILE_MAX_MOVES=1
[[ $(find "$W/v/_garbage" -mindepth 1 -maxdepth 1 -type d | wc -l) -eq 1 ]] || falla "el tope de movimientos no se respeta"
grep -q "tope de 1 movimientos alcanzado" <<<"$SALIDA" || falla "no anuncio el tope"

arbol "$W/v"; base "$W/db"
( exec 9>"$W/v/_garbage/.reconcile.lock"; flock 9; sleep 3 ) &
BLOQUEO=$!
sleep 0.5
correr "$SCRIPT" "$W/v" "$W/db"
wait "$BLOQUEO"
[[ $RC -eq 0 ]] && nada_movido "$W/v" && grep -q "en curso" <<<"$SALIDA" || falla "con el cerrojo tomado no se aparto"

arbol "$W/v"; base "$W/db"
correr "$SCRIPT" "$W/v" "$W/db" MAILDIR_RECONCILE_GRACE=0
en_garbage "$W/v" acme.test_huerfano || falla "con gracia 0 no movio el huerfano viejo"
[[ -d $W/v/acme.test/nuevo ]] || falla "con gracia 0 movio un buzon vivo recien creado"

# Un huerfano recien creado: con la gracia por defecto se espera y se anuncia; con gracia 0 se mueve.
arbol "$W/v"; base "$W/db"; grep -v nuevo "$W/db/buzones" >"$W/db/b2"; mv "$W/db/b2" "$W/db/buzones"
correr "$SCRIPT" "$W/v" "$W/db"
[[ -d $W/v/acme.test/nuevo ]] || falla "movio un huerfano mas joven que la gracia"
grep -q "acme.test/nuevo: creado hace menos de 10 min" <<<"$SALIDA" || falla "no anuncio la espera por gracia"
correr "$SCRIPT" "$W/v" "$W/db" MAILDIR_RECONCILE_GRACE=0
en_garbage "$W/v" acme.test_nuevo || falla "con gracia 0 no movio el huerfano recien creado"

# Valores invalidos de configuracion: no se mueve nada.
arbol "$W/v"; base "$W/db"
correr "$SCRIPT" "$W/v" "$W/db" MAILDIR_RECONCILE_GRACE=abc
[[ $RC -ne 0 ]] && nada_movido "$W/v" || falla "con una gracia invalida siguio adelante"

# ── Mutaciones: la prueba tiene que fallar sin las dos protecciones ─────────
echo "  mutaciones"
mutar() { # mutar <nombre> <sed> <escenario que debe dejar de cumplirse>
  local copia="$W/mutado.sh"
  sed -e "$2" "$SCRIPT" >"$copia"
  if cmp -s "$copia" "$SCRIPT"; then falla "mutacion '$1': el sed no cambio nada (ancla perdida)"; return; fi
  if "$3" "$copia"; then falla "mutacion '$1': la prueba no la detecto"; else echo "  mutacion detectada: $1"; fi
}
# Escenarios como funciones que devuelven 0 si la proteccion sigue en pie.
protegido_base_vacia() {
  arbol "$W/m"; base "$W/dbm"; : >"$W/dbm/buzones"
  correr "$1" "$W/m" "$W/dbm"
  [[ $RC -ne 0 ]] && nada_movido "$W/m"
}
protegida_gracia() {
  arbol "$W/m"; base "$W/dbm"; grep -v nuevo "$W/dbm/buzones" >"$W/dbm/b2"; mv "$W/dbm/b2" "$W/dbm/buzones"
  correr "$1" "$W/m" "$W/dbm"
  [[ -d $W/m/acme.test/nuevo ]]
}
protegido_base_vacia "$SCRIPT" || falla "el escenario de base vacia no pasa con el guion real"
protegida_gracia "$SCRIPT" || falla "el escenario de gracia no pasa con el guion real"
mutar "sin fail-closed ante una consulta de buzones vacia" '/la consulta de buzones no devolvio ninguno/d' protegido_base_vacia
mutar "sin gracia" 's/^reciente() .*/reciente() { return 1; }/' protegida_gracia

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: maildir_reconcile.sh mueve solo lo que no tiene buzon, respeta gracia, enlaces, reservados y tope, falla cerrado y 2 mutaciones se detectan."
