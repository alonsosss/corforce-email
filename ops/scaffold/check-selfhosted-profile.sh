#!/usr/bin/env bash
# Guardarrail del perfil de produccion autoalojada (docker-compose.selfhosted.yml), sin docker.
#
# El perfil repite a proposito parte de docker-compose.yml (command no se fusiona) y reparte un
# mismo dato entre ficheros que no se leen entre si. Aqui se atan:
#   - los parametros de Postgres y de Redis del compose base siguen en el perfil, y el perfil no
#     vuelve a poner la contrasena de Redis en la linea de ordenes ni un puerto en claro;
#   - todo servicio Go del compose recibe REDIS_TLS y la CA interna (uno que falte arrancaria sin
#     TLS y no arrancaria en production);
#   - pg_hba.conf no admite ninguna conexion TCP sin TLS ni sin contrasena;
#   - los puntos de montaje de internal-tls.sh son los del perfil;
#   - el cuerpo maximo del proxy de borde cubre el mayor envio del webmail y la importacion de
#     contactos de .env.example;
#   - los rangos de Cloudflare son CIDR validos, y la imagen del borde va por digest;
#   - ops/maintenance/perfil-despliegue.sh decide bien, y los dos despliegues lo usan.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

python3 - "$ROOT" <<'PY'
import ipaddress
import os
import re
import subprocess
import sys

root = sys.argv[1]
fallos = []


def leer(rel):
    with open(os.path.join(root, rel), encoding="utf-8") as fh:
        return fh.read()


def bloques(texto):
    """Servicios de primer nivel bajo services: -> lineas de su bloque (sin comentarios)."""
    out, actual, dentro = {}, None, False
    for linea in texto.splitlines():
        if re.match(r"^\S", linea):
            dentro, actual = linea.rstrip() == "services:", None
            continue
        m = re.match(r"^  ([A-Za-z0-9_.-]+):\s*$", linea)
        if dentro and m:
            actual = m.group(1)
            out[actual] = []
        elif dentro and actual:
            sin = linea.split(" #", 1)[0] if not linea.lstrip().startswith("#") else ""
            out[actual].append(sin.rstrip())
    return out


def lista_command(lineas):
    """Elementos de `command:` en forma de lista YAML de un bloque."""
    items, dentro = [], False
    for l in lineas:
        if re.match(r"^    command:\s*$", l):
            dentro = True
            continue
        if dentro:
            m = re.match(r"^      - (.*)$", l)
            if m:
                items.append(m.group(1).strip().strip('"'))
            elif re.match(r"^    \S", l):
                break
    return items


base = bloques(leer("docker-compose.yml"))
perfil_txt = leer("docker-compose.selfhosted.yml")
perfil = bloques(perfil_txt)

# --- Postgres ---------------------------------------------------------------------------------
base_pg = " ".join(base["postgres-primary"])
m = re.search(r"command:\s*>\s*(.*?)(environment:|$)", base_pg)
base_params = re.findall(r"-c\s+(\S+)", m.group(1) if m else "")
if not base_params:
    fallos.append("no se leen los -c de postgres-primary en docker-compose.yml")
cmd_pg = lista_command(perfil.get("postgres-primary", []))
pares = {cmd_pg[i + 1] for i, x in enumerate(cmd_pg[:-1]) if x == "-c"}
for p in base_params:
    if p not in pares:
        fallos.append(f"postgres-primary: el perfil pierde '-c {p}' de docker-compose.yml")
for requerido in ("ssl=on", "ssl_min_protocol_version=TLSv1.2", "hba_file=/etc/core-force-mail/postgres/pg_hba.conf"):
    if requerido not in pares:
        fallos.append(f"postgres-primary: falta '-c {requerido}' en el perfil")

# --- pg_hba -----------------------------------------------------------------------------------
hba = [l.split("#", 1)[0].split() for l in leer("selfhosted/postgres/pg_hba.conf").splitlines()]
hba = [c for c in hba if c]
for c in hba:
    tipo, metodo = c[0], c[-1]
    if tipo in ("host", "hostgssenc", "hostnogssenc"):
        fallos.append(f"pg_hba.conf: '{' '.join(c)}' admite TCP sin exigir TLS")
    if tipo == "hostssl" and metodo != "scram-sha-256":
        fallos.append(f"pg_hba.conf: '{' '.join(c)}' usa {metodo}; solo scram-sha-256")
    if tipo == "hostnossl" and metodo != "reject":
        fallos.append(f"pg_hba.conf: '{' '.join(c)}' admite TCP en claro")
    if tipo == "local" and metodo not in ("trust", "peer", "scram-sha-256"):
        fallos.append(f"pg_hba.conf: metodo local inesperado '{metodo}'")
rechazos = {c[3] for c in hba if c[0] == "hostnossl" and c[-1] == "reject"}
for red in ("0.0.0.0/0", "::/0"):
    if red not in rechazos:
        fallos.append(f"pg_hba.conf: falta 'hostnossl all all {red} reject'")

# --- Redis ------------------------------------------------------------------------------------
base_redis = " ".join(base["redis"])
m = re.search(r"command:\s*redis-server\s+(.*)", base_redis)
tokens = (m.group(1).split("environment:")[0].split() if m else [])
opciones, i = [], 0
while i < len(tokens):
    if tokens[i].startswith("--"):
        valor = tokens[i + 1] if i + 1 < len(tokens) and not tokens[i + 1].startswith("--") else ""
        opciones.append((tokens[i], valor))
        i += 2 if valor else 1
    else:
        i += 1
cmd_redis = lista_command(perfil.get("redis", []))
pares_redis = {(cmd_redis[i], cmd_redis[i + 1]) for i in range(len(cmd_redis) - 1) if cmd_redis[i].startswith("--")}
if not opciones:
    fallos.append("no se leen las opciones de redis en docker-compose.yml")
for opt, val in opciones:
    if opt == "--requirepass":
        continue
    if (opt, val) not in pares_redis:
        fallos.append(f"redis: el perfil pierde '{opt} {val}' de docker-compose.yml")
if "--requirepass" in cmd_redis:
    fallos.append("redis: el perfil pone la contrasena en la linea de ordenes")
if ("--port", "0") not in pares_redis:
    fallos.append("redis: el perfil deja el puerto en claro (falta --port 0)")
if not any(o == "--tls-port" for o, _ in pares_redis):
    fallos.append("redis: el perfil no abre --tls-port")
if not any(l.strip().startswith("entrypoint:") and "/etc/core-force-mail/redis/entrypoint.sh" in l for l in perfil.get("redis", [])) or \
        "./selfhosted/redis:/etc/core-force-mail/redis:ro" not in "\n".join(perfil.get("redis", [])):
    fallos.append("redis: el perfil no arranca por selfhosted/redis/entrypoint.sh")

# --- servicios Go -----------------------------------------------------------------------------
mapa = subprocess.run(["bash", os.path.join(root, "ops/scaffold/service-paths.sh"), "--map"],
                      capture_output=True, text=True, check=True).stdout
go = [l.split("\t")[0] for l in mapa.splitlines() if l.endswith("\tgo")]
if not go:
    fallos.append("service-paths.sh --map no devuelve servicios Go")
for svc in go:
    cuerpo = "\n".join(perfil.get(svc, []))
    if not re.search(r"environment:\s*\*go-service-env|<<:\s*\*go-service-env", cuerpo):
        fallos.append(f"{svc}: el perfil no le da REDIS_TLS ni la CA interna (*go-service-env)")
    if not re.search(r"volumes:\s*\*internal-ca", cuerpo):
        fallos.append(f"{svc}: el perfil no le monta la CA interna (*internal-ca)")
m = re.search(r"x-go-service-env: &go-service-env\n((?:  .*\n)+)", perfil_txt)
env_go = dict(re.findall(r"^  ([A-Z_]+):\s*\"?([^\"\n]*)\"?", m.group(1), re.M)) if m else {}
if env_go.get("REDIS_TLS") != "true":
    fallos.append("x-go-service-env: REDIS_TLS no es \"true\"")
if env_go.get("REDIS_TLS_SERVER_NAME") != "redis":
    fallos.append("x-go-service-env: REDIS_TLS_SERVER_NAME no es el nombre del servicio redis (SAN del certificado)")

# --- puntos de montaje compartidos con internal-tls.sh ----------------------------------------
tls = leer("ops/security/internal-tls.sh")
for var in ("MONTAJE_PUBLICO", "MONTAJE_REDIS"):
    m = re.search(rf"^{var}=(\S+)$", tls, re.M)
    if not m:
        fallos.append(f"internal-tls.sh: falta {var}")
        continue
    if f":{m.group(1)}:ro" not in perfil_txt:
        fallos.append(f"internal-tls.sh: {var}={m.group(1)} no es un punto de montaje del perfil")
if env_go.get("REDIS_TLS_CA_FILE", "") != "/run/core-force-mail/tls/publico/ca.crt" or \
        ":/run/core-force-mail/tls/publico:ro" not in perfil_txt:
    fallos.append("REDIS_TLS_CA_FILE no esta dentro del montaje de la CA publica")

# --- proxy de borde ---------------------------------------------------------------------------
borde = "\n".join(perfil.get("edge-proxy", []))
if not re.search(r"image:\s*\S+@sha256:[0-9a-f]{64}", borde):
    fallos.append("edge-proxy: la imagen no va fijada por digest")


def bytes_nginx(v):
    m = re.fullmatch(r"([1-9][0-9]*)([kKmM]?)", v)
    if not m:
        return None
    return int(m.group(1)) * {"": 1, "k": 1024, "m": 1024 ** 2}[m.group(2).lower()]


m = re.search(r"EDGE_MAX_BODY_SIZE:\s*\$\{EDGE_MAX_BODY_SIZE:-([^}]+)\}", borde)
defecto_compose = m.group(1) if m else ""
m = re.search(r'EDGE_MAX_BODY_SIZE="\$\{EDGE_MAX_BODY_SIZE:-([^}]+)\}"', leer("selfhosted/edge/entrypoint.d/20-plantilla.sh"))
defecto_guion = m.group(1) if m else ""
if not defecto_compose or defecto_compose != defecto_guion:
    fallos.append(f"EDGE_MAX_BODY_SIZE: el valor por defecto difiere entre el perfil ({defecto_compose}) y 20-plantilla.sh ({defecto_guion})")
limite = bytes_nginx(defecto_compose) or 0


def go_const(rel, nombre):
    m = re.search(rf"^\s*{nombre}\s*=\s*([0-9]+)\s*<<\s*([0-9]+)", leer(rel), re.M)
    if m:
        return int(m.group(1)) << int(m.group(2))
    m = re.search(rf"^\s*{nombre}\s*=\s*([0-9]+)\s*$", leer(rel), re.M)
    return int(m.group(1)) if m else None


postfix = go_const("services/webmail/main.go", "postfixMessageSizeLimit")
overhead = go_const("services/webmail/internal/adapters/http/compose_handler.go", "multipartOverhead")
if postfix is None or overhead is None:
    fallos.append("no se leen postfixMessageSizeLimit o multipartOverhead del webmail")
elif limite < postfix + overhead:
    fallos.append(f"EDGE_MAX_BODY_SIZE={defecto_compose} ({limite}) no cubre el mayor envio del webmail ({postfix + overhead})")
presupuesto = go_const("services/contacts/internal/adapters/http/handler.go", "importRowBudget")
base_json = go_const("services/contacts/internal/adapters/http/handler.go", "defaultBodyLimit")
m = re.search(r"^CONTACTS_IMPORT_MAX_ROWS=([0-9]+)$", leer(".env.example"), re.M)
if presupuesto is None or base_json is None or not m:
    fallos.append("no se leen importRowBudget, defaultBodyLimit o CONTACTS_IMPORT_MAX_ROWS")
elif limite < int(m.group(1)) * presupuesto + base_json:
    fallos.append(f"EDGE_MAX_BODY_SIZE={defecto_compose} no cubre la importacion de CONTACTS_IMPORT_MAX_ROWS={m.group(1)}")

v4 = v6 = 0
for n, linea in enumerate(leer("selfhosted/edge/cloudflare-ips.txt").splitlines(), 1):
    linea = linea.split("#", 1)[0].strip()
    if not linea:
        continue
    try:
        red = ipaddress.ip_network(linea, strict=True)
    except ValueError:
        fallos.append(f"cloudflare-ips.txt:{n}: '{linea}' no es un CIDR valido")
        continue
    if red.is_private or red.prefixlen < 8:
        fallos.append(f"cloudflare-ips.txt:{n}: '{linea}' no puede ser un rango de Cloudflare")
    v4 += red.version == 4
    v6 += red.version == 6
if not v4 or not v6:
    fallos.append("cloudflare-ips.txt: faltan rangos IPv4 o IPv6")

for rel in ("selfhosted/redis/entrypoint.sh", "selfhosted/edge/entrypoint.d/10-cloudflare.sh",
            "selfhosted/edge/entrypoint.d/20-plantilla.sh", "selfhosted/edge/entrypoint.d/40-recarga-certificado.sh",
            "ops/security/internal-tls.sh", "ops/maintenance/perfil-despliegue.sh"):
    if not os.access(os.path.join(root, rel), os.X_OK):
        fallos.append(f"{rel} no es ejecutable (el contenedor o el servidor no lo lanzarian)")

# --- los dos despliegues usan el perfil -------------------------------------------------------
deploy = leer("scripts/deploy-ecr.sh")
m = re.search(r"FICHEROS_SERVIDOR=\(([^)]*)\)", deploy)
viajan = m.group(1).split() if m else []
for rel in ("docker-compose.selfhosted.yml", "selfhosted", "pgbouncer", "ops/maintenance", "ops/security"):
    if rel not in viajan:
        fallos.append(f"deploy-ecr.sh: {rel} no viaja al servidor (FICHEROS_SERVIDOR)")
if deploy.count('stage_head_files "${FICHEROS_SERVIDOR[@]}"') != 3:
    fallos.append("deploy-ecr.sh: algun camino no sincroniza FICHEROS_SERVIDOR")
for n, linea in enumerate(deploy.splitlines(), 1):
    if re.search(r"docker\s+compose\s+-f\s+docker-compose\.yml.*\b(up|pull)\b", linea) and "remote" in linea:
        fallos.append(f"deploy-ecr.sh:{n}: compose remoto sin los argumentos del perfil")
if deploy.count("leer_perfil") < 4 or deploy.count("aplicar_infra") < 4 or deploy.count("aplicar_borde") < 4:
    fallos.append("deploy-ecr.sh: algun camino no lee el perfil o no levanta su infraestructura")
release = leer(".github/workflows/release.yml")
for marca in ("perfil-despliegue.sh", 'perfil-despliegue.sh" --env .env --compose', "--no-deps edge-proxy"):
    if marca not in release:
        fallos.append(f"release.yml: falta '{marca}'")
if re.search(r"with-secrets\.sh docker compose -f docker-compose\.yml", release):
    fallos.append("release.yml: compose sin los argumentos del perfil")

if fallos:
    print("check-selfhosted-profile: FALLA", file=sys.stderr)
    for f in fallos:
        print(f"  FALLA: {f}", file=sys.stderr)
    sys.exit(1)
PY

# --- perfil-despliegue.sh, ejecutado ------------------------------------------------------------
P="$ROOT/ops/maintenance/perfil-despliegue.sh"
FALLOS=0
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }
perfil() { printf '%b' "$1" >"$TMP/env"; shift; bash "$P" --env "$TMP/env" "$@" >"$TMP/out" 2>&1; }

perfil 'ENVIRONMENT=production\n' --compose && [[ "$(cat "$TMP/out")" == "-f docker-compose.yml" ]] || mal "perfil: sin DEPLOY_PROFILE no es el de AWS"
perfil 'DEPLOY_PROFILE=aws\n' --infra && [[ -z "$(cat "$TMP/out")" ]] || mal "perfil aws: declara infraestructura propia"
if perfil 'DEPLOY_PROFILE=selfhost\n' --compose; then mal "perfil: acepta un DEPLOY_PROFILE mal escrito"; fi
if perfil "DEPLOY_PROFILE=selfhosted\nINTERNAL_TLS_DIR=$TMP/sin-tls\n" --compose; then mal "perfil selfhosted: despliega sin CA interna"; fi
grep -q 'internal-tls.sh' "$TMP/out" || mal "perfil selfhosted sin CA: el mensaje no dice como generarla"
mkdir -p "$TMP/tls/publico" && printf -- '-----BEGIN CERTIFICATE-----\n' >"$TMP/tls/publico/ca.crt"
perfil "DEPLOY_PROFILE=\"selfhosted\"\nINTERNAL_TLS_DIR=$TMP/tls\n" --compose &&
  [[ "$(cat "$TMP/out")" == "-f docker-compose.yml -f docker-compose.selfhosted.yml" ]] || mal "perfil selfhosted: argumentos de compose inesperados"
perfil "DEPLOY_PROFILE=selfhosted\nINTERNAL_TLS_DIR=$TMP/tls\n" --infra || mal "perfil selfhosted: --infra falla"
for s in $(cat "$TMP/out"); do
  grep -qE "^  $s:\s*$" "$ROOT/docker-compose.selfhosted.yml" "$ROOT/docker-compose.yml" || mal "perfil selfhosted: $s no es un servicio de compose"
done
[[ "$(awk '{print $NF}' "$TMP/out")" == edge-proxy ]] || mal "perfil selfhosted: el proxy de borde no va el ultimo"
# NATS no se construye ni lo recrea ningun despliegue: si no es infraestructura del perfil, nadie
# lo arranca y los servicios se quedan sin eventos.
for s in postgres-primary redis pgbouncer nats; do
  grep -qw "$s" "$TMP/out" || mal "perfil selfhosted: $s no es infraestructura del perfil"
done
perfil 'DEPLOY_PROFILE=selfhosted\n' --nombre && [[ "$(cat "$TMP/out")" == selfhosted ]] || mal "perfil: --nombre no necesita la CA y dice selfhosted"

if [[ $FALLOS -ne 0 ]]; then
  echo "check-selfhosted-profile: FALLA" >&2
  exit 1
fi
echo "  OK: el perfil autoalojado conserva los parametros base, cifra Postgres y Redis para todos sus clientes, cubre los limites del webmail y lo aplican los dos despliegues."
