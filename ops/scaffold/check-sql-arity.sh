#!/usr/bin/env bash
# Un INSERT cuyas columnas, placeholders y argumentos no cuadran compila perfectamente y
# falla en produccion, en la primera escritura real. Es el error mas facil de introducir
# al añadir una columna a una sentencia existente, y el mas caro de descubrir tarde.
#
# Comprueba, sobre los INSERT literales de una sola pieza: n columnas == n placeholders.
# El numero de argumentos Go no se puede contar de forma fiable (hay expresiones con
# comas), asi que esto cubre el desajuste columna/placeholder, que es el habitual.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "== Aridad de los INSERT (columnas vs placeholders) =="
python3 - "$ROOT" <<'PY'
import os, re, sys
root = sys.argv[1]
pat = re.compile(r'INSERT\s+INTO\s+([a-z_]+\.[a-z_]+)\s*\(([^)]*)\)\s*VALUES\s*\(', re.I | re.S)

def split_top(text):
    """Divide por comas de primer nivel: NOW(), coalesce(a,b) cuentan como UN valor."""
    out, depth, cur = [], 0, ''
    for ch in text:
        if ch == '(':
            depth += 1
        elif ch == ')':
            depth -= 1
        if ch == ',' and depth == 0:
            out.append(cur); cur = ''
        else:
            cur += ch
    if cur.strip():
        out.append(cur)
    return [c for c in out if c.strip()]

def values_body(src, start):
    """Devuelve el contenido del parentesis de VALUES, equilibrando anidamiento."""
    depth, i = 1, start
    while i < len(src) and depth:
        if src[i] == '(':
            depth += 1
        elif src[i] == ')':
            depth -= 1
        i += 1
    return src[start:i-1], i

fail = 0
for dirpath, _, files in os.walk(os.path.join(root, 'services')):
    for fn in files:
        if not fn.endswith('.go'):
            continue
        path = os.path.join(dirpath, fn)
        src = open(path, encoding='utf-8', errors='replace').read()
        for m in pat.finditer(src):
            table, cols = m.group(1), m.group(2)
            body, _ = values_body(src, m.end())
            # SQL armado por concatenacion o con multiples filas: no se puede contar.
            if any(t in cols for t in ('`', '"', '+')) or any(t in body for t in ('`', '"', '+')):
                continue
            if '),' in body or ',(' in body:
                continue
            ncols = len(split_top(cols))
            nvals = len(split_top(body))
            if ncols and nvals and ncols != nvals:
                line = src[:m.start()].count('\n') + 1
                print(f"  FALLA: {os.path.relpath(path, root)}:{line} {table}: "
                      f"{ncols} columnas y {nvals} valores")
                fail = 1
sys.exit(fail)
PY
rc=$?
[[ $rc -eq 0 ]] && echo "  OK: todos los INSERT literales cuadran."
exit $rc
