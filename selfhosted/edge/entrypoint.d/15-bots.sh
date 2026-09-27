#!/bin/sh
# Traduce ai-crawlers.txt al mapa $edge_bot_ia de nginx: vale 1 cuando el User-Agent contiene uno
# de los agentes de la lista, y el servidor del host publico responde 403 antes de tocar el gateway
# (docs/Plan_Proteccion_Frente_a_Bots.md, capa 1). robots.txt es una peticion; esto es el bloqueo.
#
# Cada nombre se escapa antes de convertirse en expresion regular y solo se admiten letras, digitos,
# punto, guion, guion bajo y espacio: una linea con otra cosa impide arrancar, porque un caracter
# de regex colado ("." sin escapar, un "|") ampliaria el bloqueo a quien no toca. Una lista vacia
# tambien impide arrancar: es que se perdio el fichero, no que no haya rastreadores.
set -eu

origen="${EDGE_AI_CRAWLERS_FILE:-/etc/core-force-mail/edge/ai-crawlers.txt}"
destino=/etc/nginx/conf.d/05-bots.conf

[ -r "$origen" ] || { echo "edge: no se puede leer $origen" >&2; exit 1; }

n=0
{
  echo "# Generado al arrancar desde $origen; no editar."
  echo "map \$http_user_agent \$edge_bot_ia {"
  echo "  default 0;"
  while IFS= read -r linea || [ -n "$linea" ]; do
    linea=$(printf '%s' "$linea" | sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
    [ -z "$linea" ] && continue
    if ! printf '%s' "$linea" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._ -]{1,63}$'; then
      echo "edge: agente no valido en $origen: '$linea'" >&2
      exit 1
    fi
    # Escapa el punto (el unico caracter de regex admitido) y cita la clave: el espacio de
    # "Kangaroo Bot" lo exige.
    patron=$(printf '%s' "$linea" | sed -e 's/[.]/\\./g')
    echo "  \"~*$patron\" 1;"
    n=$((n + 1))
  done <"$origen"
  echo "}"
} >"$destino"
[ "$n" -gt 0 ] || { rm -f "$destino"; echo "edge: $origen no tiene ningun agente" >&2; exit 1; }
echo "edge: $n rastreadores de IA bloqueados por User-Agent"
