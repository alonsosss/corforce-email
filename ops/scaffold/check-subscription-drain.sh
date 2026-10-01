#!/usr/bin/env bash
# Los consumidores de NATS se paran con events.DrainSubscriptions, nunca con un .Drain() suelto.
#
# Por que: Drain de nats.go vuelve en el acto y la baja sigue por detras. Lo que el servidor empujaba a
# un consumidor durable durante la baja quedaba sin confirmar y no se reentregaba hasta AckWait (90 s):
# en cada despliegue, eventos con minuto y medio de retraso. Lo destapo el 2026-10-01 la prueba del
# rastro de audit, que fallaba una de cada quince veces. events.DrainSubscriptions espera a que cada
# suscripcion termine sus mensajes en vuelo (pkg/events/drain.go).
#
# Comprueba que ningun .go de services/ ni de pkg/ (fuera de las pruebas y de pkg/events/drain.go)
# llama a .Drain(). Y demuestra que muerde con un arbol de prueba.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

# revisar <raiz>: escribe un FALLA por llamada y sale 1 si hay alguna.
revisar() {
  local raiz="$1" hallazgos
  hallazgos="$(cd "$raiz" && grep -rn --include='*.go' -E '\.Drain\(\)' services pkg 2>/dev/null |
    grep -v '_test\.go:' | grep -v '^pkg/events/drain\.go:' || true)"
  [[ -z "$hallazgos" ]] && return 0
  sed 's/^/  FALLA: Drain suelto (usa events.DrainSubscriptions): /' <<<"$hallazgos"
  return 1
}

echo "== Consumidores de NATS parados con events.DrainSubscriptions =="
FAIL=0
revisar "$ROOT" || FAIL=1

mkdir -p "$TMP/services/x" "$TMP/pkg/events"
printf 'package events\n\nfunc f(s interface{ Drain() error }) { _ = s.Drain() }\n' >"$TMP/pkg/events/drain.go"
printf 'package x\n\nfunc g(s interface{ Drain() error }) { _ = s.Drain() }\n' >"$TMP/services/x/consumer_test.go"
if ! revisar "$TMP" >/dev/null; then
  echo "  FALLA: el guardarrail rechaza el helper o una prueba"; FAIL=1
fi
printf 'package x\n\nfunc h(s interface{ Drain() error }) { _ = s.Drain() }\n' >"$TMP/services/x/consumer.go"
if revisar "$TMP" >"$TMP/salida"; then
  echo "  FALLA: el guardarrail no detecta un Drain suelto en un servicio"; FAIL=1
elif ! grep -q 'services/x/consumer.go:3' "$TMP/salida"; then
  echo "  FALLA: el guardarrail no dice donde esta el Drain"; FAIL=1
fi

[[ $FAIL -eq 0 ]] && echo "  OK: ningun consumidor se para con un Drain suelto; la mutacion se detecta."
exit $FAIL
