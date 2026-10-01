#!/usr/bin/env bash
# Las imagenes Debian de los motores aplican las actualizaciones de seguridad al construirse.
#
# Por que: la etiqueta debian:<version>-slim de Docker Hub se republica con retraso respecto a
# <version>-security, y `apt-get install` no actualiza lo que la base ya trae. El 2026-10-01 Postfix,
# Rspamd y postfix-tlspol salian con libpcre2 y libssl vulnerables aunque el parche ya estaba
# publicado (incidencia #22): reconstruir no lo recogia. deploy/mail/UPSTREAM.md, seccion 9.
#
# Comprueba que la etapa final de cada deploy/mail/*/Dockerfile basada en debian o ubuntu ejecuta
# `apt-get upgrade` (o dist-upgrade) antes del primer `apt-get install`. Y demuestra que muerde.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

# revisar <Dockerfile>: sale 1 y lo dice si la etapa final Debian no actualiza antes de instalar.
revisar() {
  local f="$1" veredicto
  veredicto="$(awk '
    /^FROM / { base = $2; upg = 0; inst = 0; mal = 0 }
    /apt-get (dist-)?upgrade/ { if (!inst) upg = 1 }
    /apt-get install|apt install/ { if (!inst && !upg) mal = 1; inst = 1 }
    END {
      if (base !~ /^(debian|ubuntu)[:@]/) { print "no-debian"; exit }
      if (!inst) { print "sin-install"; exit }
      print (mal ? "mal" : "ok")
    }' "$f")"
  if [[ "$veredicto" == mal ]]; then
    echo "  FALLA: ${f#"$ROOT"/}: la etapa final Debian instala sin 'apt-get upgrade -y' antes"
    return 1
  fi
  return 0
}

echo "== Actualizaciones de seguridad en las imagenes Debian de los motores =="
FAIL=0
n=0
for f in "$ROOT"/deploy/mail/*/Dockerfile; do
  revisar "$f" || FAIL=1
  n=$((n + 1))
done

cat >"$TMP/Dockerfile" <<'EOF'
FROM golang:1.26-trixie AS builder
RUN apt-get update && apt-get upgrade -y
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates
EOF
if revisar "$TMP/Dockerfile" >/dev/null; then
  echo "  FALLA: el guardarrail acepta una etapa final que instala sin actualizar (el upgrade del builder no cuenta)"; FAIL=1
fi
sed -i 's/apt-get update && apt-get install/apt-get update \&\& apt-get upgrade -y \&\& apt-get install/' "$TMP/Dockerfile"
if ! revisar "$TMP/Dockerfile" >/dev/null; then
  echo "  FALLA: el guardarrail rechaza una etapa final que actualiza antes de instalar"; FAIL=1
fi

[[ $FAIL -eq 0 ]] && echo "  OK: $n Dockerfiles de motores revisados; las etapas Debian actualizan antes de instalar; la mutacion se detecta."
exit $FAIL
