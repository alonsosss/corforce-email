#!/usr/bin/env bash
# Guardarrail: nadie se busca un secreto por su cuenta en el .env.
#
# Tres scripts de respaldo leian POSTGRES_PASSWORD directamente del .env de la aplicacion.
# El dia que las credenciales pasaron al almacen de secretos, el .env dejo de tenerla y los
# tres se quedaron con la variable vacia. No fallo nada de forma visible: el respaldo
# nocturno habria abortado con un "faltan credenciales" que no apuntaba a la causa, y el
# aviso solo se ve leyendo un log que nadie mira mientras el respaldo funciona.
#
# Un secreto se obtiene por el resolvedor comun -ops/db/pg-credentials.sh para Postgres,
# ops/security/secrets/load.sh para el resto- y por ningun otro camino. Asi, mover la fuente
# de los secretos mueve a todos sus consumidores a la vez.
#
# Segunda regla, del mismo tipo: todo 'docker compose' que CREE o RECREE contenedores va por
# with-secrets.sh. Compose resuelve la interpolacion de ${VAR} contra el entorno del proceso,
# asi que un compose suelto levanta el servicio con las credenciales vacias y solo avisa con
# un warning. Habia uno asi en un camino de despliegue, ademas con la salida silenciada.
#
# Complementa a check-secrets.sh, que persigue lo contrario: un secreto con valor DENTRO del
# repositorio.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

LISTA="$(mktemp)"
trap 'rm -f "$LISTA"' EXIT
git ls-files -z >"$LISTA"

python3 - ops/security/secrets/secret-keys.txt "$LISTA" <<'PY'
import re
import sys

claves = sorted({
    line.strip().rstrip("?") for line in open(sys.argv[1], encoding="utf-8")
    if line.strip() and not line.startswith("#")
})

# push-secrets.sh es la migracion de una sola vez: su trabajo ES leer el .env para subirlo.
EXENTOS = {"ops/security/secrets/push-secrets.sh"}

alternativa = "|".join(claves)
patrones = [
    # read_env POSTGRES_PASSWORD  (el ayudante que tenian los scripts de respaldo)
    re.compile(r"read_env\s+(" + alternativa + r")\b"),
    # grep -m1 "^POSTGRES_PASSWORD=" ... .env
    #
    # Se EXCLUYE la lectura del fichero materializado del almacen
    # (/dev/shm/core-force-mail/secrets.env): ahi el secreto SI esta, y leerlo es legitimo.
    # El nombre acaba igualmente en ".env", asi que sin esta salvedad los scripts que
    # hacen lo correcto salian marcados y el guardarrail vivia en rojo.
    re.compile(r"grep.*\^(" + alternativa + r")=(?!.*secrets\.env).*\.env"),
    # . .env / source .env seguido de uso directo no se detecta aqui: lo que se persigue es
    # la lectura dirigida de una clave concreta, que es la forma en que aparecio.
]

hallazgos = []
for ruta in open(sys.argv[2], "rb").read().split(b"\0"):
    if not ruta:
        continue
    ruta = ruta.decode()
    if not ruta.endswith((".sh", ".bash")) or ruta in EXENTOS:
        continue
    try:
        with open(ruta, encoding="utf-8") as fh:
            for n, linea in enumerate(fh, 1):
                if linea.lstrip().startswith("#"):
                    continue
                for patron in patrones:
                    m = patron.search(linea)
                    if m:
                        hallazgos.append((ruta, n, m.group(1)))
    except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
        continue

if hallazgos:
    print("Secretos leidos del .env en vez del almacen:", file=sys.stderr)
    for ruta, n, clave in hallazgos:
        print(f"  {ruta}:{n}  {clave}", file=sys.stderr)
    print("", file=sys.stderr)
    print("Usar el resolvedor comun: . ops/db/pg-credentials.sh (Postgres) o", file=sys.stderr)
    print("ops/security/secrets/load.sh (el resto). Desde que las credenciales viven en el", file=sys.stderr)
    print("almacen, esa lectura devuelve una cadena vacia y el fallo aparece lejos.", file=sys.stderr)
    sys.exit(1)

print(f"  OK: ninguno de los scripts se busca en el .env las {len(claves)} credenciales canonicas.")

# --- Regla 2: compose que crea contenedores, sin envoltorio de secretos -------------------
COMPOSE = re.compile(r"docker\s+compose\b[^|;&\n]*?\b(up|run|create|start)\b")

def sentencias(texto):
    """Une las continuaciones con barra antes de mirar.

    Buscar linea a linea da un falso positivo en cuanto alguien parte el comando:
    'with-secrets.sh \\' en una linea y 'docker compose up' en la siguiente son la misma
    sentencia, y separadas parecen un compose suelto. Este mismo descuido ya habia dejado
    pasar un fallo por delante de otro guardarrail."""
    logica, primera = "", 1
    for n, linea in enumerate(texto.splitlines(), 1):
        if not logica:
            primera = n
        sin_comentario = linea.split("#", 1)[0]
        if sin_comentario.rstrip().endswith("\\"):
            logica += sin_comentario.rstrip()[:-1] + " "
            continue
        yield primera, (logica + sin_comentario).strip()
        logica = ""
    if logica:
        yield primera, logica.strip()


sueltos = []
for ruta in open(sys.argv[2], "rb").read().split(b"\0"):
    if not ruta:
        continue
    ruta = ruta.decode()
    if not ruta.endswith((".sh", ".bash", ".yml", ".yaml")):
        continue
    try:
        contenido = open(ruta, encoding="utf-8").read()
    except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
        continue
    for n, sentencia in sentencias(contenido):
        if "with-secrets" in sentencia:
            continue
        if COMPOSE.search(sentencia):
            sueltos.append((ruta, n, sentencia[:90]))

if sueltos:
    print("docker compose que crea contenedores sin el envoltorio de secretos:", file=sys.stderr)
    for ruta, n, linea in sueltos:
        print(f"  {ruta}:{n}  {linea}", file=sys.stderr)
    print("", file=sys.stderr)
    print("Anteponer ops/security/secrets/with-secrets.sh. Compose interpola ${VAR} contra el", file=sys.stderr)
    print("entorno del proceso: sin el envoltorio, el servicio arranca SIN credenciales y solo", file=sys.stderr)
    print("lo dice con un warning.", file=sys.stderr)
    sys.exit(1)

print("  OK: todo compose que crea contenedores va por el envoltorio de secretos.")
PY
