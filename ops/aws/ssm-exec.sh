#!/usr/bin/env bash
# Ejecuta en el servidor de produccion un script leido de la ENTRADA ESTANDAR, igual que
# `ssh servidor 'bash -s'`, pero a traves de SSM Run Command. Existe para el despliegue
# automatico: el Security Group solo admite el puerto 22 desde la IP del administrador en
# /32 y los runners de GitHub salen por IPs arbitrarias, asi que por SSH el job muere con
# "Connection timed out". Por SSM no hace falta abrir ningun puerto de entrada: el agente
# de la instancia abre la conexion hacia AWS.
#
# Uso:
#   DEPLOY_SSM_INSTANCE=i-xxxx ops/aws/ssm-exec.sh <<'REMOTO'
#   ...script...
#   REMOTO
#
# Devuelve el codigo de salida del script remoto, para que un fallo tumbe el job.
set -euo pipefail

INSTANCE="${DEPLOY_SSM_INSTANCE:?falta DEPLOY_SSM_INSTANCE (id de la instancia EC2)}"
REGION="${AWS_REGION:-${ECR_REGION:-us-east-1}}"
RUN_AS="${DEPLOY_USER:-deploy}"
ESPERA_MAX="${SSM_WAIT_SECONDS:-1800}"

script="$(cat)"
[[ -n "${script//[[:space:]]/}" ]] || { echo "ssm-exec: script vacio" >&2; exit 2; }

# AWS-RunShellScript corre como root. El arbol de despliegue pertenece al usuario deploy:
# ejecutar como root dejaria ficheros suyos (.deployed-tag, el estado de la deriva) que
# despues el propio usuario no podria reescribir, y el fallo aparecerian dias mas tarde en
# un despliegue manual. Se baja de privilegio con runuser.
#
# El script viaja en base64 dentro de un solo comando: asi no hay que escapar comillas ni
# saltos de linea al meterlo en el JSON de --parameters, que es donde se rompen estas cosas.
b64="$(printf '%s' "$script" | base64 | tr -d '\n')"
orden="echo $b64 | base64 -d | runuser -u $RUN_AS -- bash -s"

params="$(mktemp)"; trap 'rm -f "$params"' EXIT
printf '{"commands":["%s"]}' "$orden" > "$params"

cid="$(aws ssm send-command --region "$REGION" --instance-ids "$INSTANCE" \
        --document-name AWS-RunShellScript \
        --comment "despliegue core-force" \
        --timeout-seconds 600 \
        --parameters "file://$params" \
        --query 'Command.CommandId' --output text)"
echo "ssm-exec: comando $cid en $INSTANCE" >&2

# El primer get puede dar InvocationDoesNotExist durante un instante: la invocacion tarda
# en materializarse aunque send-command ya haya devuelto el id.
estado=""
fin=$(( SECONDS + ESPERA_MAX ))
while (( SECONDS < fin )); do
  estado="$(aws ssm get-command-invocation --region "$REGION" \
             --command-id "$cid" --instance-id "$INSTANCE" \
             --query 'Status' --output text 2>/dev/null || echo Pending)"
  case "$estado" in
    Success|Failed|Cancelled|TimedOut|Undeliverable|Terminated) break ;;
  esac
  sleep 5
done

leer() {
  aws ssm get-command-invocation --region "$REGION" \
    --command-id "$cid" --instance-id "$INSTANCE" --query "$1" --output text 2>/dev/null || true
}

salida="$(leer 'StandardOutputContent')"
errores="$(leer 'StandardErrorContent')"
rc="$(leer 'ResponseCode')"

[[ -n "$salida"  && "$salida"  != "None" ]] && printf '%s\n' "$salida"
[[ -n "$errores" && "$errores" != "None" ]] && printf '%s\n' "$errores" >&2

case "$estado" in
  Success|Failed) ;;
  *) echo "ssm-exec: el comando quedo en estado '$estado' (ni exito ni fallo)" >&2; exit 1 ;;
esac

# SSM recorta la salida en linea a 24 KB; lo que se pierde no es el codigo de salida, pero
# conviene decirlo en vez de dejar creer que el script no imprimio mas.
[[ ${#salida} -ge 24000 ]] && echo "ssm-exec: salida recortada por SSM (24 KB)" >&2

[[ "$rc" =~ ^[0-9]+$ ]] || rc=1
exit "$rc"
