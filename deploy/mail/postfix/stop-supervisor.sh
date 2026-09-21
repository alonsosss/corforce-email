#!/bin/bash

# Oyente de supervisord: la salida de cualquier proceso detiene el contenedor para que docker lo
# reinicie, salvo la de los auxiliares (el agente de la cola): su fallo no puede llevarse a Postfix.
# Un evento que no se puede leer se trata como critico.
NONCRITICAL=" queue-agent "

printf "READY\n";

while read -r header; do
  payload=""
  len="${header##*len:}"
  if [[ $len =~ ^[0-9]+$ ]] && (( len > 0 )); then
    IFS= read -r -N "$len" payload
  fi
  name=""
  if [[ $payload =~ (^|[[:space:]])processname:([^[:space:]]+) ]]; then
    name="${BASH_REMATCH[2]}"
  fi
  if [[ -n $name && $NONCRITICAL == *" $name "* ]]; then
    echo "Ignoring event of non-critical process $name: $header" >&2;
    printf "RESULT 2\nOKREADY\n";
    continue
  fi
  echo "Processing Event: $header" >&2;
  kill -3 $(cat "/var/run/supervisord.pid")
done < /dev/stdin
