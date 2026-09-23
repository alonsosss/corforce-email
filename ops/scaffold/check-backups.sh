#!/usr/bin/env bash
# Guardarrail de los respaldos y del acceso a la base desde ops/ (ops/backup, ops/db,
# ops/maintenance y ops/apply-all-canonical.sh), sin docker.
#
# El respaldo no se ve funcionar: un cambio que lo rompe pasa desapercibido hasta el dia que hace
# falta. Aqui se atan las decisiones que lo sostienen en los dos perfiles:
#   - las herramientas de Postgres, ejecutadas con un docker simulado: en el perfil autoalojado van
#     en contenedor de la imagen del Postgres en marcha (por su identificador), en su red, con
#     verify-full y la CA interna montada, la contrasena solo por nombre (-e PGPASSWORD) y sin
#     capacidades; en el perfil aws, los binarios del host sin tocar PGSSLMODE;
#   - ningun guion de ops/ que abra la base (respaldo, migraciones, roles, arranque) llama a psql,
#     pg_dump o pg_restore sin pasar por esas funciones, y todos conservan el respaldo al binario
#     del host para cuando se ejecutan sin pg-credentials.sh (pruebas, make e2e);
#   - ningun secreto viaja como argumento de psql: los que el SQL necesita se pasan por el entorno
#     con cf_pg_pasar_entorno y se leen con \getenv;
#   - el respaldo lista las tres clases de base y la verificacion completa exige registro y celda;
#   - la copia externa: CLI fijado por digest, credenciales y frase nunca en argumentos, sus
#     secretos en secret-keys-backup.txt (fuera de secret-keys.txt y de .env.example) y vigilados
#     por check-secrets.sh;
#   - unidades de systemd: plantilla coherente con install-timers.sh, UMask 0077, todos los
#     temporizadores activados, y bootstrap.sh instalando por ese guion;
#   - el respaldo de los volumenes de correo: imagen de tar fijada e igual en respaldo y
#     restauracion, buzones y claves de mail_crypt por defecto y crypt-vol nunca sin cifrar fuera;
#   - todo trabajo que publique metrica tiene alerta en plataforma.yml;
#   - el nombre del servicio de Postgres es el de compose y el del certificado interno;
#   - el despliegue sincroniza ops/backup, ops/db y ops/maintenance.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FALLOS=0
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

# --- herramientas de Postgres con docker simulado --------------------------------------------------
mkdir -p "$TMP/arbol/ops/security/secrets" "$TMP/bin" "$TMP/tls/publico"
cp -r "$ROOT/ops/db" "$ROOT/ops/maintenance" "$TMP/arbol/ops/"
: >"$TMP/arbol/ops/security/secrets/load.sh"
: >"$TMP/tls/publico/ca.crt"
cat >"$TMP/bin/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  ps) echo c0ffee ;;
  inspect)
    case "$*" in
      *'{{.Image}}'*) echo sha256:imagen-del-servidor ;;
      *Networks*) echo proyecto_mail-internal ;;
    esac ;;
  run)
    printf '%s\n' "$@" >"$DOCKER_LOG"
    # La entrada estandar del contenedor, para distinguir la familia "con entrada" de la otra.
    cat >"$DOCKER_LOG.entrada"
    ;;
esac
EOF
for binario in psql pg_dump pg_restore; do
  printf '#!/usr/bin/env bash\necho "host:%s $*" >"$HOST_LOG"\ncat >/dev/null\n' "$binario" >"$TMP/bin/$binario"
done
chmod +x "$TMP/bin/"*

printf 'DEPLOY_PROFILE=selfhosted\nPOSTGRES_USER=mail_admin\nINTERNAL_TLS_DIR=%s\nCOMPOSE_PROJECT_NAME=proyecto\n' "$TMP/tls" >"$TMP/arbol/.env"
salida="$(env -i PATH="$TMP/bin:/usr/bin:/bin" APP_DIR="$TMP/arbol" POSTGRES_PASSWORD=clave-simulada DOCKER_LOG="$TMP/docker.log" \
  HOST_LOG="$TMP/host.log" bash -c '. "$APP_DIR/ops/db/pg-credentials.sh" || exit 1
  mkdir -p "$APP_DIR/volcados" && cf_pg_montar "$APP_DIR/volcados" || exit 1
  CF_SECRETO_SIMULADO=verificador-simulado cf_pg_pasar_entorno CF_SECRETO_SIMULADO
  cf_pg_dump -d mail_registry -Fc -f "$APP_DIR/volcados/x.dump"
  echo "perfil=$CF_PERFIL_DESPLIEGUE host=$PGHOST modo=$PGSSLMODE ca=$PGSSLROOTCERT"' 2>&1)" || mal "pg-credentials.sh en el perfil autoalojado: $salida"
args="$(cat "$TMP/docker.log" 2>/dev/null || true)"
tiene() { grep -qxF -- "$1" <<<"$args"; }
[[ "$salida" == *"perfil=selfhosted host=postgres-primary modo=verify-full ca=/run/core-force-mail/tls/publico/ca.crt"* ]] ||
  mal "autoalojado: se esperaba postgres-primary con verify-full y la CA montada ($salida)"
for esperado in run --rm --network proyecto_mail-internal --read-only --cap-drop ALL no-new-privileges \
  PGPASSWORD PGSSLMODE PGSSLROOTCERT "$TMP/tls/publico:/run/core-force-mail/tls/publico:ro" \
  "$TMP/arbol/volcados:$TMP/arbol/volcados:rw" pg_dump sha256:imagen-del-servidor; do
  tiene "$esperado" || mal "autoalojado: docker run sin '$esperado'"
done
tiene "CF_SECRETO_SIMULADO" || mal "autoalojado: cf_pg_pasar_entorno no pasa la variable al contenedor con -e"
grep -qE '^(PG[A-Z]*|POSTGRES_PASSWORD|CF_SECRETO_SIMULADO)=' <<<"$args" && mal "autoalojado: una variable de Postgres va con su valor en los argumentos de docker run"
grep -qF 'verificador-simulado' <<<"$args" && mal "autoalojado: el valor de un secreto del entorno aparece en los argumentos de docker run"
grep -qF 'clave-simulada' <<<"$args" && mal "autoalojado: la contrasena aparece en los argumentos de docker run"
[[ -f "$TMP/host.log" ]] && mal "autoalojado: se ejecuto un binario de Postgres del host ($(cat "$TMP/host.log"))"

# La entrada estandar es de quien la pide: cf_psql no puede comerse la lista de un bucle
# `while read` (lo hizo en produccion y tenant-service-role.sh --all creo un solo rol), y
# cf_psql_entrada tiene que recibir su heredoc.
vueltas="$(env -i PATH="$TMP/bin:/usr/bin:/bin" APP_DIR="$TMP/arbol" POSTGRES_PASSWORD=clave-simulada \
  DOCKER_LOG="$TMP/docker.log" HOST_LOG="$TMP/host.log" bash -c '. "$APP_DIR/ops/db/pg-credentials.sh" || exit 1
  n=0
  while IFS= read -r linea; do
    cf_psql -d mail_registry -At -c "select 1" >/dev/null
    n=$((n + 1))
  done < <(printf "uno\ndos\ntres\n")
  echo "$n"' 2>&1)" || mal "pg-credentials.sh en el bucle de prueba: $vueltas"
[[ "$vueltas" == 3 ]] || mal "autoalojado: cf_psql se come la entrada del bucle (vueltas=$vueltas, se esperaban 3)"
grep -qxF -- '-i' "$TMP/docker.log" && mal "autoalojado: cf_psql reserva la entrada estandar con -i"
rm -f "$TMP/docker.log" "$TMP/docker.log.entrada"
env -i PATH="$TMP/bin:/usr/bin:/bin" APP_DIR="$TMP/arbol" POSTGRES_PASSWORD=clave-simulada \
  DOCKER_LOG="$TMP/docker.log" HOST_LOG="$TMP/host.log" bash -c '. "$APP_DIR/ops/db/pg-credentials.sh" || exit 1
  cf_psql_entrada -d mail_registry <<SQL
SELECT marca-del-heredoc;
SQL' >/dev/null 2>&1 || mal "cf_psql_entrada fallo en el docker simulado"
grep -qxF -- '-i' "$TMP/docker.log" || mal "autoalojado: cf_psql_entrada no pasa la entrada estandar al contenedor (-i)"
grep -q 'marca-del-heredoc' "$TMP/docker.log.entrada" 2>/dev/null ||
  mal "autoalojado: el heredoc de cf_psql_entrada no llega al contenedor"

printf 'DEPLOY_PROFILE=aws\nPOSTGRES_HOST=db.interna\nPOSTGRES_USER=mail_admin\n' >"$TMP/arbol/.env"
rm -f "$TMP/docker.log" "$TMP/docker.log.entrada"
salida="$(env -i PATH="$TMP/bin:/usr/bin:/bin" APP_DIR="$TMP/arbol" POSTGRES_PASSWORD=clave-simulada DOCKER_LOG="$TMP/docker.log" \
  HOST_LOG="$TMP/host.log" bash -c '. "$APP_DIR/ops/db/pg-credentials.sh" || exit 1
  cf_pg_montar /tmp && cf_psql -d mail_registry -c "select 1"
  echo "perfil=$CF_PERFIL_DESPLIEGUE host=$PGHOST modo=${PGSSLMODE:-sin}"' 2>&1)" || mal "pg-credentials.sh en el perfil aws: $salida"
[[ "$salida" == *"perfil=aws host=db.interna modo=sin"* ]] || mal "aws: se esperaba el host del .env sin tocar PGSSLMODE ($salida)"
[[ "$(cat "$TMP/host.log" 2>/dev/null)" == "host:psql -d mail_registry -c select 1" ]] || mal "aws: cf_psql no ejecuta el psql del host"
[[ -f "$TMP/docker.log" ]] && mal "aws: se lanzo un contenedor"
vueltas="$(env -i PATH="$TMP/bin:/usr/bin:/bin" APP_DIR="$TMP/arbol" POSTGRES_PASSWORD=clave-simulada \
  DOCKER_LOG="$TMP/docker.log" HOST_LOG="$TMP/host.log" bash -c '. "$APP_DIR/ops/db/pg-credentials.sh" || exit 1
  n=0
  while IFS= read -r linea; do cf_psql -d mail_registry -At -c "select 1" >/dev/null; n=$((n + 1)); done < <(printf "uno\ndos\ntres\n")
  echo "$n"' 2>&1)" || mal "pg-credentials.sh en el bucle de prueba (aws): $vueltas"
[[ "$vueltas" == 3 ]] || mal "aws: cf_psql se come la entrada del bucle (vueltas=$vueltas, se esperaban 3)"

# Sin .env (un puesto de trabajo o una prueba con PG* propias): el camino de siempre.
rm -f "$TMP/arbol/.env" "$TMP/host.log"
salida="$(env -i PATH="$TMP/bin:/usr/bin:/bin" APP_DIR="$TMP/arbol" POSTGRES_PASSWORD=clave-simulada POSTGRES_DIRECT_HOST=127.0.0.1 \
  POSTGRES_USER=mail_admin DOCKER_LOG="$TMP/docker.log" HOST_LOG="$TMP/host.log" bash -c '. "$APP_DIR/ops/db/pg-credentials.sh" || exit 1
  cf_pg_dump -d mail_registry; echo "perfil=$CF_PERFIL_DESPLIEGUE host=$PGHOST modo=${PGSSLMODE:-sin}"' 2>&1)" || mal "pg-credentials.sh sin .env: $salida"
[[ "$salida" == *"perfil=aws host=127.0.0.1 modo=sin"* && "$(cat "$TMP/host.log" 2>/dev/null)" == "host:pg_dump -d mail_registry" ]] ||
  mal "sin .env: se esperaba el pg_dump del host contra POSTGRES_DIRECT_HOST ($salida)"
[[ -f "$TMP/docker.log" ]] && mal "sin .env: se lanzo un contenedor"

# --- comprobaciones estaticas ---------------------------------------------------------------------
python3 - "$ROOT" <<'PY' || FALLOS=1
import os
import re
import sys

root = sys.argv[1]
fallos = []


def leer(rel):
    with open(os.path.join(root, rel), encoding="utf-8") as fh:
        return fh.read()


def codigo(texto):
    return [(n, l) for n, l in enumerate(texto.splitlines(), 1) if not l.lstrip().startswith("#")]


backup = "ops/backup"
guiones = sorted(f for f in os.listdir(os.path.join(root, backup)) if f.endswith(".sh"))
llamada = re.compile(r"(^|\$\(|[|;&!{(]|\bif\b|\bthen\b|\bdo\b|\|\|)\s*(psql|pg_dump|pg_restore)\b")

# Guiones de ops/ que abren la base (fuera de ops/backup): el respaldo, las migraciones, los roles
# y el arranque de la plataforma van todos por cf_psql. En el perfil autoalojado el psql del host no
# resuelve postgres-primary, y el fallo aparece lejos: "could not translate host name".
def guiones_de(rel_dir):
    d = os.path.join(root, rel_dir)
    return sorted(f"{rel_dir}/{f}" for f in os.listdir(d) if f.endswith(".sh")) if os.path.isdir(d) else []


def sentencias(texto):
    """Lineas logicas: une las continuaciones con barra antes de mirar.

    Un docker exec partido en dos lineas, con el psql en la segunda, es una sola orden; leidas
    por separado la segunda parece una llamada al psql del host."""
    logica, primera, salida = "", 0, []
    for n, linea in enumerate(texto.splitlines(), 1):
        if not logica:
            primera = n
        sin = "" if linea.lstrip().startswith("#") else linea
        if sin.rstrip().endswith("\\"):
            logica += sin.rstrip()[:-1] + " "
            continue
        salida.append((primera, logica + sin))
        logica = ""
    if logica:
        salida.append((primera, logica))
    return salida


abren_base = [f"ops/{g}" for g in ("apply-all-canonical.sh",) if os.path.isfile(os.path.join(root, "ops", g))]
abren_base += guiones_de("ops/db") + guiones_de("ops/maintenance") + [f"{backup}/{g}" for g in guiones]
# pgbouncer-reconnect.sh habla con la consola de administracion del POOLER, dentro de su propio
# contenedor (docker exec): no pasa por la base ni por este resolvedor.
docker_exec = re.compile(r"docker\s+exec\b")
secreto_en_argumento = re.compile(r"-v\s+[a-z_]+=\"?\$\{?[A-Za-z_]*(PASSWORD|VERIFIER|SECRET|TOKEN)")
for rel in abren_base:
    texto = leer(rel)
    bucles_read = 0
    for n, linea in sentencias(texto):
        # Profundidad de bucles `while ... read`; un bucle escrito en una sola linea abre y cierra
        # en ella, y su cuerpo tambien esta dentro.
        abre = bool(re.search(r"\bwhile\b.*\bread\b", linea))
        cierra = bool(re.match(r"\s*done\b", linea) or re.search(r";\s*done\b", linea))
        bucles_read_aqui = bucles_read + (1 if abre else 0)
        bucles_read = max(0, bucles_read_aqui - (1 if cierra else 0))
        if "declare -F cf_psql" in linea:
            continue
        if llamada.search(linea) and not docker_exec.search(linea):
            fallos.append(f"{rel}:{n} llama a psql/pg_dump/pg_restore sin cf_psql/cf_pg_dump/cf_pg_restore")
        if secreto_en_argumento.search(linea):
            fallos.append(f"{rel}:{n} pasa un secreto como argumento de psql (-v); usa cf_pg_pasar_entorno y \\getenv")
        # La entrada estandar es de quien la pide (ops/db/pg-credentials.sh): una orden con
        # heredoc o con `<fichero` tiene que ser de la familia "con entrada" (cf_psql_entrada);
        # las otras la sustituyen por /dev/null y el SQL no llegaria a ejecutarse, en silencio.
        if re.search(r"(?<!_entrada)\b(cf_psql|cf_pg_dump|cf_pg_restore)\b[^|]*(<<|<\s*\")", linea):
            fallos.append(f"{rel}:{n} da entrada estandar (heredoc o fichero) a una herramienta que la descarta: usa cf_psql_entrada")
        # Y al contrario: una de la familia "con entrada" DENTRO de un bucle `while read`, sin su
        # propia redireccion, se come la lista del bucle. Es lo que rompio tenant-service-role.sh --all.
        if bucles_read_aqui and "_entrada" in linea and not re.search(r"<<|<\s*\"|</dev/null", linea):
            fallos.append(f"{rel}:{n} llama a una herramienta con entrada dentro de un bucle `while read` sin su propia redireccion: se comeria la lista del bucle")
    # Los que sourcean pg-credentials.sh solo cuando PGHOST esta vacio (pruebas y make e2e lo
    # traen en el entorno) tienen que conservar el respaldo al binario del host.
    if 'PGHOST:-' in texto and "pg-credentials.sh" in texto and "cf_psql" in texto:
        for funcion in ("cf_psql", "cf_psql_entrada"):
            if re.search(rf"(?<![a-z_]){funcion}(?![a-z_])", texto) and \
               f"declare -F {funcion} >/dev/null || {funcion}()" not in texto:
                fallos.append(f"{rel} usa {funcion} sin el respaldo al psql del host (declare -F ... || {funcion}())")
    # Y lo contrario de pasar un secreto por argumento: un \getenv cuya variable nadie pasa al
    # contenedor efimero llegaria vacia.
    for variable in set(re.findall(r"\\getenv\s+[a-z_]+\s+([A-Z_][A-Z0-9_]*)", texto)):
        if f"cf_pg_pasar_entorno {variable}" not in texto:
            fallos.append(f"{rel} lee {variable} con \\getenv sin pasarla con cf_pg_pasar_entorno: en contenedor llegaria vacia")

respaldo = leer(f"{backup}/backup-tenants.sh")
if "LIKE 'mail\\_%'" not in respaldo:
    fallos.append("backup-tenants.sh ya no lista todas las bases mail_* (registro, celdas y empresas)")
# Los tres trabajos comparten el cerrojo de BACKUP_DIR y los tres tienen que ESPERARLO. Con
# `flock -n` uno abortaba en cuanto encontraba el cerrojo tomado, que pasa siempre que dos
# temporizadores se disparan juntos (al instalarlos, tras un reinicio o una caida, por Persistent=true):
# el respaldo de las bases perdia el turno entero (2026-09-23).
for trabajo in ("backup-tenants.sh", "backup-mail-volumes.sh", "verify-restore.sh"):
    texto = leer(f"{backup}/{trabajo}")
    if re.search(r"\bflock\s+-n\b", texto):
        fallos.append(f"{trabajo} no espera el cerrojo (flock -n): falla el turno si otro trabajo lo tiene")
    elif not re.search(r"\bflock\s+-w\s+\d+", texto):
        fallos.append(f"{trabajo} no toma el cerrojo de BACKUP_DIR con espera (flock -w)")

verificacion = leer(f"{backup}/verify-restore.sh")
for patron in ("mail_registry.dump", "'mail_cell_*.dump'", "'mail_tenant_*.dump'"):
    if patron not in verificacion:
        fallos.append(f"verify-restore.sh sin argumentos no cubre {patron}")

externo = leer(f"{backup}/destino-externo.sh")
m = re.search(r'CF_BACKUP_S3_CLI_IMAGE="\$\{BACKUP_S3_CLI_IMAGE:-([^}]+)\}"', externo)
if not m or not re.search(r"@sha256:[0-9a-f]{64}$", m.group(1)):
    fallos.append("destino-externo.sh: la imagen del CLI de S3 no esta fijada por digest")
for n, linea in codigo(externo) + [(n, l) for g in guiones for n, l in codigo(leer(f"{backup}/{g}"))]:
    if re.search(r"-e\s+(AWS_[A-Z_]+|PG[A-Z]+|BACKUP_[A-Z0-9_]+)=", linea):
        fallos.append(f"ops/backup:{n} pasa una credencial con su valor en -e")
    if re.search(r"--passphrase(\s|=)|--passphrase-file", linea):
        fallos.append(f"ops/backup:{n} pasa la frase de cifrado por argumento o fichero")
if not any("--passphrase-fd 0" in l for _, l in codigo(externo)):
    fallos.append("destino-externo.sh: gpg debe leer la frase por la entrada estandar")

secretos = lambda rel: {l.strip().rstrip("?") for l in leer(rel).splitlines() if l.strip() and not l.startswith("#")}
de_respaldo = secretos("ops/security/secrets/secret-keys-backup.txt")
usados = set(re.findall(r"_cf_secreto_respaldo (BACKUP_[A-Z0-9_]+)", externo))
if usados != de_respaldo:
    fallos.append(f"secret-keys-backup.txt ({sorted(de_respaldo)}) no coincide con lo que lee destino-externo.sh ({sorted(usados)})")
if cruce := de_respaldo & (secretos("ops/security/secrets/secret-keys.txt") | secretos("ops/security/secrets/secret-keys-db.txt")):
    fallos.append(f"secretos del respaldo en las listas que reciben los contenedores o el pooler: {sorted(cruce)}")
ejemplo = leer(".env.example")
for clave in sorted(de_respaldo):
    if re.search(rf"^\s*{clave}\s*=", ejemplo, re.M):
        fallos.append(f".env.example declara {clave}: el .env llega a todos los contenedores")
if not re.search(r'^\s*cat .*"\$KEYS_BACKUP_FILE"', leer("ops/security/secrets/check-secrets.sh"), re.M):
    fallos.append("check-secrets.sh no vigila secret-keys-backup.txt")

# --- volumenes de correo --------------------------------------------------------------------------
correo = leer(f"{backup}/backup-mail-volumes.sh")
restauracion_correo = leer(f"{backup}/restore-mail-volume.sh")
imagenes = [re.search(r'BACKUP_ARCHIVE_IMAGE:-([^}]+)\}', t) for t in (correo, restauracion_correo)]
if not all(imagenes) or len({i.group(1) for i in imagenes}) != 1 or not re.search(r"@sha256:[0-9a-f]{64}$", imagenes[0].group(1)):
    fallos.append("la imagen de tar del respaldo de correo no esta fijada por digest o difiere entre respaldo y restauracion")
if 'VOLUMENES:-crypt-vol vmail-vol' not in correo:
    fallos.append("backup-mail-volumes.sh ya no respalda por defecto crypt-vol (claves de mail_crypt) y vmail-vol (buzones)")
if not re.search(r'\[\[ "\$vol" == crypt-vol \]\] && ! cf_externo_cifra', correo):
    fallos.append("backup-mail-volumes.sh debe negarse a subir crypt-vol sin cifrar: es la clave que descifra todo el correo")
if "--exclude=./_garbage" not in correo:
    fallos.append("backup-mail-volumes.sh archiva _garbage (lo que Dovecot ya borro)")

# --- claves que el volcado no trae -----------------------------------------------------------------
# Una restauracion en un servidor nuevo necesita las claves del almacen, que no estan en ningun archivo
# del respaldo. El README las enumera y verify-restore.sh avisa de la cadena de auditoria de version 2,
# que sin AUDIT_HASH_KEY se restaura pero no se puede verificar.
lectura = leer(f"{backup}/README.md")
for clave in ("MAIL_ENCRYPTION_KEY", "AUDIT_HASH_KEY", "JWT_SIGNING_KEY", "MAIL_LINK_SIGNING_KEY"):
    if clave not in lectura.split("## Lo que el respaldo no trae", 1)[-1].split("## Configuración", 1)[0]:
        fallos.append(f"ops/backup/README.md no lista {clave} entre lo que el respaldo no trae y hay que guardar aparte")
if "AUDIT_HASH_KEY" not in leer(f"{backup}/verify-restore.sh"):
    fallos.append("verify-restore.sh ya no avisa de que la cadena de auditoria de version 2 restaurada exige AUDIT_HASH_KEY")

# --- cada trabajo programado tiene alerta ---------------------------------------------------------
reglas = leer("ops/observability/prometheus/rules/plataforma.yml")
trabajos = set()
for g in guiones:
    texto = leer(f"{backup}/{g}")
    trabajos |= set(re.findall(r"cf_publicar_metrica ([a-z_]+)", texto))
    if 'cf_publicar_metrica "$TRABAJO"' in texto:
        m = re.search(r"^TRABAJO=([a-z_]+)$", texto, re.M)
        if m:
            trabajos.add(m.group(1))
for trabajo in sorted(trabajos):
    if f'trabajo="{trabajo}"' not in reglas and f'{trabajo}|' not in reglas and f'|{trabajo}' not in reglas:
        fallos.append(f"ninguna alerta de plataforma.yml vigila el trabajo {trabajo}: su ausencia no se notaria")

instalador = leer(f"{backup}/install-timers.sh")
usuario = re.search(r"^PLANTILLA_USUARIO=(\S+)$", instalador, re.M)
ruta = re.search(r"^PLANTILLA_RUTA=(\S+)$", instalador, re.M)
temporizadores = re.search(r"^TEMPORIZADORES=\(([^)]*)\)$", instalador, re.M)
unidades = sorted(os.listdir(os.path.join(root, backup, "systemd")))
if not (usuario and ruta and temporizadores):
    fallos.append("install-timers.sh sin PLANTILLA_USUARIO, PLANTILLA_RUTA o TEMPORIZADORES")
else:
    if sorted(temporizadores.group(1).split()) != sorted(u for u in unidades if u.endswith(".timer")):
        fallos.append("install-timers.sh no activa exactamente los temporizadores de ops/backup/systemd")
    for u in unidades:
        texto = leer(f"{backup}/systemd/{u}")
        if u.endswith(".timer"):
            if u.replace(".timer", ".service") not in unidades:
                fallos.append(f"{u} sin su .service")
            continue
        if f"User={usuario.group(1)}" not in texto.splitlines():
            fallos.append(f"{u}: User= no es el de la plantilla ({usuario.group(1)}), install-timers.sh no lo reescribiria")
        if "UMask=0077" not in texto.splitlines():
            fallos.append(f"{u}: sin UMask=0077")
        exec_start = re.search(r"^ExecStart=(\S+)$", texto, re.M)
        if not exec_start or not exec_start.group(1).startswith(ruta.group(1) + "/ops/backup/"):
            fallos.append(f"{u}: ExecStart fuera de {ruta.group(1)}/ops/backup")
        elif not os.path.isfile(os.path.join(root, exec_start.group(1)[len(ruta.group(1)) + 1:])):
            fallos.append(f"{u}: {exec_start.group(1)} no existe en el repositorio")
        if f"Environment=APP_DIR={ruta.group(1)}" not in texto.splitlines():
            fallos.append(f"{u}: sin Environment=APP_DIR={ruta.group(1)}")
if not any('"$SCRIPT_DIR/../backup/install-timers.sh"' in l and '"$DEPLOY_PATH/ops/backup/install-timers.sh"' in l
           for _, l in codigo(leer("ops/server-template/bootstrap.sh"))):
    fallos.append("bootstrap.sh no instala el respaldo por ops/backup/install-timers.sh")

credenciales = leer("ops/db/pg-credentials.sh")
servicio = re.search(r"^\s*CF_PG_SERVICIO=(\S+)$", credenciales, re.M)
if not servicio:
    fallos.append("pg-credentials.sh sin CF_PG_SERVICIO")
else:
    if not re.search(rf"^  {re.escape(servicio.group(1))}:\s*$", leer("docker-compose.selfhosted.yml"), re.M):
        fallos.append(f"CF_PG_SERVICIO={servicio.group(1)} no es un servicio de docker-compose.selfhosted.yml")
    if f"[postgres]={servicio.group(1)}" not in leer("ops/security/internal-tls.sh"):
        fallos.append(f"CF_PG_SERVICIO={servicio.group(1)} no es el nombre del certificado de Postgres en internal-tls.sh")

despliegue = re.search(r"FICHEROS_SERVIDOR=\(([^)]*)\)", leer("scripts/deploy-ecr.sh"), re.S)
for ruta_srv in ("ops/backup", "ops/db", "ops/maintenance"):
    if not despliegue or ruta_srv not in despliegue.group(1).split():
        fallos.append(f"scripts/deploy-ecr.sh no sincroniza {ruta_srv} al servidor")

for f in fallos:
    print(f"  FALLA: {f}", file=sys.stderr)
sys.exit(1 if fallos else 0)
PY

[[ $FALLOS -eq 0 ]] || exit 1
echo "  OK: respaldos, migraciones y roles con la herramienta del servidor por verify-full en el perfil autoalojado y la del host en aws, sin secretos en argumentos, copia externa cifrada y fijada, tres clases cubiertas y temporizadores instalables."
