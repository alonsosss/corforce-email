#!/usr/bin/env bash
# Genera la lista de objetivos de Prometheus a partir de docker-compose.yml.
#
# La alternativa habitual es descubrir los contenedores en caliente montando el socket de
# Docker dentro de Prometheus; eso equivale a dar root del host a un contenedor de la red
# de monitoreo, asi que aqui la lista se genera desde el fichero de despliegue y se versiona.
# Como es un artefacto derivado, el CI lo regenera y falla si difiere: un microservicio
# nuevo no puede quedarse sin vigilancia por olvido.
#
# Se incluye un servicio cuando (1) publica un puerto y (2) tiene codigo propio en
# services/<nombre>, que es lo que garantiza que expone /metrics con pkg/server. La
# infraestructura de terceros (Postgres, Redis, NATS) no se instrumenta aqui.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/ops/observability/prometheus/targets.json"

python3 - "$ROOT" "$OUT" <<'PY'
import json
import os
import re
import sys

root, out = sys.argv[1], sys.argv[2]
compose = os.path.join(root, "docker-compose.yml")

# Servicios con directorio propio pero sin exposicion Prometheus: se documentan aqui para
# que su ausencia sea una decision y no un descuido.
SKIP = set()

service = None
targets = []
seen = set()
for line in open(compose, encoding="utf-8"):
    m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
    if m:
        service = m.group(1)
        continue
    # El puerto publicado puede ir atado al bucle local (lo normal) o abierto al
    # exterior, como la pasarela. Mirar solo el primer caso dejaba a la pasarela
    # -- la unica puerta de entrada de la plataforma -- fuera del monitoreo, sin error y sin
    # que nadie lo notara: ni su tasa de error, ni su latencia, ni las denegaciones
    # del control de acceso llegaban a Prometheus.
    m = re.match(r'^\s+- "[^"]*:(\d+)"\s*$', line)
    if not (m and service) or service in seen:
        continue
    if service in SKIP or not os.path.isdir(os.path.join(root, "services", service)):
        continue
    seen.add(service)
    targets.append({
        "targets": [f"{service}:{m.group(1)}"],
        "labels": {"job": "mail-services", "service": service},
    })

targets.sort(key=lambda t: t["labels"]["service"])
os.makedirs(os.path.dirname(out), exist_ok=True)
with open(out, "w", encoding="utf-8") as fh:
    json.dump(targets, fh, indent=2, ensure_ascii=False)
    fh.write("\n")
print(f"{len(targets)} objetivos -> {os.path.relpath(out, root)}")
PY
