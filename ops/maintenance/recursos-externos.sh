#!/usr/bin/env bash
# Comprueba que existen las redes y los volumenes que un compose declara externos.
#
#   ops/maintenance/recursos-externos.sh -f docker-compose.yml [-f otro.yml ...]
#
# Por que existe: la plataforma usa la red mail-engines y, en el perfil autoalojado, el volumen de
# certificados de acme, y los dos son de otro proyecto (deploy/mail). Si no existen, `up` falla
# servicio a servicio a mitad de la recreacion y deja la plataforma a medias. Se comprueba antes
# de tocar ningun contenedor. Los nombres salen del propio compose ya interpolado, no de una lista
# aqui. Salida 1 si falta alguno, con su nombre; nunca crea nada: crearlo con otras etiquetas o
# con otra subred que la de su dueno lo dejaria incompatible con el compose que lo declara.
set -euo pipefail

[[ $# -gt 0 ]] || { echo "uso: $0 -f <compose> [-f <compose>...]" >&2; exit 2; }

config="$(docker compose "$@" config --format json)" || {
  echo "recursos-externos: docker compose no pudo leer la configuracion" >&2
  exit 2
}

faltan=0
while IFS=$'\t' read -r tipo nombre; do
  [[ -z "$tipo" ]] && continue
  if ! docker "$tipo" inspect "$nombre" >/dev/null 2>&1; then
    echo "recursos-externos: falta $( [[ "$tipo" == network ]] && echo la red || echo el volumen ) externo $nombre" >&2
    faltan=1
  fi
done < <(python3 -c '
import json, sys
d = json.load(sys.stdin)
for clave, tipo in (("networks", "network"), ("volumes", "volume")):
    for k, v in sorted((d.get(clave) or {}).items()):
        if (v or {}).get("external"):
            print(tipo + "\t" + (v.get("name") or k))
' <<<"$config")

exit "$faltan"
