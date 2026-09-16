#!/usr/bin/env bash
# Materializa los secretos de produccion desde AWS Secrets Manager a un fichero de entorno
# en memoria (tmpfs), que es lo que consumen Compose y los contenedores.
#
# Por que asi:
#   - El fichero vive en /dev/shm (memoria): no queda texto plano en disco ni en las copias
#     de seguridad del volumen, y desaparece al reiniciar la maquina.
#   - La identidad la aporta el rol IAM de la instancia; no hay ninguna credencial de AWS
#     en el servidor, y cada lectura queda registrada en CloudTrail.
#   - La escritura es atomica y solo se publica si TODOS los secretos requeridos vinieron:
#     un fichero a medias arrancaria servicios sin credencial, que es peor que no arrancar.
#
# Uso:
#   ops/security/secrets/fetch-secrets.sh          # materializa
#   SECRETS_ID=core-force-mail/staging ... fetch-secrets.sh
#
# Los scripts de despliegue lo invocan antes de cualquier 'docker compose up'.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_FILE="${SECRET_KEYS_FILE:-$SCRIPT_DIR/secret-keys.txt}"
# Las credenciales de BASE se materializan aparte: el fichero de arriba lo reciben los
# contenedores por env_file, asi que una credencial de base que viviera ahi la veria TODO el
# despliegue. Las de abajo solo llegan al servicio al que su bloque de compose se las pasa
# por `environment:` (ops/db/service-credentials.json).
KEYS_DB_FILE="${SECRET_KEYS_DB_FILE:-$SCRIPT_DIR/secret-keys-db.txt}"
SECRET_ID="${SECRETS_ID:-core-force-mail/prod}"
REGION="${AWS_REGION:-us-east-1}"
OUT="${SECRETS_ENV_FILE:-/dev/shm/core-force-mail/secrets.env}"
OUT_DB="${SECRETS_DB_ENV_FILE:-/dev/shm/core-force-mail/secrets-db.env}"

command -v aws >/dev/null || { echo "fetch-secrets: falta el CLI de AWS" >&2; exit 1; }
[[ -f "$KEYS_FILE" ]] || { echo "fetch-secrets: no existe $KEYS_FILE" >&2; exit 1; }
[[ -f "$KEYS_DB_FILE" ]] || { echo "fetch-secrets: no existe $KEYS_DB_FILE" >&2; exit 1; }

payload="$(aws secretsmanager get-secret-value \
    --secret-id "$SECRET_ID" --region "$REGION" \
    --query SecretString --output text)" || {
  echo "fetch-secrets: no se pudo leer $SECRET_ID en $REGION." >&2
  echo "  Revisar que el rol de la instancia tenga la politica core-force-mail-secretos (ops/aws/setup-iam.sh)." >&2
  exit 1
}

mkdir -p "$(dirname "$OUT")"
chmod 700 "$(dirname "$OUT")"

umask 077
tmp="$(mktemp "${OUT}.XXXXXX")"
tmp_db="$(mktemp "${OUT_DB}.XXXXXX")"
trap 'rm -f "$tmp" "$tmp_db"' EXIT

SECRETS_PAYLOAD="$payload" python3 - "$KEYS_FILE" "$tmp" "$KEYS_DB_FILE" "$tmp_db" <<'PY'
import json
import os
import sys

data = json.loads(os.environ["SECRETS_PAYLOAD"])
# (lista de claves, fichero de salida) por cada uno de los dos ficheros. Se resuelven los
# dos ANTES de escribir nada: la publicacion sigue siendo todo o nada, ahora sobre el par.
grupos = [(sys.argv[1], sys.argv[2]), (sys.argv[3], sys.argv[4])]

def declaradas(keys_file):
    return [
        line.strip() for line in open(keys_file, encoding="utf-8")
        if line.strip() and not line.startswith("#")
    ]

salidas, total_req, total = [], 0, 0
for keys_file, out in grupos:
    claves = declaradas(keys_file)
    # El sufijo "?" marca opcional: se materializa si esta, pero su ausencia no bloquea al
    # resto. Una integracion sin contratar no puede impedir que arranque la plataforma.
    required = [k for k in claves if not k.endswith("?")]
    optional = [k[:-1] for k in claves if k.endswith("?")]

    missing = [k for k in required if not str(data.get(k, "")).strip()]
    if missing:
        # Sin valor no se publica nada: un servicio con la credencial vacia falla de formas
        # mucho mas dificiles de diagnosticar que uno que no arranca.
        print("fetch-secrets: faltan secretos en el almacen: " + ", ".join(missing), file=sys.stderr)
        sys.exit(1)

    presentes = required + [k for k in optional if str(data.get(k, "")).strip()]
    lineas = ["# Generado por ops/security/secrets/fetch-secrets.sh. No editar a mano.\n"]
    for k in presentes:
        v = str(data[k])
        if "\n" in v:
            print(f"fetch-secrets: el secreto {k} contiene un salto de linea", file=sys.stderr)
            sys.exit(1)
        lineas.append(f"{k}={v}\n")
    salidas.append((out, lineas))
    total_req += len(required)
    total += len(presentes)

for out, lineas in salidas:
    with open(out, "w", encoding="utf-8") as fh:
        fh.writelines(lineas)
print(f"fetch-secrets: {total} secretos materializados "
      f"({total_req} requeridos, {total - total_req} opcionales presentes)")
PY

chmod 600 "$tmp" "$tmp_db"
mv -f "$tmp" "$OUT"
mv -f "$tmp_db" "$OUT_DB"
trap - EXIT
echo "fetch-secrets: $OUT y $OUT_DB listos"
