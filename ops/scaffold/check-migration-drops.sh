#!/usr/bin/env bash
# Detecta las migraciones que dicen reemplazar una restriccion y no la reemplazan.
#
# Una migracion escribio:
#
#   ALTER TABLE ... DROP CONSTRAINT IF EXISTS pw_stock_movements_type_check;
#   ALTER TABLE ... ADD  CONSTRAINT pw_stock_movements_type_check CHECK (...);
#
# La restriccion real, creada al nacer la tabla, se llamaba pw_movements_type_check. El DROP
# no fallo -para eso esta el IF EXISTS- ni aviso: no hizo nada. La tabla quedo con DOS CHECK
# sobre la misma columna, el viejo seguia rechazando los valores nuevos, y la migracion
# figuraba como aplicada. Se descubrio mirando pg_constraint a mano.
#
# Dos reglas, las dos estaticas: no hace falta base de datos.
#
#   1. Una migracion que RETIRA una restriccion de una tabla y anade un CHECK sobre una
#      columna que ya estaba gobernada por otro CHECK que no retira. Esa es la firma exacta
#      del fallo: se quiso reemplazar y se erro el nombre, asi que el viejo sigue en pie.
#      Anadir un CHECK complementario -sin retirar nada- es normal y no se toca.
#   2. Un DROP ... IF EXISTS sobre un nombre que NINGUNA migracion crea. O esta mal escrito,
#      o pertenece a otro sitio; en ambos casos la sentencia miente sobre lo que hace.
#
# Quedan fuera los nombres que PostgreSQL genera solo (tabla_columna_pkey, _key, _fkey): no
# aparecen escritos en ninguna migracion porque los pone el motor.
#
# Excepciones justificadas: ops/scaffold/migration-drops-allowlist.txt.
set -euo pipefail
cd "$(dirname "$0")/../.."

python3 - ops/scaffold/migration-drops-allowlist.txt <<'PY'
import pathlib
import re
import sys

raiz = pathlib.Path("migrations")


def orden_de_aplicacion(ruta):
    # Las migraciones se aplican por numero dentro de cada servicio, no por orden
    # alfabetico: ordenar como texto pone la 190 antes que la 36 y la historia de la tabla
    # se lee al reves.
    m = re.match(r"(\d+)", ruta.name)
    return (str(ruta.parent), int(m.group(1)) if m else 10**9, ruta.name)


archivos = sorted(raiz.rglob("*.sql"), key=orden_de_aplicacion)
sin_comentarios = {
    ruta: re.sub(r"--[^\n]*", "", ruta.read_text(encoding="utf-8", errors="replace"))
    for ruta in archivos
}

allow = set()
ruta_allow = pathlib.Path(sys.argv[1])
if ruta_allow.exists():
    allow = {
        l.strip().lower() for l in ruta_allow.read_text(encoding="utf-8").splitlines()
        if l.strip() and not l.startswith("#")
    }

IMPLICITO = re.compile(r"_(pkey|key|fkey|excl)$", re.I)

# Palabras que aparecen en una expresion CHECK sin ser columnas: operadores, tipos y
# funciones. Lo que queda tras descartarlas son los campos que la restriccion gobierna, que
# es lo unico que hace falta para saber si dos CHECK hablan de lo mismo.
RUIDO = {
    "and", "or", "not", "in", "any", "all", "is", "null", "true", "false", "between",
    "like", "similar", "to", "case", "when", "then", "else", "end", "exists", "array",
    "character", "varying", "text", "integer", "numeric", "boolean", "date", "timestamp",
    "time", "zone", "uuid", "jsonb", "json", "bigint", "smallint", "real", "double",
    "precision", "interval", "check", "constraint", "value", "values", "cast", "coalesce",
    "length", "upper", "lower", "trim", "abs", "round", "now", "current_date", "with",
    "octet_length", "char_length", "jsonb_typeof", "cardinality",
}


def columnas_de(expresion):
    # Se quitan los literales antes de mirar identificadores: 'draft' no es una columna.
    limpio = re.sub(r"'[^']*'", " ", expresion)
    return {
        palabra for palabra in re.findall(r"[A-Za-z_][\w$]*", limpio.lower())
        if palabra not in RUIDO
    }


def expresion_tras(texto, pos):
    # Devuelve el contenido del parentesis que abre despues de pos, equilibrado.
    inicio = texto.find("(", pos)
    if inicio == -1:
        return ""
    nivel = 0
    for i in range(inicio, len(texto)):
        if texto[i] == "(":
            nivel += 1
        elif texto[i] == ")":
            nivel -= 1
            if nivel == 0:
                return texto[inicio + 1:i]
    return texto[inicio + 1:]


def linea_de(texto, pos):
    return texto[:pos].count("\n") + 1


# --- Inventario: que restricciones CHECK tiene cada tabla, y desde que migracion ----------
# La clave es el nombre calificado de la tabla tal como se escribe (esquema.tabla).
checks_por_tabla = {}   # tabla -> {nombre_restriccion: {columnas que gobierna}}
creados = set()         # todo nombre que alguna migracion crea, sea del tipo que sea
hallazgos = []

CREA_TABLA = re.compile(
    r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)\s*\((.*?)\n\)\s*;",
    re.I | re.S,
)
CONSTRAINT_INLINE = re.compile(r"\bCONSTRAINT\s+([A-Za-z_][\w$]*)\s+CHECK\b", re.I)
CUALQUIER_CONSTRAINT = re.compile(r"\bCONSTRAINT\s+([A-Za-z_][\w$]*)", re.I)
CREA_INDICE = re.compile(
    r"\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][\w$]*)", re.I)
CREA_TRIGGER = re.compile(r"\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+([A-Za-z_][\w$]*)", re.I)

ALTER_ADD_CHECK = re.compile(
    r"ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)"
    r"[^;]*?ADD\s+CONSTRAINT\s+([A-Za-z_][\w$]*)\s+CHECK",
    re.I | re.S,
)
ALTER_DROP = re.compile(
    r"ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)"
    r"[^;]*?DROP\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][\w$]*)",
    re.I | re.S,
)

# Primera pasada: todo lo que se crea en cualquier migracion.
for contenido in sin_comentarios.values():
    for patron in (CUALQUIER_CONSTRAINT, CREA_INDICE, CREA_TRIGGER):
        for m in patron.finditer(contenido):
            creados.add(m.group(1).lower())

# Segunda pasada, en orden de fichero: se sigue la historia de cada tabla.
for ruta in archivos:
    contenido = sin_comentarios[ruta]

    for m in CREA_TABLA.finditer(contenido):
        tabla = m.group(1).lower()
        cuerpo = m.group(2)
        for c in CONSTRAINT_INLINE.finditer(cuerpo):
            checks_por_tabla.setdefault(tabla, {})[c.group(1).lower()] = columnas_de(
                expresion_tras(cuerpo, c.end()))

    retirados_aqui = {
        (m.group(1).lower(), m.group(2).lower()) for m in ALTER_DROP.finditer(contenido)
    }

    for m in ALTER_ADD_CHECK.finditer(contenido):
        tabla, nombre = m.group(1).lower(), m.group(2).lower()
        columnas = columnas_de(expresion_tras(contenido, m.end()))
        previos = checks_por_tabla.get(tabla, {})
        # Solo interesa si la migracion pretende reemplazar algo en esta tabla: sin ningun
        # DROP, anadir un CHECK mas es exactamente lo que dice ser.
        pretende_reemplazar = any(t == tabla for t, _ in retirados_aqui)
        chocan = sorted(
            p for p, cols in previos.items()
            if pretende_reemplazar and p != nombre
            and (tabla, p) not in retirados_aqui and p not in allow
            and cols & columnas
        )
        if chocan:
            hallazgos.append(
                f"{ruta}:{linea_de(contenido, m.start())}: se anade el CHECK '{nombre}' a "
                f"{tabla} sin retirar {', '.join(chocan)}, que gobierna las mismas columnas "
                f"({', '.join(sorted(previos[chocan[0]] & columnas))}). Quedarian dos y "
                "manda el mas restrictivo."
            )
        checks_por_tabla.setdefault(tabla, {})[nombre] = columnas

    for tabla, nombre in sorted(retirados_aqui):
        checks_por_tabla.get(tabla, {}).pop(nombre, None)

# Regla 2: DROP de un nombre que nadie crea.
PATRONES_DROP = [
    (r"DROP\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][\w$]*)", "restriccion"),
    (r"DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?(?:[A-Za-z_][\w$]*\.)?([A-Za-z_][\w$]*)", "indice"),
    (r"DROP\s+TRIGGER\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][\w$]*)", "disparador"),
]
for ruta in archivos:
    contenido = sin_comentarios[ruta]
    for patron, clase in PATRONES_DROP:
        for m in re.finditer(patron, contenido, re.I):
            nombre = m.group(1)
            bajo = nombre.lower()
            if bajo in creados or bajo in allow or IMPLICITO.search(bajo):
                continue
            hallazgos.append(
                f"{ruta}:{linea_de(contenido, m.start())}: se retira la {clase} "
                f"'{nombre}', que ninguna migracion crea"
            )

if hallazgos:
    print("Migraciones que no hacen lo que dicen. Un DROP que no encuentra su objeto no falla", file=sys.stderr)
    print("-por el IF EXISTS- y la migracion figura como aplicada. Revisar el nombre real, o", file=sys.stderr)
    print("anotarlo en ops/scaffold/migration-drops-allowlist.txt con su motivo.", file=sys.stderr)
    print("", file=sys.stderr)
    for h in sorted(set(hallazgos)):
        print(h, file=sys.stderr)
    sys.exit(1)

print("check-migration-drops: OK")
PY
