#!/usr/bin/env bash
# Prueba de deploy/mail/postfix/stop-supervisor.sh con eventos de supervisord simulados.
#
# Por que: en el contenedor de Postfix, supervisord manda a este oyente cada salida de un proceso, y el
# oyente detiene el contenedor entero: si cae Postfix, el contenedor debe caer para que docker lo
# reinicie. El agente de la cola (queue-agent) es un componente auxiliar con superficie de red y
# ejecucion como root: un fallo suyo (una condicion de carrera, la falta de memoria, un panic) no puede
# llevarse consigo a Postfix ni a la entrega de correo.
#
# Uso: bash ops/scaffold/test-stop-supervisor.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/deploy/mail/postfix/stop-supervisor.sh"
W="$(mktemp -d)"
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

MARCA="$W/quit"
# El "supervisord" de prueba: un proceso que anota SIGQUIT (kill -3), que es como el oyente lo detiene.
env --default-signal=QUIT bash -c "trap 'echo quit > \"$MARCA\"' QUIT; while :; do sleep 0.05; done" &
FAKE=$!
disown "$FAKE"
echo "$FAKE" >"$W/supervisord.pid"
cleanup() { kill -9 "$FAKE" 2>/dev/null; rm -rf "$W"; }
trap cleanup EXIT
sleep 0.3

# evento <proceso> <estado>: el encabezado y la carga tal como los envia supervisord.
evento() {
  local payload="processname:$1 groupname:$1 from_state:RUNNING expected:0 pid:1234"
  printf 'ver:3.0 server:supervisor serial:1 pool:processes poolserial:1 eventname:%s len:%d\n%s' "$2" "${#payload}" "$payload"
}

# El oyente lee el pid de supervisord de su ruta fija: se prueba una copia que apunta al de prueba.
sed "s|/var/run/supervisord.pid|$W/supervisord.pid|g" "$SCRIPT" >"$W/stop-supervisor.sh"

corre() {
  # El oyente responde READY al arrancar y, si ignora el evento, OK y otra vez READY.
  timeout 3 bash "$W/stop-supervisor.sh" 2>"$W/err" >"$W/out"
}

rm -f "$MARCA"
evento queue-agent PROCESS_STATE_EXITED | corre
sleep 0.3
if [[ -e "$MARCA" ]]; then
  falla "la salida de queue-agent detuvo el contenedor: un fallo del agente no puede llevarse a Postfix"
fi
if ! grep -q '^READY' "$W/out"; then
  falla "el oyente no anuncio READY"
fi
if ! grep -q 'RESULT 2' "$W/out"; then
  falla "el oyente no reconocio el evento ignorado (supervisord lo dejaria esperando)"
fi

for estado in PROCESS_STATE_EXITED PROCESS_STATE_FATAL PROCESS_STATE_STOPPED; do
  rm -f "$MARCA"
  evento queue-agent "$estado" | corre
  sleep 0.3
  [[ ! -e "$MARCA" ]] || falla "queue-agent en $estado detuvo el contenedor"
done

for proceso in postfix syslog-ng; do
  rm -f "$MARCA"
  evento "$proceso" PROCESS_STATE_EXITED | corre
  sleep 0.3
  [[ -e "$MARCA" ]] || falla "la salida de $proceso debe detener el contenedor y no lo hizo"
done

# Un evento que no se puede leer se trata como critico: ante la duda, el contenedor cae y docker lo levanta.
rm -f "$MARCA"
printf 'ver:3.0 server:supervisor serial:1 pool:processes poolserial:1 eventname:PROCESS_STATE_EXITED len:5\nbasura' | corre
sleep 0.3
[[ -e "$MARCA" ]] || falla "un evento ilegible debe detener el contenedor"

# Un nombre que solo contiene el del agente no lo es.
rm -f "$MARCA"
evento "xqueue-agent" PROCESS_STATE_EXITED | corre
sleep 0.3
[[ -e "$MARCA" ]] || falla "un proceso llamado xqueue-agent no es el agente y debe detener el contenedor"

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: solo la salida del agente de la cola se ignora; la de cualquier otro proceso detiene el contenedor."
