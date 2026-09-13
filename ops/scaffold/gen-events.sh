#!/usr/bin/env bash
# Genera docs/arquitectura/EVENTS.md: el registro de subjects NATS publicados y consumidos
# por servicio, extraido del codigo. Base para versionar subjects sin roturas y para ver el
# acoplamiento por eventos. Regenerar tras cambios: make gen-events.
#
# Lo produce ops/scaffold/eventcontracts, el mismo analisis que fija los payloads de
# EVENT-CONTRACTS.md (que se regenera a la vez para que los dos no discrepen): cubre lo
# publicado en el bus y lo encolado en la outbox (outbox.Enqueue), con subjects literales,
# constantes o recibidos por parametro. Una publicacion cuyo subject no se puede resolver
# aborta la generacion en lugar de quedar fuera del registro.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
exec go run ./ops/scaffold/eventcontracts
