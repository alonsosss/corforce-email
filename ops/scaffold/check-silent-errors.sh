#!/usr/bin/env bash
# Detecta los 500 que no dejan constancia del motivo.
#
# El patron aparecia en casi todos los servicios: writeError traducia los errores de dominio
# conocidos y mandaba el resto a response.ErrInternal(w), sin registrar nada. Un fallo asi
# obliga a reproducir el caso para saber que paso, y hay casos que no se reproducen: una
# restriccion de la base con datos concretos, una integracion que responde distinto de lo
# esperado, una credencial sin habilitar.
#
# Durante una sola auditoria del modulo de produccion, cuatro servicios distintos escondieron
# asi un error real: una columna JSON mal alimentada que hacia imposible crear una zona, una
# restriccion sobre el tipo de componente que impedia guardar una receta, un token rechazado
# por un proveedor externo y un buzon sin cuota. Ninguno se veia desde fuera.
#
# Regla: en el caso por defecto de un writeError, y en cualquier ErrInternal que responda a
# un error con nombre, usa response.Unexpected(w, err), que registra y responde lo mismo.
# El mensaje al cliente NO cambia: el detalle de un fallo interno no es suyo.
#
# Excepciones justificadas: ops/scaffold/silent-errors-allowlist.txt (una linea por
# coincidencia reportada, con el motivo al lado en un comentario aparte).
set -euo pipefail
cd "$(dirname "$0")/../.."

ALLOWLIST=ops/scaffold/silent-errors-allowlist.txt

# Solo interesa el DEFAULT de un writeError: ahi es donde caen los errores sin clasificar.
# Un ErrInternal suelto tras validar una entrada no esconde nada, porque no hay error que
# registrar.
hits=$(awk '
  /func writeError\(/ { infn=1 }
  infn && /^\tdefault:/ { indefault=1; next }
  indefault && /response\.ErrInternal\(w\)/ {
    print FILENAME ": el default de writeError responde 500 sin registrar el motivo"
    indefault=0; infn=0; next
  }
  indefault && /[^ \t]/ { indefault=0 }
  infn && /^\}/ { infn=0 }
' $(find services -name handler.go -path '*/adapters/http/*' | sort) 2>/dev/null || true)

if [ -s "$ALLOWLIST" ]; then
  hits=$(printf '%s\n' "$hits" | grep -vFf "$ALLOWLIST" || true)
fi
hits=$(printf '%s\n' "$hits" | sed '/^$/d')

if [ -n "$hits" ]; then
  {
    echo "500 sin motivo registrado. Usa response.Unexpected(w, err) en el default de"
    echo "writeError: responde lo mismo al cliente y deja escrito que fallo."
    echo
    printf '%s\n' "$hits"
  } >&2
  exit 1
fi
echo "check-silent-errors: OK"
