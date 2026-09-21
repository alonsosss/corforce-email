#!/usr/bin/env bash
# Guardarrail: el ejecutor de la migracion de buzones (deploy/mail/migration-runner) compila, pasa go
# vet y sus pruebas con el detector de carreras, y sigue siendo lo que dice ser.
#
# Por que: el ejecutor recibe la contrasena de un buzon AJENO y habla con servidores que no controlamos.
# Lo que lo hace aceptable es lo que NO puede hacer, y eso tiene que romper la CI si cambia: no depende de
# nada fuera de la biblioteca estandar (ni base de datos, ni Redis, ni NATS, ni el kernel pkg/ del
# repositorio), no lanza un shell ni "sh -c", solo ejecuta imapsync y su propio binario (el filtro
# antivirus), no pasa ninguna opcion que borre correo del origen, no pone contrasenas en la linea de
# ordenes ni en el entorno, y verifica siempre el certificado de los dos servidores.
#
# Es un modulo aparte (deploy/mail/migration-runner/go.mod) para poder compilarse dentro del contexto de
# su Dockerfile; el go test del repositorio no lo alcanza, asi que lo corre este script.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="$ROOT/deploy/mail/migration-runner"
FAIL=0
falla() { echo "  FALLA: $*"; FAIL=1; }

mapfile -t FUENTES < <(ls "$DIR"/*.go | grep -v '_test\.go$')

if grep -qE '^require|^\s*github.com/|^\s*golang.org/' "$DIR/go.mod"; then
  falla "migration-runner no puede depender de nada fuera de la biblioteca estandar (go.mod tiene require)"
fi
if grep -nE '"(/bin/)?(ba)?sh"|"-c"|exec\.Command(Context)?\([^,]*,\s*"(/bin/)?(ba)?sh' "${FUENTES[@]}"; then
  falla "migration-runner no puede lanzar un shell"
fi
# Lo unico que ejecuta el ejecutor es imapsync (la ruta de configuracion): el filtro antivirus lo lanza
# imapsync con --pipemess, no el ejecutor.
if grep -n 'exec\.Command' "${FUENTES[@]}" | grep -vE 'exec\.CommandContext\(ctx, cfg\.ImapsyncBin,'; then
  falla "migration-runner solo puede ejecutar imapsync (cfg.ImapsyncBin)"
fi
if grep -nE -- '--delete|--expunge|--nodry|--move|--remove' "$DIR/imapsync.go"; then
  falla "migration-runner no puede pasar a imapsync opciones que borren o muevan correo del origen"
fi
if grep -nE -- '--password[12]|IMAPSYNC_PASSWORD|--authuser|--oauth' "${FUENTES[@]}"; then
  falla "las contrasenas van a imapsync solo por --passfile"
fi
for regla in 'SSL_verify_mode=1' 'SSL_verifycn_name=' '--nosslcheck' '--passfile1=' '--passfile2=' '--pipemess='; do
  grep -qF -- "$regla" "$DIR/imapsync.go" || falla "imapsync.go ya no pasa $regla"
done
if grep -nE 'InsecureSkipVerify|SSL_verify_mode=0|SSL_verify_mode=None' "$DIR"/*.go | grep -v _test.go; then
  falla "no se puede desactivar la verificacion de certificados"
fi
if grep -nE 'log\.[A-Za-z]+\(.*(Password|password|Pass\b|RunnerKey)' "${FUENTES[@]}"; then
  falla "un registro incluye una contrasena o la clave del ejecutor"
fi

if command -v go >/dev/null 2>&1; then
  modulo="$(cd "$DIR" && go list -m)"
  ajenos="$(cd "$DIR" && go list -deps -test -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./... | grep -v "^$modulo" | sort -u)"
  if [[ -n "$ajenos" ]]; then
    falla "migration-runner importa paquetes que no son de la biblioteca estandar: $(echo "$ajenos" | tr '\n' ' ')"
  fi
  if ! (cd "$DIR" && go vet ./... && go test -race -count=1 ./...); then
    falla "vet o pruebas del ejecutor de migracion"
  fi
else
  falla "go no esta en el PATH: no se pueden correr las pruebas del ejecutor de migracion"
fi

[[ $FAIL -eq 0 ]] || exit 1
echo "  OK: el ejecutor de migracion compila, pasa vet y pruebas, sin dependencias, sin shell y verificando los certificados."
