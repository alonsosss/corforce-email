#!/usr/bin/env bash
# Guardarrail: cada servicio recibe SU credencial de base de datos y ninguna otra.
#
# El reparto vive en tres sitios que tienen que decir lo mismo, y este script los ata:
#   ops/db/service-credentials.json  el plano de cada servicio (quien puede recibir que)
#   docker-compose.yml               lo que cada contenedor recibe de verdad
#   ops/security/secrets/secret-keys-db.txt  las credenciales que el almacen materializa
#   migrations/{tenant,registry}/... los permisos del rol al que esa credencial da entrada
#
# Por que existe: el reparto se sostiene sobre una convencion, y una convencion sin
# comprobacion dura hasta el siguiente servicio. Un servicio nuevo que copie el bloque de
# compose de otro heredaria la credencial de aquel, o se quedaria con la de plataforma -que
# es duena de TODAS las bases- sin que nada lo dijera. Aqui eso falla en CI.
#
# No mira valores: solo que cada bloque pase las variables de su plano y ninguna de otro.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

python3 - <<'PY'
import json
import os
import re
import sys

matriz = json.load(open("ops/db/service-credentials.json", encoding="utf-8"))
servicios = matriz["servicios"]
enrutado = matriz["enrutado"]

fallos = []

# ── Credenciales declaradas en el almacen ────────────────────────────────────
claves_db = {
    l.strip().rstrip("?") for l in open("ops/security/secrets/secret-keys-db.txt", encoding="utf-8")
    if l.strip() and not l.startswith("#")
}
esperadas = {"POSTGRES_PASSWORD", "CELL_DB_PASSWORD", "MAIL_DB_PASSWORD", enrutado["secreto"]}
for nombre, s in servicios.items():
    if s.get("plane") == "tenant":
        esperadas.add(nombre.upper().replace("-", "_") + "_DB_PASSWORD")
if faltan := sorted(esperadas - claves_db):
    fallos.append("secret-keys-db.txt no declara: " + ", ".join(faltan))
# Lo contrario tambien importa: una credencial que el almacen materializa y que ya no usa
# nadie se queda viva en produccion sin que nadie la rote.
if sobran := sorted(claves_db - esperadas):
    fallos.append("secret-keys-db.txt declara credenciales que no usa ningun servicio: " + ", ".join(sobran))

# Ninguna credencial de base puede estar en la lista COMPARTIDA: de ahi va a env_file, y
# env_file llega entero a cada contenedor que lo declara.
claves_compartidas = {
    l.strip().rstrip("?") for l in open("ops/security/secrets/secret-keys.txt", encoding="utf-8")
    if l.strip() and not l.startswith("#")
}
if cruce := sorted(claves_compartidas & claves_db):
    fallos.append("credenciales de base en la lista compartida (las veria todo el despliegue): " + ", ".join(cruce))

# ── Lo que cada bloque de compose recibe ─────────────────────────────────────
def bloques(ruta):
    svc_re = re.compile(r"^  ([A-Za-z0-9_.-]+):\s*$")
    out, actual, en_servicios = {}, None, False
    for linea in open(ruta, encoding="utf-8"):
        if re.match(r"^\S", linea):
            en_servicios, actual = linea.rstrip() == "services:", None
            continue
        m = svc_re.match(linea)
        if en_servicios and m:
            actual = m.group(1)
            out[actual] = []
        elif en_servicios and actual:
            out[actual].append(linea.split("#", 1)[0])
    return out

compose = bloques("docker-compose.yml")

def lineas_por_plano(nombre, s):
    up = nombre.upper().replace("-", "_")
    plane = s.get("plane")
    if plane == "registry":
        return ["POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-}"]
    if plane == "tenant":
        return [
            "POSTGRES_PASSWORD: ${TENANT_SERVICES_PLATFORM_PASSWORD:-}",
            "REGISTRY_DB_PASSWORD: ${TENANT_ROUTER_DB_PASSWORD:-}",
            f"TENANT_DB_USER: mail_svc_{s['schema']}",
            "TENANT_DB_PASSWORD: ${%s_DB_PASSWORD:-}" % up,
        ]
    if plane == "cell":
        # El POSTGRES_PASSWORD de un servicio de celda es el respaldo de DESARROLLO
        # (CELL_SERVICES_PLATFORM_PASSWORD), vacio en produccion: no es la credencial de
        # plataforma, y por eso se exige escrito asi y no de cualquier forma.
        return [
            "CELL_DB_PASSWORD: ${CELL_DB_PASSWORD:-}",
            "POSTGRES_PASSWORD: ${CELL_SERVICES_PLATFORM_PASSWORD:-}",
        ]
    return []

# Toda credencial de base: la que no le toca a un servicio no puede aparecer en su bloque.
todas = {"POSTGRES_PASSWORD", "CELL_DB_PASSWORD", "TENANT_DB_PASSWORD", "REGISTRY_DB_PASSWORD"}

for nombre, cuerpo in compose.items():
    texto = "".join(cuerpo)
    if "dockerfile: ./services/" not in texto:
        continue  # infraestructura de terceros y la web: no abren ninguna base de la plataforma
    s = servicios.get(nombre)
    if s is None:
        fallos.append(
            f"{nombre} recibe el fichero de secretos y no figura en ops/db/service-credentials.json: "
            "declara su plano (registry, tenant, cell o none)")
        continue
    esperado = lineas_por_plano(nombre, s)
    for linea in esperado:
        if linea not in texto:
            fallos.append(f"{nombre}: falta en environment: {linea}")
    for var in todas - {l.split(":")[0] for l in esperado}:
        if re.search(r"^\s+%s\s*:" % var, texto, re.M):
            fallos.append(f"{nombre}: recibe {var}, que no es de su plano ({s.get('plane')})")

# Un servicio de la matriz que ya no esta en compose es una entrada muerta que confunde.
for nombre in servicios:
    if nombre not in compose:
        fallos.append(f"{nombre} figura en service-credentials.json y no es un servicio de docker-compose.yml")

# ── El rol al que da entrada esa credencial tiene sus permisos declarados ────
for nombre, s in sorted(servicios.items()):
    if s.get("plane") != "tenant":
        continue
    esquema = s["schema"]
    directorio = f"migrations/tenant/canonical/{nombre}"
    if not os.path.isdir(directorio):
        fallos.append(f"{nombre}: no existe {directorio}")
        continue
    concede = any(
        f"TO {esquema}_service" in open(os.path.join(directorio, f), encoding="utf-8").read()
        for f in os.listdir(directorio) if f.endswith(".sql")
    )
    if not concede:
        fallos.append(
            f"{nombre}: ninguna migracion de {directorio} concede permisos a {esquema}_service; "
            "su credencial daria entrada a un rol sin permisos")

# ── userlist.txt no puede acabar versionado ──────────────────────────────────
# Lo genera ops/db/pgbouncer-userlist.sh dentro del directorio que se sincroniza al
# servidor, y lleva las contrasenas EN CLARO que exige auth_type = plain. Un `git add`
# distraido las publicaria en la historia del repositorio, de donde no se borran.
import subprocess

seguidos = subprocess.run(["git", "ls-files", "--", "pgbouncer/userlist.txt"],
                          capture_output=True, text=True).stdout.strip()
if seguidos:
    fallos.append("pgbouncer/userlist.txt esta versionado y lleva contrasenas en claro: "
                  "sacalo del indice (git rm --cached) y rota lo que haya quedado expuesto")

if fallos:
    print("Reparto de credenciales de base incoherente:", file=sys.stderr)
    for f in fallos:
        print(f"  {f}", file=sys.stderr)
    print("", file=sys.stderr)
    print("Regla: cada servicio recibe solo la credencial de su plano "
          "(ops/db/service-credentials.json).", file=sys.stderr)
    sys.exit(1)

print(f"  OK: {len(servicios)} servicios con la credencial de su plano y ninguna ajena.")
PY
