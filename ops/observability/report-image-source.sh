#!/usr/bin/env bash
# Publica, como metrica, de donde viene la imagen que corre cada servicio.
#
# EL PROBLEMA QUE VIGILA
#
# El despliegue bueno compila en el PC y el servidor solo hace pull de ECR. Cualquier
# `docker compose build/up` ejecutado EN el servidor compila con la copia de
# /opt/core-force-mail/app, que llega por rsync selectivo y puede estar atrasada: el contenedor
# queda sano, `docker ps` lo muestra "Up", y dentro corre codigo ANTERIOR al desplegado. Es
# la unica forma en que la plataforma retrocede de version sin sintoma visible.
#
# Ya paso dos veces. La segunda, el 2026-08-16, el gateway se recreo desde una imagen local
# diez minutos despues de un despliegue: su binario era anterior a la ruta /remotes/
# asociaciones, asi que ese modulo -y solo ese- dejo de cargar. Trece remotes respondian 200
# y uno daba 404. Nadie se entero hasta que alguien abrio esa pantalla.
#
# POR QUE NO BASTA scripts/check-image-drift.sh
#
# Aquel compara y avisa, pero corre DENTRO del despliegue. El retroceso de arriba ocurrio
# diez minutos DESPUES del ultimo, asi que no habia nada mirando. Esto corre por cron y
# convierte un fallo mudo en una alerta.
#
# QUE SE PUBLICA Y QUE NO
#
# Se publica el ORIGEN de la imagen (ECR o local), no si la etiqueta coincide con el ultimo
# despliegue: un despliegue solo reconstruye los servicios afectados, asi que la mayoria
# corre legitimamente una etiqueta anterior y alertar por eso seria ruido puro que enterraria
# el aviso que importa.
#
# Los contenedores ausentes no emiten `local=0`: eso diria "todo bien" de algo que ni
# siquiera esta. Se cuentan aparte, y de que un servicio este caido ya avisa ServicioCaido.
#
# Uso:
#   ops/observability/report-image-source.sh        # en el SERVIDOR, por cron
set -euo pipefail

APP_DIR="${CF_APP_DIR:-/opt/core-force-mail/app}"
# Mismo directorio que usan los trabajos de respaldo: node-exporter lo monta como /textfile.
# Vive dentro del arbol de la aplicacion porque el usuario que corre el cron no tiene root y
# no puede crear /var/lib/node_exporter, que es lo habitual.
METRICS_DIR="${CF_METRICS_DIR:-/opt/core-force-mail/metrics}"
DESTINO="$METRICS_DIR/core_force_imagenes.prom"

# La lista de servicios sale del override de despliegue, igual que en check-image-drift.sh:
# un servicio nuevo entra solo, sin tocar este script.
mapfile -t SVCS < <(grep -E '^  [a-z0-9-]+:$' "$APP_DIR/docker-compose.images.yml" | tr -d ' :')
if [[ ${#SVCS[@]} -eq 0 ]]; then
  echo "no se pudo leer la lista de servicios de docker-compose.images.yml" >&2
  exit 2
fi

# Una sola llamada a docker para los ~112 contenedores. Un `docker inspect` por servicio
# tardaba mas de un minuto y este script corre cada cinco.
declare -A IMAGEN=()
while IFS=$'\t' read -r nombre img; do
  IMAGEN["$nombre"]="$img"
done < <(docker ps --format '{{.Names}}'$'\t''{{.Image}}' 2>/dev/null)

mkdir -p "$METRICS_DIR" 2>/dev/null || {
  echo "AVISO: no se pudo escribir en $METRICS_DIR" >&2
  exit 0
}
tmp="$(mktemp "${DESTINO}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

locales=0 ausentes=0 total=0
{
  echo "# HELP core_force_image_local El servicio corre una imagen compilada en el servidor en vez de la de ECR. 1 = si."
  echo "# TYPE core_force_image_local gauge"
  for s in "${SVCS[@]}"; do
    img="${IMAGEN[app-$s-1]:-}"
    if [[ -z "$img" ]]; then
      ausentes=$((ausentes + 1))
      continue
    fi
    total=$((total + 1))
    case "$img" in
      *.dkr.ecr.*.amazonaws.com/core-force-mail/*) echo "core_force_image_local{servicio=\"$s\"} 0" ;;
      *)
        locales=$((locales + 1))
        echo "core_force_image_local{servicio=\"$s\"} 1"
        ;;
    esac
  done
  echo "# HELP core_force_image_local_total Servicios corriendo una imagen local."
  echo "# TYPE core_force_image_local_total gauge"
  echo "core_force_image_local_total $locales"
  echo "# HELP core_force_image_absent_total Servicios declarados que no tienen contenedor en marcha."
  echo "# TYPE core_force_image_absent_total gauge"
  echo "core_force_image_absent_total $ausentes"
  echo "# HELP core_force_image_services_total Servicios observados."
  echo "# TYPE core_force_image_services_total gauge"
  echo "core_force_image_services_total $total"
  # La marca de tiempo es lo que permite alertar de que la propia vigilancia dejo de correr,
  # que es el modo de fallo silencioso de cualquier trabajo programado.
  echo "# HELP core_force_image_check_timestamp_seconds Ultima vez que se comprobo el origen de las imagenes."
  echo "# TYPE core_force_image_check_timestamp_seconds gauge"
  echo "core_force_image_check_timestamp_seconds $(date +%s)"
} >"$tmp"

chmod 0644 "$tmp"
# Movimiento atomico: node-exporter lee el directorio en cualquier momento y un fichero a
# medias lo descarta entero.
mv -f "$tmp" "$DESTINO"
trap - EXIT

if [[ $locales -gt 0 ]]; then
  echo "$locales servicio(s) corriendo imagen local"
fi
