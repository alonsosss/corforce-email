#!/usr/bin/env bash
# Activa el escaneo de vulnerabilidades de ECR sobre las imagenes publicadas.
#
# Complementa a govulncheck, no lo repite: govulncheck mira NUESTRO codigo Go y sus
# dependencias; esto mira la imagen entera, incluido lo que trae la base (nginx, alpine)
# y lo que se instale por fuera del modulo Go. Las imagenes `FROM scratch` no tienen
# paquetes de sistema y saldran limpias; la de la aplicacion web, que corre sobre nginx,
# es la que este escaneo cubre de verdad.
#
# Requiere la politica core-force-mail-ecr-scanning, que ops/aws/setup-iam.sh concede al rol de
# la instancia (core-force-mail-ec2-role); tambien sirve una credencial de administrador.
#
# Uso:
#   ops/ecr/enable-scanning.sh            # activa escaneo continuo
#   ops/ecr/enable-scanning.sh --report   # solo informa los hallazgos actuales
set -uo pipefail

REGION="${AWS_REGION:-us-east-1}"
NAMESPACE="${ECR_NAMESPACE:-core-force-mail}"

if ! command -v aws >/dev/null 2>&1; then
  echo "FALLA: falta el AWS CLI" >&2; exit 1
fi

if [[ "${1:-}" != "--report" ]]; then
  # ENHANCED escanea de forma continua y vuelve a evaluar las imagenes ya publicadas
  # cuando aparece un CVE nuevo. BASIC solo mira una vez, al subir: una imagen que era
  # limpia ayer y vulnerable hoy no se entera nadie.
  if ! aws ecr put-registry-scanning-configuration \
        --region "$REGION" \
        --scan-type ENHANCED \
        --rules '[{"scanFrequency":"CONTINUOUS_SCAN","repositoryFilters":[{"filter":"'"$NAMESPACE"'/","filterType":"WILDCARD"}]}]' \
        >/dev/null; then
    echo "FALLA: no se pudo configurar el escaneo. Revisa que el rol tenga la politica" >&2
    echo "       core-force-mail-ecr-scanning (ops/aws/setup-iam.sh)." >&2
    exit 1
  fi
  echo "Escaneo continuo activado para $NAMESPACE/* en $REGION"
fi

echo
echo "Hallazgos por repositorio (imagen 'latest'):"
aws ecr describe-repositories --region "$REGION" \
    --query "repositories[?starts_with(repositoryName, '$NAMESPACE/')].repositoryName" \
    --output text 2>/dev/null | tr '\t' '\n' | while read -r repo; do
  [[ -z "$repo" ]] && continue
  counts="$(aws ecr describe-image-scan-findings --region "$REGION" \
              --repository-name "$repo" --image-id imageTag=latest \
              --query 'imageScanFindings.findingSeverityCounts' --output json 2>/dev/null)"
  case "$counts" in
    ''|'null'|'{}') continue ;;   # sin escanear todavia, o sin hallazgos
  esac
  echo "  $repo: $counts"
done
echo
echo "El escaneo continuo tarda en poblar resultados; vuelve a correr --report mas tarde."
