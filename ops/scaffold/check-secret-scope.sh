#!/usr/bin/env bash
# Guardarrail: cada contenedor recibe SOLO los secretos que usa (docs/adr/0007).
#
# La fuente es ops/security/secrets/reparto.tsv (servicio, secreto, evidencia). Antes, todo servicio
# montaba el fichero de secretos entero por env_file: con UN servicio comprometido se filtraban la
# llave de cifrado, la de enlaces, el maestro de Dovecot y las de los demas. Ahora cada bloque de
# compose declara uno a uno los suyos, y este check ata cuatro sitios que tienen que decir lo mismo:
#
#   reparto.tsv      lo que cada contenedor debe recibir, con la evidencia de que lo lee
#   compose          docker-compose.yml, el de observabilidad, los del perfil y el de los motores:
#                    nadie recibe (como variable o por interpolacion) un secreto que su fila no
#                    lista, y todo lo que su fila lista se entrega (sin eso el servicio arranca sin
#                    su credencial, que es el fallo silencioso que esto viene a evitar)
#   codigo           services/<svc> y deploy/mail/<motor>: nadie lee un secreto que su fila no
#                    lista, ni su fila concede uno que no lee
#   secret-keys.txt  todo secreto tiene destinatario (un contenedor, o "operacion" si solo lo lee
#                    un guion de ops/)
#
# Ningun bloque puede montar secrets.env ni secrets-db.env por env_file: seria volver al reparto
# de todo a todos. docker-compose.e2e.yml y los overrides de imagenes quedan fuera: el primero es un
# arnes desechable con valores de prueba y los otros no llevan entorno.
#
# El codigo Go de un servicio se lee por literales ("NOMBRE") y por tres lecturas indirectas que
# conoce (middleware.RequireGatewayToken/InternalGatewayToken, cfg.Redis y auth.SignerFromEnv); el
# de un motor, por el nombre en cualquier fichero que no sea un comentario. Una lectura indirecta
# nueva se anade a INDIRECTOS.
#
# Y demuestra que muerde: sobre una copia del arbol, cada mutacion (un secreto ajeno en un bloque,
# uno interpolado bajo otro nombre, el fichero entero por env_file, una entrega quitada, una lectura
# no declarada en un servicio y en un motor, una fila que concede de mas, un secreto sin destinatario,
# una fila sobre un servicio o un secreto inexistentes, una evidencia rota) tiene que hacer fallar
# el check con su mensaje. SECRET_SCOPE_ROOT apunta el check a otro arbol.
set -uo pipefail

ROOT_REAL="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROOT="${SECRET_SCOPE_ROOT:-$ROOT_REAL}"

verificar() {
  python3 - "$1" <<'PY'
import os
import re
import sys

raiz = sys.argv[1]
ruta = lambda *p: os.path.join(raiz, *p)
fallos = []

# Lecturas que no citan el nombre de la variable: pasan por una funcion de pkg/.
INDIRECTOS = {
    "INTERNAL_GATEWAY_TOKEN": r"middleware\.(InternalGatewayToken|RequireGatewayToken)\b",
    "REDIS_PASSWORD": r"\bcfg\.Redis\b|\bconfig\.LoadRedis\(",
    "JWT_SIGNING_KEY": r"\bauth\.SignerFromEnv\(",
}


def lineas(f):
    with open(f, encoding="utf-8") as fh:
        return fh.read().splitlines()


def palabra(nombre):
    return re.compile(r"(?<![A-Za-z0-9_])" + re.escape(nombre) + r"(?![A-Za-z0-9_])")


# ── secret-keys.txt ──────────────────────────────────────────────────────────
claves = {}
for l in lineas(ruta("ops/security/secrets/secret-keys.txt")):
    l = l.strip()
    if l and not l.startswith("#"):
        claves[l.rstrip("?")] = l.endswith("?")

# ── reparto.tsv ──────────────────────────────────────────────────────────────
filas = {}
n_filas = 0
for n, l in enumerate(lineas(ruta("ops/security/secrets/reparto.tsv")), 1):
    if not l.strip() or l.startswith("#"):
        continue
    c = l.split("\t")
    if len(c) < 3 or not all(x.strip() for x in c[:3]):
        fallos.append(f"reparto.tsv:{n}: la fila necesita servicio, secreto y evidencia separados por tabulador")
        continue
    svc, secreto, evidencia = (x.strip() for x in c[:3])
    if secreto not in claves:
        fallos.append(f"reparto.tsv:{n}: {secreto} no esta en secret-keys.txt")
        continue
    if secreto in filas.setdefault(svc, {}):
        fallos.append(f"reparto.tsv:{n}: {svc} repite {secreto}")
    filas[svc][secreto] = evidencia
    n_filas += 1

# ── secreto sin destinatario ─────────────────────────────────────────────────
asignados = {s for f in filas.values() for s in f}
for s in sorted(set(claves) - asignados):
    fallos.append(
        f"{s} esta en secret-keys.txt y no tiene destinatario: anadelo a reparto.tsv con el "
        "servicio que lo lee, o con \"operacion\" si solo lo lee un guion de ops/")

# ── evidencia ────────────────────────────────────────────────────────────────
for svc, f in filas.items():
    for s, evid in f.items():
        m = re.match(r"^(.*?)(?::\d+)?$", evid)
        fichero = ruta(m.group(1))
        if not os.path.isfile(fichero):
            fallos.append(f"{svc} {s}: la evidencia {evid} no es un fichero")
            continue
        texto = open(fichero, encoding="utf-8", errors="ignore").read()
        if not palabra(s).search(texto) and not (s in INDIRECTOS and re.search(INDIRECTOS[s], texto)):
            fallos.append(f"{svc} {s}: la evidencia {m.group(1)} no lee esa variable")

# ── bloques de compose ───────────────────────────────────────────────────────
def composes():
    bases = [raiz] + [ruta("deploy", d) for d in sorted(os.listdir(ruta("deploy"))) if os.path.isdir(ruta("deploy", d))]
    for base in bases:
        for nombre in sorted(os.listdir(base)):
            if nombre.startswith("docker-compose") and nombre.endswith((".yml", ".yaml")) \
                    and not re.search(r"\.(images|e2e)\.", nombre):
                yield os.path.join(base, nombre)


def bloques(fichero):
    svc_re = re.compile(r"^  ([A-Za-z0-9_.-]+):\s*$")
    out, actual, en_servicios = {}, None, False
    for l in lineas(fichero):
        if re.match(r"^\S", l):
            en_servicios, actual = l.rstrip() == "services:", None
            continue
        m = svc_re.match(l)
        if en_servicios and m:
            actual = m.group(1)
            out[actual] = []
        elif en_servicios and actual:
            out[actual].append(re.sub(r"(^|\s)#.*$", "", l))
    return out


def dir_codigo(cuerpo, base_rel):
    texto = "\n".join(cuerpo)
    m = re.search(r"dockerfile:[ \t]*\.?/?(services/[A-Za-z0-9_.-]+)/Dockerfile", texto)
    if m:
        return m.group(1), True
    m = re.search(r"^    build:[ \t]*(\S+)[ \t]*$", texto, re.M)
    if m:
        return os.path.normpath(os.path.join(base_rel, m.group(1))), False
    return None, False


def leer_codigo(directorio, es_go):
    """Secretos que lee el codigo de un directorio, segun el tipo de contenedor."""
    lee = set()
    tope = ruta(directorio)
    if not os.path.isdir(tope):
        return lee
    for actual, dirs, ficheros in os.walk(tope):
        dirs[:] = [d for d in dirs if d not in ("node_modules", ".git", "testdata")]
        for f in ficheros:
            if f.endswith((".md", ".sum", "_test.go")) or (es_go and not f.endswith(".go")):
                continue
            p = os.path.join(actual, f)
            if os.path.getsize(p) > 2_000_000:
                continue
            try:
                texto = open(p, encoding="utf-8").read()
            except (UnicodeDecodeError, OSError):
                continue
            if es_go:
                for s in claves:
                    if re.search(r"[\"`]" + re.escape(s) + r"[\"`]", texto):
                        lee.add(s)
                for s, rx in INDIRECTOS.items():
                    if re.search(rx, texto):
                        lee.add(s)
            else:
                util = "\n".join(l for l in texto.splitlines() if not re.match(r"\s*(#|//|--)", l))
                for s in claves:
                    if palabra(s).search(util):
                        lee.add(s)
    return lee


entregados = {}
contenedores = 0
codigo_cache = {}
for fichero in composes():
    rel = os.path.relpath(fichero, raiz)
    base_rel = os.path.dirname(rel)
    for svc, cuerpo in bloques(fichero).items():
        contenedores += 1
        texto = "\n".join(cuerpo)
        propios = filas.get(svc, {})
        recibe = {s for s in claves if palabra(s).search(texto)}
        entregados.setdefault(svc, set()).update(recibe)

        if re.search(r"secrets(-db)?\.env", texto):
            fallos.append(
                f"{rel}: {svc} recibe el fichero de secretos entero por env_file; cada secreto va "
                "en su environment: como NOMBRE: ${NOMBRE:-}")
        for s in sorted(recibe - set(propios)):
            fallos.append(f"{rel}: {svc} recibe {s} y su fila de reparto.tsv no lo lista")

        directorio, es_go = dir_codigo(cuerpo, base_rel)
        if directorio is None:
            continue
        if directorio not in codigo_cache:
            codigo_cache[directorio] = leer_codigo(directorio, es_go)
        lee = codigo_cache[directorio]
        for s in sorted(lee - set(propios)):
            fallos.append(f"{directorio}: el codigo de {svc} lee {s} y su fila de reparto.tsv no lo lista")
        for s in sorted(set(propios) - lee):
            fallos.append(f"{svc}: su fila lista {s} y su codigo ({directorio}) no lo lee: se le concede de mas")

servicios = set(entregados)
for svc, f in filas.items():
    if svc == "operacion":
        continue
    if svc not in servicios:
        fallos.append(f"reparto.tsv: {svc} no es un servicio de ningun compose")
        continue
    for s in sorted(set(f) - entregados[svc]):
        fallos.append(f"{svc}: su fila lista {s} y su bloque de compose no lo entrega: arrancaria sin ella")

if fallos:
    print("Reparto de secretos incoherente:", file=sys.stderr)
    for f in fallos:
        print(f"  {f}", file=sys.stderr)
    print("", file=sys.stderr)
    print("Regla: cada contenedor recibe solo los secretos de su fila en "
          "ops/security/secrets/reparto.tsv (docs/adr/0007).", file=sys.stderr)
    sys.exit(1)

print(f"  OK: {n_filas} entregas en {len(filas)} contenedores; {contenedores} bloques de compose sin "
      "secretos ajenos ni lecturas sin declarar.")
PY
}

echo "  Arbol real:"
verificar "$ROOT" || exit 1

[[ -n "${SECRET_SCOPE_ROOT:-}" ]] && exit 0

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

mkdir -p "$TMP/base/ops/security"
cp "$ROOT"/docker-compose*.yml "$TMP/base/"
cp -r "$ROOT/services" "$ROOT/deploy" "$TMP/base/"
cp -r "$ROOT/ops/security/secrets" "$TMP/base/ops/security/"

correr() { verificar "$1" 2>&1; }

# esperar_pasa <nombre> <comando que muta el arbol $D>: un cambio legitimo no debe fallar.
esperar_pasa() {
  local salida
  rm -rf "$TMP/w"; cp -r "$TMP/base" "$TMP/w"
  D="$TMP/w" eval "$2"
  if salida="$(correr "$TMP/w")"; then
    echo "  sin falso positivo: $1"
  else
    falla "'$1' no deberia fallar: $salida"
  fi
}

# esperar_fallo <nombre> <texto esperado> <comando que muta el arbol $D>
esperar_fallo() {
  local nombre="$1" esperado="$2" salida
  rm -rf "$TMP/w"; cp -r "$TMP/base" "$TMP/w"
  D="$TMP/w" eval "$3"
  if salida="$(correr "$TMP/w")"; then
    falla "mutacion '$nombre': el guardarrail no la detecto"
  elif ! grep -qF -- "$esperado" <<< "$salida"; then
    falla "mutacion '$nombre': fallo, pero sin el mensaje '$esperado': $salida"
  else
    echo "  mutacion detectada: $nombre"
  fi
}

if correr "$TMP/base" >/dev/null; then echo "  copia sin mutar: OK"; else falla "la copia sin mutar deberia pasar: $(correr "$TMP/base")"; fi

# Pone una linea justo despues del primer "environment:" de un servicio de un compose.
en_entorno() { sed -i "/^  $2:\$/,/^    environment:\$/ s|^    environment:\$|    environment:\\n      $3|" "$D/$1"; }

esperar_fallo "un secreto ajeno en el bloque de un servicio" \
  "contacts recibe DOVECOT_MASTER_PASS y su fila de reparto.tsv no lo lista" \
  'en_entorno docker-compose.yml contacts "DOVECOT_MASTER_PASS: \${DOVECOT_MASTER_PASS:-}"'
esperar_fallo "un secreto ajeno interpolado bajo otro nombre" \
  "webmail recibe DOVECOT_MASTER_PASS y su fila de reparto.tsv no lo lista" \
  'en_entorno docker-compose.yml webmail "OTRO_NOMBRE: \${DOVECOT_MASTER_PASS:-}"'
esperar_fallo "un secreto ajeno en un motor" \
  "unbound-mail recibe QUEUE_AGENT_API_KEY y su fila de reparto.tsv no lo lista" \
  'sed -i "s|^      - TZ=\${TZ:-UTC}\$|&\n      - QUEUE_AGENT_API_KEY=\${QUEUE_AGENT_API_KEY:-}|;t" "$D/deploy/mail/docker-compose.mail.yml"'
esperar_fallo "el fichero de secretos entero por env_file" \
  "analytics recibe el fichero de secretos entero por env_file" \
  'sed -i "/^  analytics:\$/,/^    environment:\$/ s|^      - .env\$|&\n      - path: /dev/shm/core-force-mail/secrets.env|" "$D/docker-compose.yml"'
esperar_fallo "una entrega quitada del bloque" \
  "mail-dav: su fila lista INTERNAL_GATEWAY_TOKEN y su bloque de compose no lo entrega" \
  'sed -i "/^  mail-dav:\$/,/^    depends_on:\$/ {/INTERNAL_GATEWAY_TOKEN/d}" "$D/docker-compose.yml"'
esperar_fallo "el codigo de un servicio lee un secreto que su fila no lista" \
  "el codigo de contacts lee MAIL_ENCRYPTION_KEY y su fila de reparto.tsv no lo lista" \
  'printf "package main\n\nvar _ = os.Getenv(\"MAIL_ENCRYPTION_KEY\")\n" > "$D/services/contacts/zz_mutacion.go"'
esperar_fallo "el codigo de un motor lee un secreto que su fila no lista" \
  "el codigo de unbound-mail lee DOVECOT_MASTER_PASS y su fila de reparto.tsv no lo lista" \
  'printf "echo \$DOVECOT_MASTER_PASS\n" >> "$D/deploy/mail/unbound/docker-entrypoint.sh"'
esperar_fallo "una fila que concede un secreto que el codigo no lee" \
  "contacts: su fila lista AUDIT_HASH_KEY y su codigo (services/contacts) no lo lee" \
  'printf "contacts\tAUDIT_HASH_KEY\tservices/audit/main.go:90\n" >> "$D/ops/security/secrets/reparto.tsv"; en_entorno docker-compose.yml contacts "AUDIT_HASH_KEY: \${AUDIT_HASH_KEY:-}"'
esperar_fallo "un secreto de la lista sin destinatario" \
  "SECRETO_NUEVO esta en secret-keys.txt y no tiene destinatario" \
  'printf "SECRETO_NUEVO\n" >> "$D/ops/security/secrets/secret-keys.txt"'
esperar_fallo "una fila sobre un secreto que no existe" \
  "NO_EXISTE no esta en secret-keys.txt" \
  'printf "contacts\tNO_EXISTE\tservices/contacts/main.go:1\n" >> "$D/ops/security/secrets/reparto.tsv"'
esperar_fallo "una fila sobre un servicio que no existe" \
  "reparto.tsv: fantasma no es un servicio de ningun compose" \
  'printf "fantasma\tINTERNAL_GATEWAY_TOKEN\tservices/contacts/main.go:1\n" >> "$D/ops/security/secrets/reparto.tsv"'
esperar_fallo "una evidencia que no es un fichero" \
  "contacts INTERNAL_GATEWAY_TOKEN: la evidencia services/contacts/no_existe.go:1 no es un fichero" \
  'sed -i "s|^contacts\tINTERNAL_GATEWAY_TOKEN\t[^\t]*|contacts\tINTERNAL_GATEWAY_TOKEN\tservices/contacts/no_existe.go:1|" "$D/ops/security/secrets/reparto.tsv"'
esperar_pasa "un secreto citado en un comentario o en un mensaje no es una lectura" \
  'printf "package main\n\n// MAIL_ENCRYPTION_KEY la lee otro servicio\nvar msg = \"MAIL_ENCRYPTION_KEY debe tener 64 hex\"\n" > "$D/services/contacts/zz_cita.go"'

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: el reparto de secretos es coherente; 12 mutaciones detectadas y una cita inocua aceptada."
