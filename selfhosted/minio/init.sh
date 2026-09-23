#!/bin/sh
# Inicializacion idempotente de MinIO en la produccion autoalojada (servicio minio-init de
# docker-compose.selfhosted.yml). Corre en cada despliegue despues de que minio este sano y deja:
#   - el bucket MINIO_BUCKET, privado (sin ninguna lectura anonima);
#   - la politica core-force-media, acotada a ese bucket: leer, escribir y listar, sin borrar
#     (los objetos public/ son imagenes que correos ya enviados siguen mostrando) y sin tocar
#     la configuracion del bucket;
#   - el usuario de servicio MINIO_ACCESS_KEY/MINIO_SECRET_KEY con esa politica y ninguna otra,
#     que es la credencial que reciben gateway y templates (pkg/objectstore).
#
# Las credenciales llegan solo por el entorno y del almacen de secretos: la raiz entra en mc por
# MC_HOST_local (nunca en la linea de ordenes) y la clave secreta del usuario por la entrada
# estandar de `mc admin user add`. Nada de esto se imprime.
set -eu

falla() {
  echo "minio-init: $*" >&2
  exit 1
}

# credencial <nombre> <minimo> <maximo>: presente, con la longitud que MinIO admite y de un juego
# de caracteres que no necesita escaparse dentro de la URL de MC_HOST_local.
credencial() {
  eval "valor=\${$1:-}"
  [ -n "$valor" ] || falla "falta $1 (almacen de secretos, ops/security/secrets/add-secret.sh)"
  largo=${#valor}
  [ "$largo" -ge "$2" ] && [ "$largo" -le "$3" ] || falla "$1 debe tener de $2 a $3 caracteres"
  case "$valor" in
    *[!A-Za-z0-9_-]*) falla "$1 solo admite [A-Za-z0-9_-] (openssl rand -hex)" ;;
  esac
}

credencial MINIO_ROOT_USER 3 64
credencial MINIO_ROOT_PASSWORD 32 64
credencial MINIO_ACCESS_KEY 16 20
credencial MINIO_SECRET_KEY 32 40
[ "$MINIO_ACCESS_KEY" != "$MINIO_ROOT_USER" ] || falla "MINIO_ACCESS_KEY no puede ser el usuario raiz"
[ "$MINIO_SECRET_KEY" != "$MINIO_ROOT_PASSWORD" ] || falla "MINIO_SECRET_KEY no puede ser la contrasena raiz"

BUCKET="${MINIO_BUCKET:-}"
# Nombre de bucket S3: 3 a 63 caracteres, minusculas, digitos, punto y guion, sin empezar ni acabar
# en separador. La imagen no trae grep ni sed: todo con sh.
case "$BUCKET" in
  "" | *[!a-z0-9.-]* | [.-]* | *[.-]) falla "MINIO_BUCKET no es un nombre de bucket valido: '$BUCKET' (.env)" ;;
esac
[ ${#BUCKET} -ge 3 ] && [ ${#BUCKET} -le 63 ] || falla "MINIO_BUCKET debe tener de 3 a 63 caracteres"
ENDPOINT="${MINIO_INIT_ENDPOINT:-minio:9000}"
POLITICA=core-force-media

mc() { command mc --config-dir /tmp/mc --quiet --no-color "$@"; }
MC_HOST_local="http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@$ENDPOINT"
export MC_HOST_local

mc mb --ignore-existing "local/$BUCKET" >/dev/null || falla "no se pudo crear el bucket $BUCKET"
mc anonymous set none "local/$BUCKET" >/dev/null || falla "no se pudo retirar el acceso anonimo de $BUCKET"

cat >/tmp/politica.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetBucketLocation", "s3:ListBucket"],
      "Resource": ["arn:aws:s3:::$BUCKET"]
    },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": ["arn:aws:s3:::$BUCKET/*"]
    }
  ]
}
EOF
mc admin policy create local "$POLITICA" /tmp/politica.json >/dev/null || falla "no se pudo crear la politica $POLITICA"

# Crea el usuario o, si existe, le fija la clave del almacen (una rotacion se aplica asi).
printf '%s\n' "$MINIO_SECRET_KEY" | mc admin user add local "$MINIO_ACCESS_KEY" >/dev/null ||
  falla "no se pudo crear el usuario de servicio"

# Exactamente esta politica: cualquier otra que tuviera adjunta se retira.
info="$(mc admin user info local "$MINIO_ACCESS_KEY" --json)" || falla "no se pudo leer el usuario de servicio"
adjuntas=""
case "$info" in
  *'"policyName":"'*)
    resto="${info#*\"policyName\":\"}"
    adjuntas="$(printf '%s' "${resto%%\"*}" | tr ',' ' ')"
    ;;
esac
for p in $adjuntas; do
  [ "$p" = "$POLITICA" ] && continue
  mc admin policy detach local "$p" --user "$MINIO_ACCESS_KEY" >/dev/null || falla "no se pudo retirar la politica $p"
done
case " $adjuntas " in
  *" $POLITICA "*) ;;
  *) mc admin policy attach local "$POLITICA" --user "$MINIO_ACCESS_KEY" >/dev/null || falla "no se pudo adjuntar $POLITICA" ;;
esac

echo "minio-init: bucket $BUCKET privado y usuario de servicio con la politica $POLITICA"
