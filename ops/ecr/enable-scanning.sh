#!/usr/bin/env bash
# Activa el escaneo de vulnerabilidades de ECR sobre las imagenes publicadas.
#
# Complementa a govulncheck, no lo repite: govulncheck mira NUESTRO codigo Go y sus
# dependencias; esto mira la imagen entera, incluido lo que trae la base (nginx, alpine)
# y lo que se instale por fuera del modulo Go. Las imagenes `FROM scratch` no tienen
# paquetes de sistema y saldran limpias; las de los micro-frontends, que corren sobre
# nginx, son las que este escaneo cubre de verdad.
#
# Requiere los permisos de ops/ecr/iam-policy-scanning.json en el rol que lo ejecute.
# El rol de la instancia (core-force-mail-ec2-role) hoy solo puede publicar, no configurar.
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
    echo "FALLA: no se pudo configurar el escaneo. Revisa que el rol tenga" >&2
    echo "       ops/ecr/iam-policy-scanning.json adjunta." >&2
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
