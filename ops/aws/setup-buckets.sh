#!/usr/bin/env bash
# Reconcilia la proteccion de los buckets de la plataforma: versionado, caducidad de
# versiones antiguas y candado de retencion sobre los respaldos.
#
# Los buckets se crearon a mano en su dia (medios, respaldos, flota) y su configuracion
# vivia solo en la consola. El 2026-09-05 se comprobo con el usuario raiz que solo el de
# respaldos versionaba, que ninguno tenia Object Lock, y que un objeto borrado en medios
# desaparecia sin vuelta atras. Aqui queda como codigo, idempotente: correrlo de nuevo
# deja los buckets como dice este archivo.
#
# Coste: ninguno apreciable. El versionado solo guarda copias de lo que se sobreescribe o
# borra, y las versiones no actuales caducan a los 30 dias. El candado no añade
# almacenamiento: los respaldos ya se conservaban 180 dias por ciclo de vida.
#
# Uso:
#   ops/aws/setup-buckets.sh            # aplica
#   ops/aws/setup-buckets.sh --check    # solo informa
set -euo pipefail

CHECK=0
[[ "${1:-}" == "--check" ]] && CHECK=1

command -v aws >/dev/null 2>&1 || { echo "FALLA: falta el AWS CLI" >&2; exit 1; }
ACC="$(aws sts get-caller-identity --query Account --output text)" || {
  echo "FALLA: no hay credenciales AWS validas" >&2; exit 1; }

: "${MEDIA_BUCKET:=cf-media-${ACC}}"
: "${BACKUP_BUCKET:=cf-backups-${ACC}}"
: "${FLEET_BUCKET:=cf-oee-flota-${ACC}}"

# Retencion del candado sobre cada respaldo nuevo. Modo GOBERNANZA, no cumplimiento: el
# usuario raiz puede saltarselo con s3:BypassGovernanceRetention si de verdad hace falta,
# pero el rol del servidor no (no tiene ese permiso ni s3:DeleteObject en respaldos). Es
# exactamente el caso que protege: alguien con el servidor tomado no puede borrar lo que
# permitiria recuperarse. En cumplimiento ni el raiz podria, y un error de retencion se
# pagaria en almacenamiento sin remedio.
: "${BACKUP_LOCK_DAYS:=30}"

echo "Cuenta $ACC"
[[ $CHECK -eq 1 ]] && echo "(modo --check: no se aplica nada)"
echo

paso() {
  local nombre="$1" aldia="$2"; shift 2
  if [[ "$aldia" == "1" ]]; then printf '  %-48s al dia\n' "$nombre"; return 0; fi
  if [[ $CHECK -eq 1 ]]; then printf '  %-48s FALTA\n' "$nombre"; return 0; fi
  "$@"
  printf '  %-48s aplicado\n' "$nombre"
}

versionado_ok() {
  aws s3api get-bucket-versioning --bucket "$1" --query Status --output text 2>/dev/null | grep -q '^Enabled$'
}
activar_versionado() {
  aws s3api put-bucket-versioning --bucket "$1" --versioning-configuration Status=Enabled
}

# Versiones no actuales caducan a los 30 dias; subidas a medias se abortan a los 7. No se
# toca la regla de caducidad que un bucket pueda tener para sus objetos actuales: este
# script la conserva si existe (respaldos: 180 dias) y solo la añade si falta.
ciclo_ok() {
  aws s3api get-bucket-lifecycle-configuration --bucket "$1" --output json 2>/dev/null \
    | grep -q '"NoncurrentDays": 30'
}
poner_ciclo() {
  local b="$1" actual regla
  actual="$(aws s3api get-bucket-lifecycle-configuration --bucket "$b" --output json 2>/dev/null || echo '{"Rules":[]}')"
  regla='{"ID":"versiones-antiguas","Status":"Enabled","Filter":{"Prefix":""},
          "NoncurrentVersionExpiration":{"NoncurrentDays":30},
          "AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}'
  python3 - "$actual" "$regla" > /tmp/ciclo-$$.json <<'PY'
import json, sys
actual = json.loads(sys.argv[1]); nueva = json.loads(sys.argv[2])
reglas = [r for r in actual.get("Rules", []) if r.get("ID") != nueva["ID"]]
# Si otra regla ya trata las versiones no actuales, se respeta y no se duplica.
if any("NoncurrentVersionExpiration" in r for r in reglas):
    for r in reglas:
        if "NoncurrentVersionExpiration" in r:
            r["NoncurrentVersionExpiration"] = {"NoncurrentDays": 30}
else:
    reglas.append(nueva)
print(json.dumps({"Rules": reglas}))
PY
  aws s3api put-bucket-lifecycle-configuration --bucket "$b" --lifecycle-configuration "file:///tmp/ciclo-$$.json" >/dev/null
  rm -f "/tmp/ciclo-$$.json"
}

# --- Medios: lo que suben los usuarios. Sin versionado, un borrado o una sobreescritura
# por error (o por un atacante con el rol) era definitivo.
v=0; versionado_ok "$MEDIA_BUCKET" && v=1
paso "medios: versionado" "$v" activar_versionado "$MEDIA_BUCKET"
c=0; ciclo_ok "$MEDIA_BUCKET" && c=1
paso "medios: versiones antiguas caducan (30 d)" "$c" poner_ciclo "$MEDIA_BUCKET"

# --- Respaldos: ya versionaba y caducaba a 180 dias. Se añade la caducidad de versiones no
# actuales (ya la tenia a 30) y el candado.
v=0; versionado_ok "$BACKUP_BUCKET" && v=1
paso "respaldos: versionado" "$v" activar_versionado "$BACKUP_BUCKET"
c=0; ciclo_ok "$BACKUP_BUCKET" && c=1
paso "respaldos: versiones antiguas caducan (30 d)" "$c" poner_ciclo "$BACKUP_BUCKET"

lock_ok=0
if aws s3api get-object-lock-configuration --bucket "$BACKUP_BUCKET" --output json 2>/dev/null \
   | grep -q "\"Days\": $BACKUP_LOCK_DAYS"; then
  lock_ok=1
fi
poner_candado() {
  aws s3api put-object-lock-configuration --bucket "$BACKUP_BUCKET" --object-lock-configuration \
    "{\"ObjectLockEnabled\":\"Enabled\",\"Rule\":{\"DefaultRetention\":{\"Mode\":\"GOVERNANCE\",\"Days\":$BACKUP_LOCK_DAYS}}}"
}
paso "respaldos: candado de retencion ($BACKUP_LOCK_DAYS d, gobernanza)" "$lock_ok" poner_candado

# --- Flota: los paquetes llevan el hash en el nombre y no se sobreescriben nunca, asi que
# el versionado no aportaria nada y cada version pesa gigas. Solo se protege lo abortado.
c=0; aws s3api get-bucket-lifecycle-configuration --bucket "$FLEET_BUCKET" --output json 2>/dev/null | grep -q DaysAfterInitiation && c=1
poner_ciclo_flota() {
  aws s3api put-bucket-lifecycle-configuration --bucket "$FLEET_BUCKET" --lifecycle-configuration \
    '{"Rules":[{"ID":"subidas-abortadas","Status":"Enabled","Filter":{"Prefix":""},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":7}}]}' >/dev/null
}
paso "flota: subidas a medias se abortan (7 d)" "$c" poner_ciclo_flota

echo
if [[ $CHECK -eq 1 ]]; then
  echo "Nada aplicado. Corre sin --check para dejar los buckets como dice este archivo."
else
  echo "Listo. El candado solo alcanza a los respaldos subidos desde ahora; los anteriores siguen protegidos por el versionado y la politica del rol."
fi
