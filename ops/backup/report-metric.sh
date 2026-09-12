#!/usr/bin/env bash
# Publica el resultado de un trabajo programado como metrica, para que su ausencia se note.
#
#   . ops/backup/report-metric.sh
#   cf_publicar_metrica respaldo 0 7      # trabajo, codigo de salida, bases tratadas
#
# El respaldo y su prueba de restauracion no viven en un contenedor, asi que no aparecen en
# ningun panel: que dejen de correr se ve exactamente igual que si corrieran. La unica huella
# es un log que nadie abre mientras todo funciona, y para cuando se abre ya hace falta el
# respaldo.
#
# Se escribe un fichero .prom que el recolector de node-exporter lee. Lo que vigila la alerta
# NO es el codigo de salida sino la MARCA DE TIEMPO del ultimo exito: un trabajo que fallo
# avisa, y uno que dejo de ejecutarse -temporizador desactivado, servidor reinstalado, cron
# vaciado por error- tambien, que es el caso silencioso.
#
# Sin `set -e`: se sourcea.

# Vive dentro del arbol de la aplicacion porque el usuario que corre los trabajos no tiene
# root: /var/lib/node_exporter, que es lo habitual, no lo puede crear. node-exporter lo
# recibe como un montaje propio.
CF_METRICS_DIR="${CF_METRICS_DIR:-/opt/core-force-mail/metrics}"

# cf_publicar_metrica <trabajo> <codigo_salida> [unidades_tratadas]
cf_publicar_metrica() {
  local trabajo="$1" codigo="$2" unidades="${3:-0}" ahora
  ahora="$(date +%s)"

  if [[ ! -d "$CF_METRICS_DIR" ]]; then
    mkdir -p "$CF_METRICS_DIR" 2>/dev/null || {
      # No poder publicar la metrica no puede tumbar el respaldo: es peor quedarse sin copia
      # que sin vigilancia.
      echo "  AVISO: no se pudo escribir la metrica en $CF_METRICS_DIR" >&2
      return 0
    }
  fi

  local destino="$CF_METRICS_DIR/core_force_$trabajo.prom"
  local tmp
  tmp="$(mktemp "${destino}.XXXXXX" 2>/dev/null)" || return 0

  {
    echo "# HELP core_force_job_last_run_timestamp_seconds Ultima vez que el trabajo se ejecuto, con exito o sin el."
    echo "# TYPE core_force_job_last_run_timestamp_seconds gauge"
    echo "core_force_job_last_run_timestamp_seconds{trabajo=\"$trabajo\"} $ahora"
    echo "# HELP core_force_job_last_exit_code Codigo de salida de la ultima ejecucion. Cero es exito."
    echo "# TYPE core_force_job_last_exit_code gauge"
    echo "core_force_job_last_exit_code{trabajo=\"$trabajo\"} $codigo"
    echo "# HELP core_force_job_units Unidades tratadas en la ultima ejecucion (bases respaldadas, por ejemplo)."
    echo "# TYPE core_force_job_units gauge"
    echo "core_force_job_units{trabajo=\"$trabajo\"} $unidades"
    if [[ "$codigo" -eq 0 ]]; then
      echo "# HELP core_force_job_last_success_timestamp_seconds Ultima vez que el trabajo termino bien."
      echo "# TYPE core_force_job_last_success_timestamp_seconds gauge"
      echo "core_force_job_last_success_timestamp_seconds{trabajo=\"$trabajo\"} $ahora"
    elif [[ -f "$destino" ]]; then
      # Un fallo no borra la marca del ultimo exito: es lo que dice cuanto hace que no hay
      # copia buena, que es la pregunta real cuando algo va mal.
      grep '^core_force_job_last_success_timestamp_seconds' "$destino" 2>/dev/null
    fi
  } >"$tmp"

  chmod 0644 "$tmp" 2>/dev/null
  # Movimiento atomico: node-exporter lee este directorio en cualquier momento y un fichero
  # a medias se descarta entero.
  mv -f "$tmp" "$destino" 2>/dev/null || rm -f "$tmp"
}
