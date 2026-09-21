#!/usr/bin/env bash
# Vulnerabilidades conocidas en las imagenes de los motores de correo (plan de mejoras, B3 y B4).
#
# Los motores reciben sus parches de seguridad al reconstruir la imagen (deploy/mail/UPSTREAM.md,
# seccion 9). Este guion construye cada imagen de deploy/mail/<motor> y la escanea con Trivy: solo lo
# que tiene arreglo publicado (--ignore-unfixed) y solo gravedad ALTA o CRITICA, que es lo que hay que
# actuar (parche critico en 72 horas, importante en 14 dias). Construye con --pull: una base fresca es lo
# que recoge los parches del sistema publicados desde la ultima vez. Nunca despliega nada ni escribe en el
# repositorio.
#
#   escanear-motores.sh escanear [<carpeta de salida>] [<motor>...]
#       construye y escanea; deja trivy-<motor>.json en la carpeta y escribe el informe en la salida
#       estandar. Necesita docker y red. Sin motores, todos los que tienen Dockerfile.
#   escanear-motores.sh informe <carpeta con trivy-*.json>
#       solo el informe, sin red: es lo que prueba ops/scaffold/check-motor-scan.sh.
#
# La primera linea del informe es "CORREGIBLES: <criticas> <altas>" para que un flujo decida si abre
# una incidencia. El informe cuenta cada CVE una vez por imagen y paquete.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TRIVY_IMAGE="${TRIVY_IMAGE:-ghcr.io/aquasecurity/trivy:0.74.0}"
MAX_POR_IMAGEN="${MAX_POR_IMAGEN:-15}"

falla() { echo "escanear-motores: $*" >&2; exit 1; }

informe() {
  local dir="$1" f motor n_crit n_alta total_crit=0 total_alta=0 tabla="" detalle=""
  compgen -G "$dir/trivy-*.json" >/dev/null || falla "no hay trivy-*.json en $dir"
  for f in "$dir"/trivy-*.json; do
    motor="$(basename "$f" .json)"; motor="${motor#trivy-}"
    jq -e . "$f" >/dev/null 2>&1 || falla "$f no es un JSON de Trivy"
    n_crit="$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity=="CRITICAL")] | unique_by(.VulnerabilityID + .PkgName) | length' "$f")"
    n_alta="$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity=="HIGH")] | unique_by(.VulnerabilityID + .PkgName) | length' "$f")"
    total_crit=$((total_crit + n_crit)); total_alta=$((total_alta + n_alta))
    tabla+="| \`$motor\` | $n_crit | $n_alta |"$'\n'
    if (( n_crit + n_alta > 0 )); then
      detalle+="### $motor"$'\n\n'"| Gravedad | CVE | Paquete | Instalada | Corregida en |"$'\n'"|---|---|---|---|---|"$'\n'
      detalle+="$(jq -r --argjson max "$MAX_POR_IMAGEN" '
        [.Results[]?.Vulnerabilities[]? | select(.Severity=="CRITICAL" or .Severity=="HIGH")]
        | unique_by(.VulnerabilityID + .PkgName)
        | sort_by(if .Severity=="CRITICAL" then 0 else 1 end, .VulnerabilityID)
        | .[:$max][]
        | "| \(.Severity) | \(.VulnerabilityID) | \(.PkgName) | \(.InstalledVersion) | \(.FixedVersion // "-") |"' "$f")"$'\n\n'
    fi
  done
  echo "CORREGIBLES: $total_crit $total_alta"
  echo "## Vulnerabilidades con arreglo en las imagenes de los motores"
  echo
  echo "Trivy (\`$TRIVY_IMAGE\`), gravedad critica o alta y solo con version corregida publicada."
  echo "Plazos propuestos: critico en 72 horas, importante en 14 dias (deploy/mail/UPSTREAM.md, seccion 9)."
  echo
  echo "| Motor | Criticas | Altas |"
  echo "|---|---|---|"
  printf '%s' "$tabla"
  echo
  if [[ -n "$detalle" ]]; then
    echo "$detalle"
    echo "Se muestran como mucho $MAX_POR_IMAGEN por imagen, las criticas primero."
    echo "Como se corrige: reconstruir la imagen (\`scripts/deploy-mail.sh\`, de un motor a la vez) o subir la"
    echo "version fijada del motor; la tabla de origen de cada version esta en UPSTREAM.md."
  else
    echo "Ninguna vulnerabilidad critica ni alta con arreglo publicado."
  fi
}

escanear() {
  local salida="${1:-$(mktemp -d)}"; shift || true
  local motores=("$@") m
  command -v docker >/dev/null || falla "falta docker"
  command -v jq >/dev/null || falla "falta jq"
  mkdir -p "$salida"
  if [[ ${#motores[@]} -eq 0 ]]; then
    mapfile -t motores < <(cd "$ROOT/deploy/mail" && ls -d */ | tr -d / | while read -r d; do [[ -f "$d/Dockerfile" ]] && echo "$d"; done)
  fi
  for m in "${motores[@]}"; do
    [[ -f "$ROOT/deploy/mail/$m/Dockerfile" ]] || falla "el motor '$m' no tiene Dockerfile en deploy/mail"
    echo ">> construyendo $m" >&2
    docker build -q --pull -t "cfm-scan/$m" "$ROOT/deploy/mail/$m" >/dev/null
    echo ">> escaneando $m" >&2
    docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v trivy-cache-cfm:/root/.cache/ "$TRIVY_IMAGE" \
      image --quiet --severity CRITICAL,HIGH --ignore-unfixed --format json "cfm-scan/$m" > "$salida/trivy-$m.json"
  done
  informe "$salida"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  escanear) escanear "$@" ;;
  informe) [[ $# -eq 1 ]] || falla "uso: informe <carpeta con trivy-*.json>"; informe "$1" ;;
  *) falla "uso: escanear-motores.sh escanear [<carpeta>] [<motor>...] | informe <carpeta>" ;;
esac
