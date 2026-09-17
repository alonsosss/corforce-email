#!/usr/bin/env bash
# Copia externa de los respaldos: un bucket S3 o S3-compatible (Cloudflare R2, MinIO), OPCIONAL.
#
#   . ops/backup/destino-externo.sh
#   cf_externo_activo && cf_externo_subir <fichero> <clave>
#   cf_externo_bajar s3://<bucket>/<clave> <fichero>
#
# Sin BACKUP_S3_BUCKET no hay copia externa y el respaldo local funciona igual: que falte el destino
# no puede dejar a un servidor sin respaldo.
#
# Configuracion (no secreta; entorno o .env de la aplicacion):
#   BACKUP_S3_BUCKET     bucket; vacio = sin copia externa
#   BACKUP_S3_ENDPOINT   https://<cuenta>.r2.cloudflarestorage.com u otro S3-compatible; vacio = AWS
#   BACKUP_S3_REGION     region de firma (R2: auto); vacio = la del CLI
#   BACKUP_SECRETS_FILE  fichero de secretos del respaldo (por defecto /opt/core-force-mail/env/backup.env)
#
# Secretos (ops/security/secrets/secret-keys-backup.txt), del entorno o de BACKUP_SECRETS_FILE:
#   BACKUP_S3_ACCESS_KEY_ID, BACKUP_S3_SECRET_ACCESS_KEY   vacias en AWS: rol de la instancia
#   BACKUP_ENCRYPTION_PASSPHRASE                           cifra lo que sale del servidor
#
# Por que un fichero propio y no el .env: el .env llega entero a cada contenedor por env_file, asi
# que una credencial del bucket o la frase de cifrado que viviera ahi la veria todo servicio. Con
# ambas, quien compromete un servicio lee y borra el historico de respaldos.
#
# Cifrado: con BACKUP_S3_ENDPOINT (un tercero que no es la cuenta de AWS del despliegue) es
# obligatorio; sin frase no se sube nada y el trabajo falla. En AWS es opcional: el bucket ya cifra
# en reposo y el acceso lo da el rol. Es gpg simetrico (AES-256, paquete con integridad), que la
# plantilla del servidor ya instala: sin dependencias nuevas en el host. Lo que se sube se descifra
# y se compara con el original antes de subirlo.
#
# El CLI de S3: en el perfil aws, el `aws` del host (el rol de la instancia no alcanza a un
# contenedor, IMDSv2 limita el salto). En el autoalojado, que no tiene el CLI instalado, la imagen
# oficial fijada por digest en un contenedor efimero; las credenciales entran por el entorno con -e
# y solo el nombre.
#
# Sin `set -e`: se sourcea. Necesita cf_read_env y CF_PERFIL_DESPLIEGUE
# (ops/maintenance/entorno-despliegue.sh).

CF_BACKUP_S3_CLI_IMAGE="${BACKUP_S3_CLI_IMAGE:-amazon/aws-cli:2.27.50@sha256:48c3d4212e2f5b0e24bdc6af7708f9412ce65425a79575e0f78b8f8c0dcd70ab}"
CF_BACKUP_FRASE_MINIMA=32

# _cf_secreto_respaldo <CLAVE>: del entorno o del fichero de secretos, sin ejecutarlo. El fichero
# tiene que ser del usuario que corre el respaldo y no legible por otros.
_cf_secreto_respaldo() {
  local clave="$1" fichero modo dueno
  if [[ -n "${!clave:-}" ]]; then
    printf '%s' "${!clave}"
    return 0
  fi
  fichero="${BACKUP_SECRETS_FILE:-$(cf_read_env BACKUP_SECRETS_FILE)}"
  fichero="${fichero:-/opt/core-force-mail/env/backup.env}"
  [[ -f "$fichero" ]] || return 0
  modo="$(stat -c '%a' "$fichero")"
  dueno="$(stat -c '%u' "$fichero")"
  if [[ "$dueno" != "$(id -u)" || "$modo" =~ [1-7]$ || "$modo" =~ [1-7].$ ]]; then
    echo "  FALLA: $fichero debe ser del usuario $(id -un) y 0600 (es $modo de uid $dueno); no se usa" >&2
    return 1
  fi
  sed -n -E "s/^[[:space:]]*$clave=(.*)$/\\1/p" "$fichero" | tail -n 1 | sed -E "s/^\"(.*)\"$/\\1/; s/^'(.*)'$/\\1/"
}

# cf_externo_cargar: lee configuracion y secretos una vez. Falla si la configuracion es incoherente.
cf_externo_cargar() {
  CF_S3_BUCKET="${BACKUP_S3_BUCKET:-$(cf_read_env BACKUP_S3_BUCKET)}"
  CF_S3_ENDPOINT="${BACKUP_S3_ENDPOINT:-$(cf_read_env BACKUP_S3_ENDPOINT)}"
  CF_S3_REGION="${BACKUP_S3_REGION:-$(cf_read_env BACKUP_S3_REGION)}"
  [[ -n "$CF_S3_BUCKET" ]] || return 0
  if [[ -n "$CF_S3_ENDPOINT" && ! "$CF_S3_ENDPOINT" =~ ^https?://[A-Za-z0-9.-]+(:[0-9]+)?/?$ ]]; then
    echo "  FALLA: BACKUP_S3_ENDPOINT no es scheme://host[:puerto]" >&2
    return 1
  fi
  CF_S3_ACCESS_KEY_ID="$(_cf_secreto_respaldo BACKUP_S3_ACCESS_KEY_ID)" || return 1
  CF_S3_SECRET_ACCESS_KEY="$(_cf_secreto_respaldo BACKUP_S3_SECRET_ACCESS_KEY)" || return 1
  CF_BACKUP_FRASE="$(_cf_secreto_respaldo BACKUP_ENCRYPTION_PASSPHRASE)" || return 1
  if [[ -n "$CF_S3_ACCESS_KEY_ID" && -z "$CF_S3_SECRET_ACCESS_KEY" || -z "$CF_S3_ACCESS_KEY_ID" && -n "$CF_S3_SECRET_ACCESS_KEY" ]]; then
    echo "  FALLA: BACKUP_S3_ACCESS_KEY_ID y BACKUP_S3_SECRET_ACCESS_KEY van juntas" >&2
    return 1
  fi
  if [[ -n "$CF_S3_ENDPOINT" && -z "$CF_S3_ACCESS_KEY_ID" ]]; then
    echo "  FALLA: BACKUP_S3_ENDPOINT sin credenciales: un destino fuera de AWS no tiene rol de instancia" >&2
    return 1
  fi
  if [[ -n "$CF_S3_ENDPOINT" && -z "$CF_BACKUP_FRASE" ]]; then
    echo "  FALLA: BACKUP_S3_ENDPOINT sin BACKUP_ENCRYPTION_PASSPHRASE: nada sale del servidor sin cifrar" >&2
    return 1
  fi
  if [[ -n "$CF_BACKUP_FRASE" && ${#CF_BACKUP_FRASE} -lt $CF_BACKUP_FRASE_MINIMA ]]; then
    echo "  FALLA: BACKUP_ENCRYPTION_PASSPHRASE con menos de $CF_BACKUP_FRASE_MINIMA caracteres (openssl rand -base64 48)" >&2
    return 1
  fi
  if [[ -n "$CF_BACKUP_FRASE" ]] && ! command -v gpg >/dev/null; then
    echo "  FALLA: cifrar los respaldos necesita gpg (paquete gnupg)" >&2
    return 1
  fi
  return 0
}

cf_externo_activo() { [[ -n "${CF_S3_BUCKET:-}" ]]; }
cf_externo_cifra() { [[ -n "${CF_BACKUP_FRASE:-}" ]]; }

# _cf_s3 <argumentos de aws s3>: rutas locales absolutas, montadas en la misma ruta.
_cf_s3() {
  local extra=() dir
  [[ -n "$CF_S3_ENDPOINT" ]] && extra+=(--endpoint-url "$CF_S3_ENDPOINT")
  [[ -n "$CF_S3_REGION" ]] && extra+=(--region "$CF_S3_REGION")
  (
    if [[ -n "$CF_S3_ACCESS_KEY_ID" ]]; then
      export AWS_ACCESS_KEY_ID="$CF_S3_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$CF_S3_SECRET_ACCESS_KEY"
    fi
    # Los S3-compatibles no aceptan todos las sumas de integridad que el CLI 2.23+ anade por defecto.
    if [[ -n "$CF_S3_ENDPOINT" ]]; then
      export AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
    fi
    if [[ "${CF_PERFIL_DESPLIEGUE:-aws}" != selfhosted ]]; then
      exec aws s3 "$@" "${extra[@]}" --only-show-errors
    fi
    dir="$CF_S3_DIR_LOCAL"
    exec docker run --rm --network host --user "$(id -u):$(id -g)" --read-only --tmpfs /tmp \
      --cap-drop ALL --security-opt no-new-privileges -e HOME=/tmp \
      -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REQUEST_CHECKSUM_CALCULATION \
      -e AWS_RESPONSE_CHECKSUM_VALIDATION -v "$dir:$dir" \
      "$CF_BACKUP_S3_CLI_IMAGE" s3 "$@" "${extra[@]}" --only-show-errors
  )
}

# _cf_gpg <argumentos>: la frase entra por la entrada estandar (--passphrase-fd 0) desde printf, que
# es interno de bash: nunca aparece en la lista de procesos.
_cf_gpg() {
  local home rc
  home="$(mktemp -d)" || return 1
  printf '%s' "$CF_BACKUP_FRASE" | gpg --homedir "$home" --batch --yes --quiet --no-tty \
    --pinentry-mode loopback --passphrase-fd 0 "$@"
  rc=$?
  rm -rf "$home"
  return $rc
}

# cf_cifrar <origen> <destino>: cifra y comprueba que descifra al mismo contenido.
cf_cifrar() {
  local original descifrado
  _cf_gpg --symmetric --cipher-algo AES256 --s2k-digest-algo SHA512 --s2k-count 65011712 \
    --compress-algo none -o "$2" "$1" || return 1
  original="$(sha256sum <"$1" | cut -d' ' -f1)"
  descifrado="$(_cf_gpg --decrypt "$2" | sha256sum | cut -d' ' -f1)"
  [[ -n "$original" && "$original" == "$descifrado" ]] || {
    echo "  FALLA: el cifrado de $1 no descifra al mismo contenido" >&2
    rm -f "$2"
    return 1
  }
}

# cf_descifrar <origen> <destino>
cf_descifrar() {
  [[ -n "${CF_BACKUP_FRASE:-}" ]] || { echo "FALLA: $1 esta cifrado y falta BACKUP_ENCRYPTION_PASSPHRASE" >&2; return 1; }
  _cf_gpg --decrypt -o "$2" "$1"
}

# cf_externo_subir <fichero local absoluto> <clave en el bucket>
cf_externo_subir() {
  CF_S3_DIR_LOCAL="$(dirname "$1")" _cf_s3 cp "$1" "s3://$CF_S3_BUCKET/$2"
}

# cf_externo_bajar <s3://bucket/clave> <fichero local absoluto>
cf_externo_bajar() {
  CF_S3_DIR_LOCAL="$(dirname "$2")" _cf_s3 cp "$1" "$2"
}
