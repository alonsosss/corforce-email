#!/usr/bin/env bash
# Guardarrail: los paneles de Grafana (ops/observability/grafana/dashboards) son JSON valido, con uid unico,
# usan solo origenes de datos aprovisionados y consultan solo metricas que el codigo de mail-security de verdad
# publica.
#
# Por que: un panel con el nombre de una metrica mal escrito no falla: se queda en blanco ("sin datos") y se
# descubre durante el incidente que tenia que mostrar. Es el mismo motivo por el que las alertas tienen pruebas
# (ops/observability/check-alertas.sh). Comprueba ademas que la comprobacion muerde, con un panel de prueba que
# consulta una metrica inventada y otro con un origen que no existe.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DASH="$ROOT/ops/observability/grafana/dashboards"
PROV="$ROOT/ops/observability/grafana/provisioning/datasources"
SRC="$ROOT/services/mail-security"
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

verificar() { # verificar <directorio de paneles>: imprime cada incumplimiento
  python3 - "$1" "$PROV" "$SRC" <<'PY'
import glob, json, os, re, sys

dash, prov, src = sys.argv[1:4]
uids_ds = set()
for f in glob.glob(os.path.join(prov, "*.yml")):
    uids_ds |= set(re.findall(r"^\s+uid:\s*(\S+)", open(f, encoding="utf-8").read(), re.M))

publicadas = set()
for raiz, _, ficheros in os.walk(src):
    for nombre in ficheros:
        if nombre.endswith(".go") and not nombre.endswith("_test.go"):
            publicadas |= set(re.findall(r'Name:\s*"(mail_security_[a-z0-9_]+)"', open(os.path.join(raiz, nombre), encoding="utf-8").read()))

problemas, vistos = [], {}
for f in sorted(glob.glob(os.path.join(dash, "*.json"))):
    nombre = os.path.basename(f)
    try:
        d = json.load(open(f, encoding="utf-8"))
    except Exception as e:
        problemas.append("%s: no es JSON valido (%s)" % (nombre, e)); continue
    uid = d.get("uid")
    if not uid:
        problemas.append("%s: sin uid" % nombre)
    elif uid in vistos:
        problemas.append("%s: el uid %s ya lo usa %s" % (nombre, uid, vistos[uid]))
    else:
        vistos[uid] = nombre
    for p in d.get("panels", []):
        ds = (p.get("datasource") or {}).get("uid")
        if ds not in uids_ds:
            problemas.append("%s, panel '%s': origen de datos '%s' no aprovisionado (hay %s)" % (nombre, p.get("title"), ds, sorted(uids_ds)))
        for t in p.get("targets", []):
            for m in re.findall(r"\bmail_security_[a-z0-9_]+", t.get("expr", "")):
                if m not in publicadas:
                    problemas.append("%s, panel '%s': la metrica %s no la publica mail-security" % (nombre, p.get("title"), m))
print("\n".join(problemas))
sys.exit(1 if problemas else 0)
PY
}

echo "  Paneles reales:"
if out="$(verificar "$DASH")"; then echo "  OK: paneles validos, con origenes aprovisionados y metricas que existen"; else while IFS= read -r l; do falla "$l"; done <<<"$out"; fi

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
panel() { # panel <uid> <origen> <expr>
  printf '{"uid":"%s","title":"t","panels":[{"title":"p","datasource":{"uid":"%s"},"targets":[{"expr":"%s"}]}]}' "$1" "$2" "$3"
}
echo "  Mutaciones (cada una tiene que fallar con su mensaje):"
panel a prometheus 'mail_security_postfix_queue_messages' >"$TMP/a.json"
if verificar "$TMP" >/dev/null; then echo "    OK  un panel correcto no falla"; else falla "un panel correcto fallo"; fi
panel b prometheus 'mail_security_metrica_inventada_total' >"$TMP/b.json"
out="$(verificar "$TMP" 2>&1)"; [[ $? -ne 0 && "$out" == *"metrica_inventada"* ]] && echo "    OK  una metrica que no existe" || falla "no detecta una metrica inventada"
rm -f "$TMP/b.json"; panel c grafito 'up' >"$TMP/c.json"
out="$(verificar "$TMP" 2>&1)"; [[ $? -ne 0 && "$out" == *"grafito"* ]] && echo "    OK  un origen sin aprovisionar" || falla "no detecta un origen sin aprovisionar"
rm -f "$TMP/c.json"; panel a prometheus 'up' >"$TMP/d.json"
out="$(verificar "$TMP" 2>&1)"; [[ $? -ne 0 && "$out" == *"ya lo usa"* ]] && echo "    OK  un uid repetido" || falla "no detecta un uid repetido"
rm -f "$TMP/d.json"; echo '{' >"$TMP/e.json"
out="$(verificar "$TMP" 2>&1)"; [[ $? -ne 0 && "$out" == *"no es JSON"* ]] && echo "    OK  un JSON roto" || falla "no detecta un JSON roto"

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: los paneles de Grafana consultan lo que existe."
