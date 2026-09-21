#!/usr/bin/env bash
# Guardarrail: el agente de la cola de Postfix (deploy/mail/postfix/queue-agent) compila, pasa go vet y sus
# pruebas con el detector de carreras, y sigue siendo lo que dice ser.
#
# Por que: el agente corre como root dentro del contenedor de Postfix porque postsuper solo lo admite del
# superusuario. Lo que lo hace aceptable es lo que NO puede hacer, y eso tiene que romper la CI si cambia:
# no depende de nada fuera de la biblioteca estandar (su modulo propio no tiene require), no lanza un shell
# ni usa "sh -c", y ningun comando lleva argumentos que no sean el identificador validado.
#
# Es un modulo aparte (deploy/mail/postfix/queue-agent/go.mod) para poder compilarse dentro del contexto
# del Dockerfile de Postfix; el go test del repositorio no lo alcanza, asi que lo corre este script.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="$ROOT/deploy/mail/postfix/queue-agent"
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

if grep -qE '^require|^\s*github.com/|^\s*golang.org/' "$DIR/go.mod"; then
  falla "queue-agent no puede depender de nada fuera de la biblioteca estandar (go.mod tiene require)"
fi
if grep -nE '"(/bin/)?(ba)?sh"|"-c"|exec\.Command\("(/bin/)?(ba)?sh' "$DIR"/*.go | grep -v _test.go; then
  falla "queue-agent no puede lanzar un shell"
fi
# Los binarios que ejecuta son dos rutas absolutas fijas.
if grep -n 'exec\.Command' "$DIR"/queue.go | grep -vE 'q\.(postqueue|postsuper)'; then
  falla "queue-agent solo puede ejecutar postqueue y postsuper por sus campos de Queue"
fi

if command -v go >/dev/null 2>&1; then
  if ! (cd "$DIR" && go vet ./... && go test -race -count=1 ./...); then
    falla "vet o pruebas del agente de la cola"
  fi
else
  falla "go no esta en el PATH: no se pueden correr las pruebas del agente de la cola"
fi

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: el agente de la cola compila, pasa vet y pruebas, sin dependencias ni shell."
