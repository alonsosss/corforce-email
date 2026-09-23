#!/usr/bin/env bash
# Espera a que los servicios recien recreados queden sanos y, si alguno no, falla con la causa.
#
#   ops/maintenance/esperar-sanos.sh [--proyecto app] [--plazo 180] [--estable 20] servicio...
#
# Por que existe: el despliegue comprobaba que cada contenedor corriera la imagen nueva, no que
# arrancara. Un servicio que se niega a arrancar por configuracion (una clave que el .env del
# servidor no tiene, un userlist.txt que falta) queda reiniciandose en bucle con la imagen
# correcta, y el despliegue se daba por bueno. Aqui se espera al estado real: sano si la imagen
# declara HEALTHCHECK y, si no, corriendo sin reiniciarse durante --estable segundos. Al vencer
# el plazo se muestran el estado y las ultimas lineas de registro de cada uno que no llego. Un
# trabajo de arranque (restart: "no", como minio-init) cuenta como bueno al salir con 0 y como fallo,
# sin esperar al plazo, al salir con otro codigo.
set -uo pipefail

PROYECTO=app
PLAZO=180
ESTABLE=20
PAUSA="${ESPERAR_SANOS_PAUSA:-3}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --proyecto) PROYECTO="$2"; shift 2 ;;
    --plazo) PLAZO="$2"; shift 2 ;;
    --estable) ESTABLE="$2"; shift 2 ;;
    --) shift; break ;;
    -*) echo "esperar-sanos: argumento desconocido: $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
[[ $# -gt 0 ]] || { echo "uso: $0 [--proyecto app] [--plazo 180] [--estable 20] servicio..." >&2; exit 2; }

# Por etiqueta de compose y no por nombre: Docker renombra el contenedor cuando una recreacion
# se cruza consigo misma.
contenedor() {
  docker ps -a --filter "label=com.docker.compose.service=$1" --filter "label=com.docker.compose.project=$PROYECTO" \
    --format '{{.ID}}' 2>/dev/null | head -1
}
estado() {
  docker inspect --format '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}sin-chequeo{{end}} {{.RestartCount}} {{.HostConfig.RestartPolicy.Name}} {{.State.ExitCode}}' "$1" 2>/dev/null
}

# Un trabajo de arranque (restart: "no", como minio-init) no queda corriendo: su exito es salir con 0.
# Solo esa politica: un servicio con restart always que sale con 0 sigue siendo un servicio caido.
declare -A desde reinicios
pendientes=("$@")
fallidos=()
fin=$(($(date +%s) + PLAZO))
while :; do
  quedan=()
  ahora=$(date +%s)
  for s in "${pendientes[@]}"; do
    id=$(contenedor "$s")
    if [[ -z "$id" ]]; then quedan+=("$s"); continue; fi
    read -r st salud rc politica salida <<<"$(estado "$id")"
    if [[ "$st" == exited && "$politica" == no ]]; then
      if [[ "$salida" == 0 ]]; then
        echo "  $s: trabajo completado"
      else
        fallidos+=("$s")
      fi
      continue
    fi
    if [[ "$st" == running && "$salud" == healthy ]]; then
      echo "  $s: sano"
      continue
    fi
    if [[ "$st" == running && "$salud" == sin-chequeo ]]; then
      if [[ "${reinicios[$s]:-}" != "$rc" ]]; then reinicios[$s]=$rc; desde[$s]=$ahora; fi
      if ((ahora - ${desde[$s]} >= ESTABLE)); then
        echo "  $s: corriendo sin reiniciarse ${ESTABLE}s (su imagen no declara chequeo)"
        continue
      fi
    fi
    quedan+=("$s")
  done
  pendientes=("${quedan[@]}")
  ((${#pendientes[@]} == 0 && ${#fallidos[@]} == 0)) && { echo "esperar-sanos: todos arrancaron"; exit 0; }
  ((${#pendientes[@]} == 0)) && break
  (($(date +%s) >= fin)) && break
  sleep "$PAUSA"
done

((${#fallidos[@]} > 0)) && echo "esperar-sanos: trabajos que salieron con error: ${fallidos[*]}" >&2
((${#pendientes[@]} > 0)) && echo "esperar-sanos: tras ${PLAZO}s no arrancaron: ${pendientes[*]}" >&2
for s in "${fallidos[@]}" "${pendientes[@]}"; do
  id=$(contenedor "$s")
  if [[ -z "$id" ]]; then
    echo "--- $s: no hay contenedor en el proyecto $PROYECTO" >&2
    continue
  fi
  echo "--- $s: $(estado "$id")" >&2
  docker logs --tail 20 "$id" 2>&1 | sed 's/^/    /' >&2
done
exit 1
