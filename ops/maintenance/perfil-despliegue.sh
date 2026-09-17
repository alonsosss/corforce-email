#!/usr/bin/env bash
# Perfil de despliegue que declara el .env de un servidor, y lo que implica para Compose.
#
#   ops/maintenance/perfil-despliegue.sh [--env <fichero>] --nombre    # aws | selfhosted
#   ops/maintenance/perfil-despliegue.sh [--env <fichero>] --compose   # argumentos -f de compose
#   ops/maintenance/perfil-despliegue.sh [--env <fichero>] --infra     # servicios propios del perfil
#
# DEPLOY_PROFILE en el .env (configuracion, no secreto): vacio o aws es la plataforma sobre
# servicios gestionados de AWS (docker-compose.yml); selfhosted es la produccion autoalojada, que
# anade docker-compose.selfhosted.yml y levanta ella misma Postgres, Redis, PgBouncer y el proxy
# de borde. Los dos caminos de despliegue (scripts/deploy-ecr.sh y release.yml) le preguntan a
# este guion en vez de decidirlo cada uno, para que no puedan divergir. Cualquier otro valor
# detiene el despliegue: un perfil mal escrito no puede caer en silencio en el de AWS, que en un
# servidor propio levanta los servicios sin base ni TLS.
#
# Con selfhosted comprueba ademas que existen la CA interna y los directorios de certificado que
# el perfil monta (ops/security/internal-tls.sh): sin la CA no arranca ni PgBouncer ni ningun
# cliente de Redis, y sin un directorio Docker lo crearia vacio y ese servicio no arrancaria (un
# servidor anterior a un certificado nuevo, como el de mail-auth); el fallo se veria contenedor a
# contenedor. Los directorios son 0700 de cada servicio: aqui solo se puede ver que existen. Solo lee claves de configuracion y nunca imprime valores de otras.
set -euo pipefail

ENV_FILE="${APP_DIR:-.}/.env"
ACCION=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV_FILE="${2:?--env necesita un fichero}"; shift 2 ;;
    --nombre | --compose | --infra) ACCION="${1#--}"; shift ;;
    *) echo "perfil-despliegue: opcion desconocida: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$ACCION" ]] || { echo "uso: $0 [--env <fichero>] --nombre|--compose|--infra" >&2; exit 2; }
[[ -f "$ENV_FILE" ]] || { echo "perfil-despliegue: no existe $ENV_FILE" >&2; exit 1; }

# valor <CLAVE>: la ultima asignacion sin comillas envolventes, como la lee Compose.
valor() {
  sed -n -E "s/^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=(.*)$/\\2/p" "$ENV_FILE" | tail -n 1 |
    sed -E "s/^\"(.*)\"$/\\1/; s/^'(.*)'$/\\1/"
}

perfil="$(valor DEPLOY_PROFILE)"
case "$perfil" in
  "" | aws) perfil=aws ;;
  selfhosted) ;;
  *)
    echo "perfil-despliegue: DEPLOY_PROFILE no valido en $ENV_FILE: $(printf '%q' "$perfil") (aws o selfhosted)" >&2
    exit 1
    ;;
esac

if [[ "$perfil" == selfhosted && "$ACCION" != nombre ]]; then
  tls="$(valor INTERNAL_TLS_DIR)"
  tls="${tls:-/opt/core-force-mail/tls}"
  if [[ ! -s "$tls/publico/ca.crt" ]]; then
    echo "perfil-despliegue: DEPLOY_PROFILE=selfhosted sin la CA interna en $tls/publico/ca.crt" >&2
    echo "  Generala antes de desplegar: sudo ops/security/internal-tls.sh (docs/Operacion_Despliegue.md, 11)" >&2
    exit 1
  fi
  for dir in postgres redis mail-auth; do
    if [[ ! -d "$tls/$dir" ]]; then
      echo "perfil-despliegue: DEPLOY_PROFILE=selfhosted sin el certificado interno de $dir ($tls/$dir)" >&2
      echo "  Emitelo antes de desplegar: sudo ops/security/internal-tls.sh (docs/Operacion_Despliegue.md, 11)" >&2
      exit 1
    fi
  done
fi

case "$ACCION:$perfil" in
  nombre:*) echo "$perfil" ;;
  compose:aws) echo "-f docker-compose.yml" ;;
  compose:selfhosted) echo "-f docker-compose.yml -f docker-compose.selfhosted.yml" ;;
  infra:aws) ;;
  # En orden de arranque: las dependencias antes; el proxy de borde, tras el gateway.
  infra:selfhosted) echo "postgres-primary redis pgbouncer nats edge-proxy" ;;
esac
