#!/usr/bin/env bash
# Comprueba FUERA del servidor un informe de anclas de auditoria recibido por correo
# (docs/adr/0006, seccion 8): la firma HMAC del bloque con la llave de la cadena y, si se le da
# la base de la empresa, que la cadena actual siga conteniendo cada ancla.
#
# Se ejecuta desde un equipo con la llave (AUDIT_HASH_KEY, y AUDIT_HASH_KEYS_OLD si el informe
# se firmo con una retirada), sacada del respaldo de secretos, NUNCA del servidor que se
# sospecha comprometido. Compila el subcomando del propio servicio, asi que el formato del
# informe y el anillo de llaves son los mismos que los del codigo que lo envio.
#
# Uso (con la llave exportada en el entorno, nunca en la linea de ordenes):
#   ops/security/verificar-ancla.sh correo.eml
#   ops/security/verificar-ancla.sh correo.eml --dsn 'postgres://...@host/mail_tenant_acme' [--tenant acme]
#
# Salida: 0 el informe es autentico y la cadena lo contiene; 1 evidencia de manipulacion (firma
# invalida, cabeza por detras del ancla o hash distinto en su posicion); 2 no se pudo comprobar.
# Procedimiento completo en docs/Operacion_Despliegue.md (Ancla externa).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
command -v go >/dev/null || { echo "verificar-ancla: falta go (compila el verificador del repositorio)" >&2; exit 2; }
if [[ $# -lt 1 ]]; then
  echo "uso: $0 <correo.eml | texto del informe> [--dsn DSN [--tenant id|slug]]" >&2
  exit 2
fi

cd "$ROOT"
exec go run ./services/audit verificar-ancla "$@"
