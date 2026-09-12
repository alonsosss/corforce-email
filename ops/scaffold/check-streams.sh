#!/usr/bin/env bash
# Detecta streams de JetStream cuyos subjects se solapan.
#
# NATS rechaza un stream cuyos subjects pisen los de otro ("subjects overlap with an
# existing stream"), y el rechazo llega en tiempo de arranque: el servicio levanta igual,
# solo que su worker nunca se suscribe. Es una falla silenciosa que solo se ve leyendo
# logs, asi que se detecta aqui, sobre el codigo.
#
# La convencion del proyecto es un stream por subject concreto. Un comodin (`dominio.>`)
# pisa a cualquier stream existente de ese dominio.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FAIL=0

echo "== Streams de JetStream con subjects solapados =="

# Cada declaracion de stream -> lineas "NOMBRE<TAB>subject". Se leen:
#   Go:     EnsureStream / EnsureStreamWithMaxAge("NOMBRE", []string{...}, ...)
#           AddStream(&nats.StreamConfig{Name: ..., Subjects: []string{...}})
#   Python: add_stream(name=..., subjects=[...]) y _ensure_stream(js, NOMBRE, [...], ...)
# en services/ y en pkg/. Los tres streams del bot (agent-sales, Python) y la DLQ
# de pkg/events eran invisibles y el guardian daba OK sin mirarlos.
# Las constantes se resuelven contra su declaracion en el mismo archivo.
pairs=$(python3 - "$ROOT" <<'PY'
import os, re, sys

root = sys.argv[1]
# Llamada con la lista ESCRITA en el sitio: EnsureStream(NOMBRE, []string{...}).
call = re.compile(r'EnsureStream(?:WithMaxAge)?\(\s*([^,]+?)\s*,\s*\[\]string\{([^}]*)\}', re.S)
# Llamada con CUALQUIER segundo argumento: sirve para detectar las que la de arriba no
# entiende (una variable, un parametro) y para resolverlas contra el propio archivo.
call_any = re.compile(r'EnsureStream(?:WithMaxAge)?\(\s*([A-Za-z_][\w.]*)\s*,\s*([A-Za-z_][\w.]*)\s*\)')
# Llamada cuya lista la da una FUNCION del paquete compartido: EnsureStream(x.Stream,
# x.Subjects()). El cuerpo de esa funcion se lee mas abajo, en funcs_subjects.
call_func = re.compile(r'EnsureStream(?:WithMaxAge)?\(\s*([A-Za-z_][\w.]*)\s*,\s*([A-Za-z_][\w.]*)\(\)\s*\)')
# func Subjects() []string { return []string{A, B} }
func_subjects = re.compile(r'func\s+([A-Za-z_][\w]*)\(\)\s*\[\]string\s*\{[^}]*?\[\]string\{([^}]*)\}', re.S)
# Declaracion de listas: `nombre = []string{...}` (dentro o fuera de un bloque var).
slice_decl = re.compile(r'([A-Za-z_][\w]*)\s*:?=\s*\[\]string\{([^}]*)\}', re.S)
# Slice de structs cuyo PRIMER campo es el subject, y el bucle que lo vuelca en una
# lista (`for _, x := range FUENTE { s = append(s, x.subject) }`). Es la forma que usa
# notification para sus canales de aprobacion.
struct_src = re.compile(r'var\s+([A-Za-z_][\w]*)\s*=\s*\[\]struct\s*\{.*?\n\}\{(.*?)\n\}', re.S)
volcado = re.compile(r'range\s+([A-Za-z_][\w]*)\s*\{[^}]*?append\(\s*([A-Za-z_][\w]*)\s*,\s*[A-Za-z_][\w]*\.subject', re.S)
# Mapa literal stream -> subjects, la forma del range: NOMBRE: {..} o NOMBRE: variable.
map_range = re.compile(r'map\[string\]\[\]string\{(.*?)\n\s*\}', re.S)
map_entry = re.compile(r'([A-Za-z_][\w.]*)\s*:\s*(\{[^}]*\}|[A-Za-z_][\w]*)')
addstream = re.compile(r'StreamConfig\{[^}]*?Name:\s*([^,\n]+),[^}]*?Subjects:\s*\[\]string\{([^}]*)\}', re.S)
py_add = re.compile(r'add_stream\(\s*name\s*=\s*([^,]+),\s*subjects\s*=\s*\[([^\]]*)\]', re.S)
py_ensure = re.compile(r'_ensure_stream\(\s*\w+\s*,\s*([^,]+),\s*\[([^\]]*)\]', re.S)
# Constantes de cadena. Acepta la forma de bloque (`const (\n  X = "..."\n)`) y
# tambien la de una sola linea (`const X = "..."`), que es igual de valida en Go y
# hasta ahora dejaba el stream entero invisible para este guardian.
const = re.compile(r'^\s*(?:const\s+|var\s+)?(_?[A-Za-z_][A-Za-z0-9_]*)\s*(?::\s*str)?\s*=\s*"([^"]*)"', re.M)
lit = re.compile(r'"([^"]*)"')

def walk():
    for base in ('services', 'pkg'):
        for dirpath, _, files in os.walk(os.path.join(root, base)):
            if 'node_modules' in dirpath or '.venv' in dirpath:
                continue
            for fn in files:
                yield dirpath, fn

sin_entender = []

# Cuerpos de las funciones que declaran los subjects de un stream, indexados por
# PAQUETE y nombre. Por paquete y no solo por nombre porque hay varias `Subjects()` en
# el arbol -- una por cada contrato compartido -- y mezclarlas le daria a un stream los
# asuntos de otro, que es exactamente el error que este guardian busca.
blobs_subjects = {}
paquete_re = re.compile(r'^package\s+([A-Za-z_][\w]*)', re.M)

# Constantes por SERVICIO: un subject suele vivir como constante del paquete domain del
# mismo servicio (domain.SubjectWeighing). Resolverlas solo dentro del fichero dejaba
# ciegas esas declaraciones.
consts_por_ambito = {}
consts_compartidas = {}

def ambito(path):
    rel = os.path.relpath(path, root)
    partes = rel.split(os.sep)
    return os.sep.join(partes[:2]) if len(partes) > 1 else rel

for dirpath, fn in walk():
    if not fn.endswith('.go') or fn.endswith('_test.go'):
        continue
    ruta = os.path.join(dirpath, fn)
    try:
        texto = open(ruta, encoding='utf-8', errors='replace').read()
    except OSError:
        continue
    hallazgos = dict(const.findall(texto))
    mp = paquete_re.search(texto)
    consts_por_ambito.setdefault(ambito(ruta), {}).update(hallazgos)
    if mp:
        for fn_nombre, blob in func_subjects.findall(texto):
            blobs_subjects[(mp.group(1), fn_nombre)] = blob
    # Las constantes de pkg/ son COMPARTIDAS por diseno: un stream declarado en
    # pkg/approvals lo usa services/workflow, y resolver solo dentro del ambito dejaba
    # esas declaraciones invisibles para este guardian -- justo las de los paquetes que
    # existen para que varios servicios compartan un contrato.
    if ambito(ruta).startswith('pkg' + os.sep) and mp:
        for k, v in hallazgos.items():
            consts_compartidas[(mp.group(1), k)] = v

for dirpath, fn in walk():
        es_go = fn.endswith('.go') and not fn.endswith('_test.go')
        es_py = fn.endswith('.py') and not fn.startswith('test_') and '/tests' not in dirpath and '/tools' not in dirpath
        if not (es_go or es_py):
            continue
        path = os.path.join(dirpath, fn)
        rel = os.path.relpath(path, root)
        src = open(path, encoding='utf-8', errors='replace').read()
        if es_go and 'EnsureStream' not in src and 'StreamConfig{' not in src:
            continue
        if es_py and 'add_stream(' not in src and '_ensure_stream(' not in src:
            continue
        # Constantes visibles: las del propio archivo alcanzan, es donde se declaran.
        consts = dict(const.findall(src))

        propias = consts_por_ambito.get(ambito(path), {})

        def resolve(tok):
            tok = tok.strip()
            m = lit.fullmatch(tok)
            if m:
                return m.group(1)
            if tok in consts:
                return consts[tok]
            # domain.SubjectWeighing -> constante del mismo servicio;
            # approvals.SubjectRequested -> constante de un paquete compartido.
            partes = tok.split('.')
            corto = partes[-1]
            # approvals.Stream y workinbox.Stream son constantes DISTINTAS con el
            # mismo nombre corto: sin el paquete, una tapaba a la otra y el stream de
            # la tapada desaparecia de la vigilancia sin que nadie lo notara.
            if len(partes) == 2:
                compartida = consts_compartidas.get((partes[0], corto))
                if compartida:
                    return compartida
            return propias.get(corto)

        def subjects_de(blob):
            out = []
            for tok in blob.split(','):
                tok = tok.strip().strip('{}').strip()
                if not tok:
                    continue
                s = resolve(tok)
                if s:
                    out.append(s)
            return out

        # Listas declaradas como variable en el mismo archivo (var X = []string{...}).
        listas = {n: subjects_de(b) for n, b in slice_decl.findall(src)}
        # Listas que devuelve una funcion de un paquete compartido (approvals.Subjects()).
        # El cuerpo puede estar en OTRO archivo: se busca por paquete y nombre.
        def subjects_de_funcion(tok):
            partes = tok.split('.')
            if len(partes) != 2:
                return []
            paquete = partes[0]
            blob = blobs_subjects.get((paquete, partes[1]))
            if not blob:
                return []
            # Dentro de su paquete los asuntos se nombran SIN prefijo
            # (`[]string{SubjectRequested, ...}`), asi que hay que resolverlos contra
            # las constantes de ESE paquete y no contra las del archivo que llama.
            out = []
            for tok2 in blob.split(','):
                tok2 = tok2.strip().strip('{}').strip()
                if not tok2:
                    continue
                m2 = lit.fullmatch(tok2)
                valor = m2.group(1) if m2 else consts_compartidas.get((paquete, tok2.split('.')[-1]))
                if valor:
                    out.append(valor)
            return out
        # Lista volcada desde un slice de structs: se leen los literales de la fuente.
        estructuras = {n: [m for m in re.findall(r'\{\s*"([^"]+)"', cuerpo)] for n, cuerpo in struct_src.findall(src)}
        for fuente, destino in volcado.findall(src):
            if estructuras.get(fuente):
                listas[destino] = estructuras[fuente]

        found = []
        if es_go:
            found += call.findall(src) + addstream.findall(src)
            # Mapas stream -> subjects (la forma `for s, subj := range map[string][]string{...}`).
            for cuerpo in map_range.findall(src):
                for name_tok, val in map_entry.findall(cuerpo):
                    subj = subjects_de(val) if val.startswith('{') else listas.get(val, [])
                    if subj:
                        found.append((name_tok, ','.join('"%s"' % x for x in subj)))
            # Llamadas cuya lista la da una funcion compartida: EnsureStream(x.Stream, x.Subjects()).
            for name_tok, fn_tok in call_func.findall(src):
                subj = subjects_de_funcion(fn_tok)
                if subj:
                    found.append((name_tok, ','.join('"%s"' % x for x in subj)))
            # Llamadas cuyo segundo argumento es una variable: se resuelven contra el archivo.
            #
            # Si NO se resuelve la lista, se reporta; y tambien si no se resuelve el
            # NOMBRE. Antes solo lo primero: un stream cuyo nombre el guardian no
            # sabia leer -por ejemplo `const X = "..."` en una sola linea, fuera de
            # un bloque const- se descartaba en silencio y el stream entero quedaba
            # sin vigilar, que es justo lo que este guardian existe para impedir.
            # Una llamada dentro de `for nombre, subjects := range map[...]` no se
            # puede resolver por sus tokens -son variables del bucle-, pero el
            # propio mapa ya se leyo mas arriba. Reportarla seria un falso aviso.
            cubierto_por_mapa = bool(map_range.findall(src))
            for name_tok, var_tok in call_any.findall(src):
                subj = listas.get(var_tok)
                if subj and resolve(name_tok):
                    found.append((name_tok, ','.join('"%s"' % x for x in subj)))
                elif not cubierto_por_mapa:
                    sin_entender.append(f"{rel}: EnsureStream({name_tok}, {var_tok})")
        else:
            found = py_add.findall(src) + [m for m in py_ensure.findall(src)]
        for name_tok, subj_blob in found:
            name = resolve(name_tok)
            if not name:
                continue
            for s in subjects_de(subj_blob):
                print(f"{name}\t{s}\t{rel}")

# Una declaracion que el guardian no entiende es peor que ninguna: da OK sobre streams
# que no ha mirado. Se avisa por una linea aparte que el script convierte en fallo.
for x in sorted(set(sin_entender)):
    print(f"!SIN-ENTENDER\t{x}\t-")
PY
)

if [[ -z "$pairs" ]]; then
  echo "  OK: no se declaran streams."
  exit 0
fi

# Declaraciones que el analizador no supo leer: se avisan y fallan. Ignorarlas era el
# fallo silencioso del propio guardian (daba OK sobre streams que nunca habia mirado).
opacas=$(printf '%s\n' "$pairs" | grep -F '!SIN-ENTENDER' || true)
if [[ -n "$opacas" ]]; then
  echo "  FALLA: hay declaraciones de stream que este guardian no sabe leer:"
  printf '%s\n' "$opacas" | cut -f2 | sed 's/^/         /'
  echo "         Declara los subjects en el sitio de la llamada, o enseña al script a resolver esa forma."
  FAIL=1
fi
pairs=$(printf '%s\n' "$pairs" | grep -Fv '!SIN-ENTENDER' || true)

# Dos subjects se solapan si son iguales o si uno es prefijo comodin del otro.
if ! python3 - <<PY
import sys
from collections import defaultdict

rows = [l.split('\t') for l in """$pairs""".strip().splitlines()]
by_stream = defaultdict(list)
for name, subject, path in rows:
    by_stream[name].append((subject, path))

def covers(pattern, subject):
    if pattern == subject:
        return True
    if pattern.endswith('.>'):
        return subject.startswith(pattern[:-1])
    return False

fail = False
names = sorted(by_stream)
for i, a in enumerate(names):
    for b in names[i + 1:]:
        for sa, pa in by_stream[a]:
            for sb, pb in by_stream[b]:
                if covers(sa, sb) or covers(sb, sa):
                    print(f"  FALLA: los streams '{a}' ({sa}) y '{b}' ({sb}) se solapan")
                    print(f"         {pa} / {pb}")
                    print("         NATS rechaza el segundo y su worker nunca se suscribe.")
                    fail = True
sys.exit(1 if fail else 0)
PY
then
  FAIL=1
fi

[[ $FAIL -eq 0 ]] && echo "  OK: ningun stream pisa los subjects de otro ($(printf '%s\n' "$pairs" | cut -f1 | sort -u | wc -l) streams declarados: $(printf '%s\n' "$pairs" | cut -f1 | sort -u | tr '\n' ' '))."
exit $FAIL
