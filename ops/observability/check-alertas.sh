#!/usr/bin/env bash
# Prueba las reglas de alerta con el marco de pruebas de Prometheus.
#
# Una alerta mal escrita NO falla: se queda callada. No hay pantalla en blanco ni error en
# ningun log; el aviso simplemente no llega el dia que hacia falta, y eso se descubre durante
# el incidente que tenia que haber avisado. `promtool check rules` tampoco lo caza: una
# expresion que no casa nunca carga perfectamente.
#
# Estas pruebas declaran una serie en el tiempo y afirman que alerta sale, con sus etiquetas
# y su texto ya renderizado. Comprobado que pueden fallar: un {{ $labels.service }} donde
# toca {{ $labels.servicio }} deja el aviso sin nombre y la prueba lo caza.
#
# Se ejecuta con la MISMA version de Prometheus que corre en produccion: promtool es parte
# de esa imagen, asi que no hay nada que instalar ni que pueda quedar desalineado.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="$ROOT/ops/observability/prometheus"
IMAGEN="${PROMETHEUS_IMAGE:-prom/prometheus:v3.1.0}"

if ! docker info >/dev/null 2>&1; then
  echo "check-alertas: se omite (docker no disponible)" >&2
  exit 0
fi

mapfile -t PRUEBAS < <(cd "$DIR" && ls tests/*_test.yml 2>/dev/null)
if [[ ${#PRUEBAS[@]} -eq 0 ]]; then
  echo "check-alertas: no hay pruebas en $DIR/tests" >&2
  exit 1
fi

# Primero que las reglas carguen; si no, el error de las pruebas seria confuso.
docker run --rm -v "$DIR:/p" --entrypoint promtool "$IMAGEN" check rules /p/rules/plataforma.yml

for t in "${PRUEBAS[@]}"; do
  docker run --rm -v "$DIR:/p" --entrypoint promtool "$IMAGEN" test rules "/p/$t"
done

# La entrega: la plantilla de Alertmanager renderizada con la MISMA imagen y el mismo script que en produccion
# tiene que ser una configuracion valida, y un valor con comillas o saltos de linea tiene que rechazarse.
AM_IMAGEN="${ALERTMANAGER_IMAGE:-prom/alertmanager:v0.28.1}"
AM_DIR="$ROOT/ops/observability/alertmanager"
am_probar() {
  docker run --rm -v "$AM_DIR:/etc/alertmanager:ro" \
    -e ALERT_EMAIL_TO="$1" -e ALERT_SMTP_HOST=mail.ejemplo.test:587 -e ALERT_SMTP_USER=alertas@ejemplo.test \
    --entrypoint /bin/sh "$AM_IMAGEN" -c "$2"
}
am_probar operadores@ejemplo.test 'echo "clave" >/tmp/smtp.pass && sh /etc/alertmanager/render.sh /etc/alertmanager/alertmanager.yml.tmpl /tmp/am.yml \
  && sed -i "s|/etc/alertmanager-secretos/smtp.pass|/tmp/smtp.pass|" /tmp/am.yml && amtool check-config /tmp/am.yml'
if am_probar "x' ; malo: 1" 'sh /etc/alertmanager/render.sh /etc/alertmanager/alertmanager.yml.tmpl /tmp/am.yml' >/dev/null 2>&1; then
  echo "check-alertas: render.sh acepto un destinatario con comilla" >&2
  exit 1
fi

echo "check-alertas: OK"
