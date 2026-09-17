#!/usr/bin/env bash
# Un servidor solo levanta contenedores si su .env declara ENVIRONMENT=production o staging.
#
# development y test relajan controles de los servicios (Redis en claro, credencial de
# plataforma en los servicios de celda, clave de firma efimera en identity:
# docs/Operacion_Despliegue.md, 1) y .env.example trae development para el entorno local.
# Un servidor montado a partir de ese fichero no fallaria en nada: quedaria abierto sin que
# nada lo dijera. with-secrets.sh, por donde pasa todo docker compose del servidor, ejecuta
# esto antes de materializar ningun secreto.
#
# Se juzga el valor que recibiran los contenedores: la ultima asignacion de ENVIRONMENT en el
# .env que Compose les pasa como env_file, no el entorno de quien despliega, que no les
# llega. Solo vale la forma exacta ENVIRONMENT=production o ENVIRONMENT=staging, con comillas
# opcionales: con espacios, un comentario o un CR al final el servicio recibiria otro texto
# y no lo reconoceria como produccion.
set -euo pipefail

env_file="${WITH_SECRETS_ENV_FILE:-${APP_DIR:-.}/.env}"

rechazar() {
  echo "despliegue rechazado: $1" >&2
  echo "  Un servidor declara en $env_file exactamente ENVIRONMENT=production o ENVIRONMENT=staging" >&2
  echo "  (lo escribe ops/server-template/bootstrap.sh). development y test son solo para el" >&2
  echo "  entorno local: make dev, make e2e y make test-integration." >&2
  exit 1
}

[[ -f "$env_file" ]] || rechazar "no existe $env_file"

linea="$(grep -E '^[[:space:]]*(export[[:space:]]+)?ENVIRONMENT[[:space:]]*=' "$env_file" | tail -n 1 || true)"
[[ -n "$linea" ]] || rechazar "$env_file no declara ENVIRONMENT"

case "$linea" in
  ENVIRONMENT=production | ENVIRONMENT=staging | \
  ENVIRONMENT=\"production\" | ENVIRONMENT=\"staging\" | \
  ENVIRONMENT=\'production\' | ENVIRONMENT=\'staging\') ;;
  *) rechazar "ENVIRONMENT no es production ni staging: $(printf '%q' "$linea")" ;;
esac

# Marcadores de .env.example. YOUR_DOMAIN nunca vale en un servidor: nada lo sustituye y acabaria en
# los registros DNS que se indican a cada empresa (MX, SPF, DMARC), en los enlaces y en los origenes
# permitidos. Se nombran las claves, nunca los valores; las lineas comentadas no cuentan.
claves_con() {
  sed -n -E "s/^[[:space:]]*(export[[:space:]]+)?([A-Z][A-Z0-9_]*)=.*$1.*/\\2/p" "$env_file" | LC_ALL=C sort -u | tr '\n' ' '
}
con_dominio="$(claves_con YOUR_DOMAIN)"
if [[ -n "$con_dominio" ]]; then
  echo "despliegue rechazado: $env_file conserva el marcador YOUR_DOMAIN de .env.example en: $con_dominio" >&2
  echo "  Pon el dominio real de la plataforma antes de levantar nada." >&2
  exit 1
fi
# CHANGE_ME solo avisa: los secretos del almacen tienen prioridad, pero si el almacen no responde el
# resolvedor recurre al .env y ese marcador acabaria usandose como credencial.
con_cambiar="$(claves_con CHANGE_ME)"
if [[ -n "$con_cambiar" ]]; then
  echo "aviso: $env_file conserva el marcador CHANGE_ME en: $con_cambiar" >&2
  echo "  Retira esas lineas: los secretos van en el almacen (ops/security/secrets)." >&2
fi
