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
#   - mail-auth sirve el certificado de la CA interna (montaje del directorio, MAIL_AUTH_TLS_*,
#     user: sin root del que internal-tls.sh saca el dueno de la clave, SAN = host de
#     MAIL_AUTH_URL) y el webmail lo verifica con esa CA (WEBMAIL_TLS_CA_FILE);
#   - el cuerpo maximo del proxy de borde cubre el mayor envio del webmail y la importacion de
#     contactos de .env.example;
#   - los rangos de Cloudflare son CIDR validos, y la imagen del borde va por digest;
#   - MinIO (minio, minio-volumen, minio-init) con una sola imagen, la propia que dice
#     scripts/imagen-minio.sh (version y hash de su Dockerfile) con pull_policy: never, tambien en el
#     compose de la prueba de los motores; su Dockerfile con las bases por digest y las fuentes por
#     commit; sin puertos, solo en la red interna, sin root (salvo el chown del volumen, sin red) y
#     sin consola;
#   - ops/maintenance/perfil-despliegue.sh decide bien, y los dos despliegues lo usan.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

python3 - "$ROOT" <<'PY'
import ipaddress
import json
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
    if not re.search(r"volumes:\s*\*internal-ca", cuerpo) and \
            "- ${INTERNAL_TLS_DIR:-/opt/core-force-mail/tls}/publico:/run/core-force-mail/tls/publico:ro" not in cuerpo:
        fallos.append(f"{svc}: el perfil no le monta la CA interna (*internal-ca)")
m = re.search(r"x-go-service-env: &go-service-env\n((?:  .*\n)+)", perfil_txt)
env_go = dict(re.findall(r"^  ([A-Z_]+):\s*\"?([^\"\n]*)\"?", m.group(1), re.M)) if m else {}
if env_go.get("REDIS_TLS") != "true":
    fallos.append("x-go-service-env: REDIS_TLS no es \"true\"")
if env_go.get("REDIS_TLS_SERVER_NAME") != "redis":
    fallos.append("x-go-service-env: REDIS_TLS_SERVER_NAME no es el nombre del servicio redis (SAN del certificado)")

# --- puntos de montaje compartidos con internal-tls.sh ----------------------------------------
tls = leer("ops/security/internal-tls.sh")
for var in ("MONTAJE_PUBLICO", "MONTAJE_REDIS", "MONTAJE_MAIL_AUTH"):
    m = re.search(rf"^{var}=(\S+)$", tls, re.M)
    if not m:
        fallos.append(f"internal-tls.sh: falta {var}")
        continue
    if f":{m.group(1)}:ro" not in perfil_txt:
        fallos.append(f"internal-tls.sh: {var}={m.group(1)} no es un punto de montaje del perfil")
if env_go.get("REDIS_TLS_CA_FILE", "") != "/run/core-force-mail/tls/publico/ca.crt" or \
        ":/run/core-force-mail/tls/publico:ro" not in perfil_txt:
    fallos.append("REDIS_TLS_CA_FILE no esta dentro del montaje de la CA publica")

# --- mail-auth con certificado de la CA interna y el webmail verificandolo --------------------


def entorno(lineas):
    return dict(re.findall(r"^      ([A-Z_]+):\s*\"?([^\"\n]*)\"?\s*$", "\n".join(lineas), re.M))


m = re.search(r"^MONTAJE_MAIL_AUTH=(\S+)$", tls, re.M)
montaje_ma = m.group(1) if m else ""
ma = perfil.get("mail-auth", [])
ma_txt = "\n".join(ma)
env_ma = entorno(ma)
if env_ma.get("MAIL_AUTH_TLS_CERT") != f"{montaje_ma}/server.crt" or env_ma.get("MAIL_AUTH_TLS_KEY") != f"{montaje_ma}/server.key":
    fallos.append(f"mail-auth: MAIL_AUTH_TLS_CERT/MAIL_AUTH_TLS_KEY no son server.crt/server.key de {montaje_ma or 'MONTAJE_MAIL_AUTH'} "
                  "(sin ellos sirve un autofirmado y el webmail no verifica)")
if f"- ${{INTERNAL_TLS_DIR:-/opt/core-force-mail/tls}}/mail-auth:{montaje_ma}:ro" not in ma_txt:
    fallos.append("mail-auth: el perfil no monta el DIRECTORIO <INTERNAL_TLS_DIR>/mail-auth (un fichero montado no ve la renovacion)")
m = re.search(r"^    user:\s*\"?([0-9]+):([0-9]+)\"?\s*$", ma_txt, re.M)
if not m or m.group(1) == "0" or m.group(2) == "0":
    fallos.append("mail-auth: el perfil no le fija un user: UID:GID numerico sin root (dueno de su clave en internal-tls.sh)")
if not re.search(r"^SERVICIOS=\([^)]*\bmail-auth\b[^)]*\)$", tls, re.M):
    fallos.append("internal-tls.sh: no emite el certificado de mail-auth (SERVICIOS)")
m = re.search(r"\[mail-auth\]=(\S+?)[\s)]", tls)
nombre_ma = m.group(1) if m else ""
hosts = set()
for rel, patron in ((".env.example", r"^MAIL_AUTH_URL=(\S+)$"),
                    ("deploy/mail/docker-compose.mail.yml", r"MAIL_AUTH_URL=\$\{MAIL_AUTH_URL:-([^}]+)\}")):
    m = re.search(patron, leer(rel), re.M)
    if not m:
        fallos.append(f"{rel}: no se lee MAIL_AUTH_URL")
        continue
    hosts.add(re.sub(r"^https://([^:/]+).*$", r"\1", m.group(1)))
m = re.search(r'^\s*tlsHostname\s*=\s*"([^"]+)"', leer("services/mail-auth/main.go"), re.M)
if m:
    hosts.add(m.group(1))
else:
    fallos.append("services/mail-auth/main.go: no se lee tlsHostname")
if hosts != {nombre_ma}:
    fallos.append(f"internal-tls.sh: el certificado de mail-auth es para '{nombre_ma}' y sus clientes lo buscan como {sorted(hosts)}")
if not re.search(r"aliases:\s*- " + re.escape(nombre_ma or "?") + r"(\s|$)", " ".join(base.get("mail-auth", []))):
    fallos.append(f"docker-compose.yml: mail-auth no tiene el alias '{nombre_ma}' en la red de los motores")
if not re.search(r'echo "DNS:\$svc,', tls):
    fallos.append("internal-tls.sh: san_de no pone el nombre del servicio en el SAN")
m = re.search(r"^SERVICIOS=\(([^)]*)\)$", tls, re.M)
servicios_tls = m.group(1).split() if m else []
m = re.search(r"^  for dir in ([a-z -]+); do$", leer("ops/maintenance/perfil-despliegue.sh"), re.M)
if not m or m.group(1).split() != servicios_tls:
    fallos.append(f"perfil-despliegue.sh: no exige los directorios de certificado de internal-tls.sh ({' '.join(servicios_tls)})")
wm = perfil.get("webmail", [])
env_wm = entorno(wm)
if env_wm.get("WEBMAIL_TLS_CA_FILE") != "/run/core-force-mail/tls/publico/ca.crt" or \
        not re.search(r"volumes:\s*\*internal-ca", "\n".join(wm)):
    fallos.append("webmail: WEBMAIL_TLS_CA_FILE no es la CA interna montada (no verificaria mail-auth)")
if env_wm.get("WEBMAIL_TLS_INSECURE_SKIP_VERIFY", "false") != "false":
    fallos.append("webmail: el perfil desactiva la verificacion TLS")

# --- proxy de borde ---------------------------------------------------------------------------
borde = "\n".join(perfil.get("edge-proxy", []))
if not re.search(r"image:\s*\S+@sha256:[0-9a-f]{64}", borde):
    fallos.append("edge-proxy: la imagen no va fijada por digest")


# --- MinIO: la imagen propia, solo en la red interna, sin puerto, sin root salvo el chown del volumen
# La etiqueta la calcula scripts/imagen-minio.sh a partir de su Dockerfile: un compose que referencia
# otra corre una receta que ya no es la del repositorio, o una imagen que el despliegue no sabe enviar.
try:
    imagen_minio = subprocess.run([os.path.join(root, "scripts/imagen-minio.sh"), "--referencia"],
                                  capture_output=True, text=True, check=True).stdout.strip()
except (OSError, subprocess.CalledProcessError) as e:
    imagen_minio = ""
    fallos.append(f"scripts/imagen-minio.sh --referencia fallo: {getattr(e, 'stderr', '') or e}")
receta_minio = leer("selfhosted/minio/imagen/Dockerfile")
for base in re.findall(r"^FROM\s+(\S+)", receta_minio, re.M):
    if not re.search(r"@sha256:[0-9a-f]{64}$", base):
        fallos.append(f"selfhosted/minio/imagen/Dockerfile: la base {base} no va fijada por digest")
for arg in ("SILO_COMMIT", "MC_COMMIT"):
    if not re.search(rf"^ARG {arg}=[0-9a-f]{{40}}$", receta_minio, re.M):
        fallos.append(f"selfhosted/minio/imagen/Dockerfile: {arg} no fija un commit completo")
if not re.search(r"^USER 10001:10001$", receta_minio, re.M):
    fallos.append("selfhosted/minio/imagen/Dockerfile: la imagen no corre con el uid 10001 del perfil")


def comprobar_imagen_minio(fichero, svc, cuerpo):
    m = re.search(r"^    image:\s*(\S+)$", cuerpo, re.M)
    if not m or (imagen_minio and m.group(1) != imagen_minio):
        fallos.append(f"{fichero} {svc}: la imagen tiene que ser {imagen_minio or 'la de scripts/imagen-minio.sh'}"
                      f" (tiene {m.group(1) if m else 'ninguna'}); tras cambiar el Dockerfile, pon la nueva referencia")
    if not re.search(r"^    pull_policy: never$", cuerpo, re.M):
        fallos.append(f"{fichero} {svc}: falta pull_policy: never (la imagen propia no esta en ningun registro)")


e2e_motores = bloques(leer("deploy/mail/docker-compose.e2e.yml"))
for svc in ("minio", "minio-init"):
    comprobar_imagen_minio("deploy/mail/docker-compose.e2e.yml", svc, "\n".join(e2e_motores.get(svc, [])))

for svc in ("minio", "minio-volumen", "minio-init"):
    cuerpo = "\n".join(perfil.get(svc, []))
    if not cuerpo:
        fallos.append(f"{svc}: falta en el perfil")
        continue
    comprobar_imagen_minio("docker-compose.selfhosted.yml", svc, cuerpo)
    if re.search(r"^    ports:", cuerpo, re.M):
        fallos.append(f"{svc}: publica un puerto; MinIO no sale de la red interna")
    for requerido in ("read_only: true", "cap_drop: [ALL]", 'security_opt: ["no-new-privileges:true"]', "mem_limit:"):
        if requerido not in cuerpo:
            fallos.append(f"{svc}: falta '{requerido}'")
    m = re.search(r'^    user:\s*"?([0-9]+):([0-9]+)"?\s*$', cuerpo, re.M)
    if svc == "minio-volumen":
        if "network_mode: none" not in cuerpo or not re.search(r"^    cap_add: \[CHOWN\]$", cuerpo, re.M):
            fallos.append("minio-volumen: corre como root solo para el chown: sin red y con CHOWN como unica capacidad")
    elif not m or m.group(1) == "0" or m.group(2) == "0":
        fallos.append(f"{svc}: no fija un user: UID:GID numerico sin root")
    elif not re.search(r"^    networks:\n      - mail-internal$", cuerpo, re.M) or re.search(r"^      - (edge|mail-engines)$", cuerpo, re.M):
        fallos.append(f"{svc}: tiene que estar solo en la red mail-internal")
if "MINIO_BROWSER: \"off\"" not in "\n".join(perfil.get("minio", [])):
    fallos.append("minio: la consola tiene que ir apagada (MINIO_BROWSER: \"off\")")

# La politica del usuario de servicio (selfhosted/minio/init.sh) solo deja borrar los ficheros de
# mail-files, que se borran al revocar o caducar su enlace; nunca public/, que muestran correos ya enviados.
politica_minio = re.search(r"cat >/tmp/politica.json <<EOF\n(.*?)\nEOF", leer("selfhosted/minio/init.sh"), re.S)
if not politica_minio:
    fallos.append("selfhosted/minio/init.sh: no se encuentra la politica del usuario de servicio")
else:
    try:
        sentencias = json.loads(politica_minio.group(1).replace("$BUCKET", "bucket"))["Statement"]
    except (ValueError, KeyError) as e:
        sentencias = []
        fallos.append(f"selfhosted/minio/init.sh: la politica no es JSON valido ({e})")
    borrado = [r for st in sentencias if st.get("Effect") == "Allow"
               for a in st.get("Action", []) if a in ("s3:DeleteObject", "s3:*", "*")
               for r in st.get("Resource", [])]
    if borrado != ["arn:aws:s3:::bucket/private/*/mail-files/*"]:
        fallos.append(f"selfhosted/minio/init.sh: el usuario de servicio tiene que poder borrar solo private/*/mail-files/* (tiene {borrado})")


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
            "selfhosted/minio/entrypoint.sh", "selfhosted/minio/init.sh",
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
m = re.search(r"^aplicar_infra\(\) \{\n(.*?)^\}", deploy, re.S | re.M)
if not m or not re.search(r"^  enviar_imagen_minio$", m.group(1), re.M) or \
        m.group(1).find("enviar_imagen_minio") > m.group(1).find("docker compose"):
    fallos.append("deploy-ecr.sh: aplicar_infra no envia la imagen propia de MinIO antes de levantar la infraestructura")
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
mkdir -p "$TMP/tls/publico" "$TMP/tls/postgres" "$TMP/tls/redis" && printf -- '-----BEGIN CERTIFICATE-----\n' >"$TMP/tls/publico/ca.crt"
if perfil "DEPLOY_PROFILE=selfhosted\nINTERNAL_TLS_DIR=$TMP/tls\n" --compose; then mal "perfil selfhosted: despliega sin el certificado de mail-auth"; fi
grep -q 'mail-auth' "$TMP/out" || mal "perfil selfhosted sin certificado de mail-auth: el mensaje no lo nombra"
mkdir -p "$TMP/tls/mail-auth"
perfil "DEPLOY_PROFILE=\"selfhosted\"\nINTERNAL_TLS_DIR=$TMP/tls\n" --compose &&
  [[ "$(cat "$TMP/out")" == "-f docker-compose.yml -f docker-compose.selfhosted.yml" ]] || mal "perfil selfhosted: argumentos de compose inesperados"
perfil "DEPLOY_PROFILE=selfhosted\nINTERNAL_TLS_DIR=$TMP/tls\n" --infra || mal "perfil selfhosted: --infra falla"
for s in $(cat "$TMP/out"); do
  grep -qE "^  $s:\s*$" "$ROOT/docker-compose.selfhosted.yml" "$ROOT/docker-compose.yml" || mal "perfil selfhosted: $s no es un servicio de compose"
done
[[ "$(awk '{print $NF}' "$TMP/out")" == edge-proxy ]] || mal "perfil selfhosted: el proxy de borde no va el ultimo"
# NATS no se construye ni lo recrea ningun despliegue: si no es infraestructura del perfil, nadie
# lo arranca y los servicios se quedan sin eventos.
for s in postgres-primary redis pgbouncer nats minio minio-init smtp-relay-certs; do
  grep -qw "$s" "$TMP/out" || mal "perfil selfhosted: $s no es infraestructura del perfil"
done
perfil 'DEPLOY_PROFILE=selfhosted\n' --nombre && [[ "$(cat "$TMP/out")" == selfhosted ]] || mal "perfil: --nombre no necesita la CA y dice selfhosted"

# Sin almacen, with-secrets.sh exporta credenciales pero no configuracion: el userlist de PgBouncer
# debe sacar del .env la celda del despliegue o sus servicios no pasan el pooler.
mkdir -p "$TMP/app" && printf 'CELL_DB_NAME=mail_cell_zz_01\n' >"$TMP/app/.env"
roles="$(env -i PATH="$PATH" APP_DIR="$TMP/app" POSTGRES_PASSWORD=x CELL_DB_PASSWORD=x MAIL_DB_PASSWORD=x \
  bash "$ROOT/ops/db/pgbouncer-userlist.sh" --print 2>/dev/null)"
for r in mail_cell_zz_01_svc mail_cell_zz_01_engine; do
  grep -qx "\"$r\"" <<<"$roles" || mal "pgbouncer-userlist.sh: sin CELL_DB_NAME en el entorno omite $r del .env"
done

# --- internal-tls.sh lee el dueno de mail-auth del perfil, sin docker ---------------------------
esperado="$(sed -n -E '/^  mail-auth:/,/^  [^ ]/s/^    user:[[:space:]]*"?([0-9]+:[0-9]+)"?[[:space:]]*$/\1/p' "$ROOT/docker-compose.selfhosted.yml")"
if duenos="$(bash "$ROOT/ops/security/internal-tls.sh" --solo-detectar --duenos postgres=1:1,redis=1:1 2>&1)"; then
  grep -qx "mail-auth=$esperado" <<<"$duenos" && [[ -n "$esperado" ]] || mal "internal-tls.sh: dueno de mail-auth '$(grep mail-auth <<<"$duenos")', el perfil dice '$esperado'"
else
  mal "internal-tls.sh --solo-detectar no lee el dueno de mail-auth del perfil: $duenos"
fi

# Tras un reinicio del servidor, el demonio de docker corre con live-restore y solo levanta los
# contenedores con la politica "always": con "unless-stopped" la plataforma no vuelve sola (paso en
# el servidor real: postgres-primary se quedo parado y todo lo demas reintentando).
python3 - "$ROOT/docker-compose.yml" "$ROOT/docker-compose.selfhosted.yml" <<'PY' || FALLOS=1
import re, sys

def servicios(ruta):
    texto = open(ruta, encoding="utf-8").read()
    cuerpo = texto.split("\nservices:\n", 1)[1]
    for corte in ("\nnetworks:\n", "\nvolumes:\n"):
        if corte in cuerpo:
            cuerpo = cuerpo.split(corte, 1)[0]
    bloques = re.split(r"\n(?=  [a-z][a-z0-9-]*:\n)", "\n" + cuerpo)
    salida = {}
    for b in bloques:
        m = re.match(r"\n?  ([a-z][a-z0-9-]*):\n", b)
        if m:
            salida[m.group(1)] = b
    return salida

base = servicios(sys.argv[1])
perfil = servicios(sys.argv[2])
sin_politica = []
for nombre in base:
    b = perfil.get(nombre, "")
    if not re.search(r"^    restart: \*reinicio$", b, re.M):
        sin_politica.append(nombre)
if sin_politica:
    print("  FALLA: servicios sin la politica de reinicio del perfil (no vuelven tras un reinicio del"
          " servidor): " + " ".join(sorted(sin_politica)), file=sys.stderr)
    print("    Anade `restart: *reinicio` en docker-compose.selfhosted.yml.", file=sys.stderr)
    sys.exit(1)
PY

# Dovecot mantiene conexiones largas a la base por PgBouncer: con el corte de clientes ociosos
# (600 s por defecto) el primer inicio de sesion IMAP tras un rato sin uso fallaba con
# "client_idle_timeout". El perfil lo desactiva; sin esto el fallo vuelve sin ningun aviso.
python3 - "$ROOT/docker-compose.selfhosted.yml" <<'PY' || FALLOS=1
import re, sys
texto = open(sys.argv[1], encoding="utf-8").read()
bloque = re.search(r"\n  pgbouncer:\n(.*?)(?=\n  [a-z][a-z0-9-]*:\n)", texto, re.S)
if not bloque or not re.search(r'^\s+PGBOUNCER_CLIENT_IDLE_TIMEOUT:\s*"?0"?\s*$', bloque.group(1), re.M):
    print("  FALLA: pgbouncer del perfil sin PGBOUNCER_CLIENT_IDLE_TIMEOUT: \"0\" (Dovecot pierde su conexion"
          " a la base tras 600 s ociosa y falla el primer inicio de sesion)", file=sys.stderr)
    sys.exit(1)
PY

# La verificacion de DNS de las empresas no puede depender del resolver del proveedor: cachea la
# respuesta negativa y un dominio recien publicado se quedaba en "fallido" durante horas.
python3 - "$ROOT/docker-compose.selfhosted.yml" <<'PY' || FALLOS=1
import re, sys
texto = open(sys.argv[1], encoding="utf-8").read()
bloque = re.search(r"\n  domain-service:\n(.*?)(?=\n  [a-z][a-z0-9-]*:\n)", texto, re.S)
if not bloque or not re.search(r"^\s+MAIL_DNS_RESOLVER:\s*\$\{MAIL_DNS_RESOLVER:-[0-9.]+:53\}\s*$", bloque.group(1), re.M):
    print("  FALLA: domain-service del perfil sin un resolver publico por defecto (MAIL_DNS_RESOLVER): un dominio"
          " recien publicado se verificaria como fallido por la cache negativa del DNS del proveedor", file=sys.stderr)
    sys.exit(1)
PY

# Recursos de cada servicio Go del perfil: sin root, solo lectura, sin capacidades, tope de procesos
# y de memoria (mem_limit con su GOMEMLIMIT por debajo). Sin limite, un servicio con una fuga o una
# peticion enorme se lleva la RAM de un servidor donde comparte host con la base y los motores, y
# el OOM del kernel elige a la victima (a menudo Postgres). Ademas, la suma de los limites de la
# plataforma no pasa del techo declarado: subirlo es una decision de presupuesto
# (docs/Operacion_Despliegue.md, "Presupuesto de recursos"), no un efecto secundario.
python3 - "$ROOT" <<'PY' || FALLOS=1
import os, re, subprocess, sys
try:
    import yaml
except ImportError:
    print("  AVISO: sin PyYAML; no se comprueban los limites de recursos del perfil", file=sys.stderr)
    sys.exit(0)

CEILING_MIB = 6400

root = sys.argv[1]


class Cargador(yaml.SafeLoader):
    pass


# Las etiquetas propias de compose (!reset, !override) no cambian lo que se lee aqui.
for etiqueta in ("!reset", "!override"):
    Cargador.add_constructor(etiqueta, lambda cargador, nodo: None)
perfil = yaml.load(open(os.path.join(root, "docker-compose.selfhosted.yml"), encoding="utf-8"), Loader=Cargador)["services"]
mapa = subprocess.run(["bash", os.path.join(root, "ops/scaffold/service-paths.sh"), "--map"],
                      capture_output=True, text=True, check=True).stdout
go = [l.split("\t")[0] for l in mapa.splitlines() if l.endswith("\tgo")]


def mib(valor):
    valor = re.sub(r"^\$\{[A-Z_]+:-([^}]*)\}$", r"\1", str(valor).strip())
    m = re.fullmatch(r"(\d+)([kmg]?)i?b?", str(valor).strip().lower())
    if not m:
        return None
    return int(m.group(1)) * {"": 1 / 1048576, "k": 1 / 1024, "m": 1, "g": 1024}[m.group(2)]


fallos = []
for svc in go:
    s = perfil.get(svc, {})
    limite = mib(s.get("mem_limit"))
    if limite is None:
        fallos.append(f"{svc}: sin mem_limit en el perfil")
    gomem = mib((s.get("environment") or {}).get("GOMEMLIMIT"))
    if gomem is None or limite is None or not (0.5 * limite <= gomem < limite):
        fallos.append(f"{svc}: GOMEMLIMIT debe existir y estar entre el 50 % y el 100 % de mem_limit")
    if str(s.get("user", "")) in ("", "0", "0:0", "root"):
        fallos.append(f"{svc}: corre como root (user:)")
    if s.get("read_only") is not True:
        fallos.append(f"{svc}: sin read_only")
    if "ALL" not in (s.get("cap_drop") or []):
        fallos.append(f"{svc}: sin cap_drop: [ALL]")
    if not s.get("pids_limit"):
        fallos.append(f"{svc}: sin pids_limit")
    espera = re.fullmatch(r"(\d+)s", str(s.get("stop_grace_period", "")))
    if not espera or int(espera.group(1)) <= 30:
        fallos.append(f"{svc}: stop_grace_period debe superar los 30 s con los que pkg/server espera a las peticiones en vuelo")
total = sum(mib(s.get("mem_limit")) or 0 for s in perfil.values())
if total > CEILING_MIB:
    fallos.append(f"la suma de mem_limit del perfil es {total:.0f} MiB y el techo declarado es {CEILING_MIB} MiB")
for f in fallos:
    print("  FALLA: " + f, file=sys.stderr)
if fallos:
    sys.exit(1)
print(f"  OK: {len(go)} servicios Go con limites de recursos; suma de mem_limit del perfil {total:.0f} MiB (techo {CEILING_MIB})")
PY

if [[ $FALLOS -ne 0 ]]; then
  echo "check-selfhosted-profile: FALLA" >&2
  exit 1
fi
echo "  OK: el perfil autoalojado conserva los parametros base, cifra Postgres y Redis para todos sus clientes, da a mail-auth un certificado que el webmail verifica, cubre los limites del webmail y lo aplican los dos despliegues."
