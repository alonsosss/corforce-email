#!/usr/bin/env bash
# La imagen de unbound resiste un fallo puntual de la red de Alpine al instalarlo desde edge.
#
# Por que: el 2026-10-12 un error TLS al bajar el indice de edge hizo que apk usara el estable, que no
# cumple la version minima fijada, y `make e2e-mail` fallo en el build (incidencia #25). Un fallo de
# red ajeno no se puede provocar, pero si el orden de lo que hace la instruccion RUN: se ejecuta con
# un `apk` y un `sleep` falsos y se comprueba que reintenta, alterna de espejo y, si todo falla,
# hace fallar la construccion en lugar de seguir con otra version.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FAIL=0

# instruccion <Dockerfile>: el RUN que instala desde edge, sin "RUN " y con las continuaciones unidas.
instruccion() {
  sed -e ':a' -e '/\\$/{N;s/\\\n//;ba}' "$1" | grep -E '^RUN .*alpine/edge|^RUN .*apk add .*unbound' | head -1 | sed 's/^RUN //'
}

mkdir -p "$TMP/bin"
cat >"$TMP/bin/sleep" <<'SH'
#!/bin/sh
exit 0
SH
cat >"$TMP/bin/apk" <<'SH'
#!/bin/sh
n=$(cat "$APK_CONTADOR" 2>/dev/null || echo 0)
n=$((n + 1)); echo "$n" >"$APK_CONTADOR"
for a in "$@"; do case "$a" in --repository=*) echo "${a#--repository=}" >>"$APK_REGISTRO";; esac; done
[ "$n" -gt "$APK_FALLOS" ]
SH
chmod +x "$TMP/bin/sleep" "$TMP/bin/apk"

# ejecutar <Dockerfile> <fallos>: corre la instruccion con apk fallando las primeras <fallos> veces.
# Deja el codigo de salida en $SALIDA, los intentos en $INTENTOS y los espejos usados en $ESPEJOS.
ejecutar() {
  local cmd; cmd="$(instruccion "$1")"
  : >"$TMP/registro"; rm -f "$TMP/contador"
  if [[ -z "$cmd" ]]; then SALIDA=sin-instruccion; INTENTOS=0; ESPEJOS=0; return; fi
  PATH="$TMP/bin:$PATH" APK_CONTADOR="$TMP/contador" APK_REGISTRO="$TMP/registro" APK_FALLOS="$2" \
    sh -c "$cmd" >/dev/null 2>&1
  SALIDA=$?
  INTENTOS="$(cat "$TMP/contador" 2>/dev/null || echo 0)"
  ESPEJOS="$(sort -u "$TMP/registro" | wc -l)"
}

verificar() {
  local f="$1" etiqueta="$2"
  ejecutar "$f" 3
  if [[ "$SALIDA" != 0 || "$INTENTOS" != 4 ]]; then
    echo "  FALLA: $etiqueta: con 3 fallos de red no termina instalando al cuarto intento (salida $SALIDA, intentos $INTENTOS)"; return 1
  fi
  if [[ "$ESPEJOS" -lt 2 ]]; then
    echo "  FALLA: $etiqueta: reintenta siempre contra el mismo espejo"; return 1
  fi
  ejecutar "$f" 1000
  if [[ "$SALIDA" == 0 ]]; then
    echo "  FALLA: $etiqueta: si apk nunca instala, la construccion termina bien (debe fallar)"; return 1
  fi
  return 0
}

echo "== Instalacion de unbound desde edge: reintento ante un fallo de red =="
verificar "$ROOT/deploy/mail/unbound/Dockerfile" "deploy/mail/unbound/Dockerfile" || FAIL=1

printf 'FROM alpine:3.23\nRUN apk add --no-cache --repository=https://dl-cdn.alpinelinux.org/alpine/edge/main "unbound>=1.26.1-r0"\n' >"$TMP/sin-reintento"
if verificar "$TMP/sin-reintento" "mutacion" >/dev/null; then
  echo "  FALLA: el guardarrail acepta una instalacion sin reintento"; FAIL=1
fi

[[ $FAIL -eq 0 ]] && echo "  OK: unbound reintenta, alterna de espejo y falla si no instala; la mutacion se detecta."
exit $FAIL
