#!/usr/bin/env bash
# Prueba sin docker de deploy/mail/rspamd/controller-password.sh, el que escribe las contrasenas del
# controller de Rspamd desde el almacen de secretos. Con un rspamadm falso (hash determinista) comprueba:
#   - lectura y escritura: una linea password y otra enable_password, cada una con el hash de la suya;
#   - solo lectura: enable_password existe, es aleatoria y distinta de la de lectura en cada arranque
#     (sin ella, Rspamd deja que la lectura escriba: comprobado con Rspamd 4.1.4);
#   - ninguna: sin linea password y con enable_password aleatoria (controller cerrado);
#   - la misma en las dos, o un rspamadm que no devuelve un hash: sale 1 y el fichero anterior queda intacto;
#   - ninguna contrasena en claro en el fichero ni en la salida, y el fichero en 0640;
# y demuestra que muerde con una mutacion (sin la escritura aleatoria).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GUION="$ROOT/deploy/mail/rspamd/controller-password.sh"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
FALLOS=0
ok() { echo "  OK: $*"; }
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

cat >"$W/rspamadm" <<'EOF'
#!/usr/bin/env bash
# rspamadm pw -e -p <contrasena>: un hash con la forma de los de Rspamd, derivado de la contrasena.
[[ "${RSPAMADM_ROTO:-0}" == 1 ]] && { echo "Invalid password"; exit 0; }
printf '$2$%s\n' "$(printf '%s' "$4" | sha256sum | cut -c1-40)"
EOF
chmod +x "$W/rspamadm"
hash_de() { printf '$2$%s' "$(printf '%s' "$1" | sha256sum | cut -c1-40)"; }

LECTURA="lectura-$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
ESCRITURA="escritura-$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
F="$W/worker-controller-password.inc"

correr() { # correr <guion> [VAR=valor...] -> salida en $W/salida, codigo en RC
  local guion="$1"; shift
  env -u RSPAMD_CONTROLLER_PASSWORD -u RSPAMD_CONTROLLER_ENABLE_PASSWORD \
    CONTROLLER_PASSWORD_FILE="$F" RSPAMADM="$W/rspamadm" "$@" bash "$guion" >"$W/salida" 2>&1
  RC=$?
}
sin_claro() {
  if grep -qF -e "$LECTURA" -e "$ESCRITURA" "$F" "$W/salida" 2>/dev/null; then mal "$1: una contrasena en claro"; else ok "$1: ninguna contrasena en claro"; fi
}

echo "controller-password: lectura y escritura"
correr "$GUION" RSPAMD_CONTROLLER_PASSWORD="$LECTURA" RSPAMD_CONTROLLER_ENABLE_PASSWORD="$ESCRITURA"
[[ $RC -eq 0 ]] && ok "sale 0" || { mal "sale $RC"; cat "$W/salida" >&2; }
grep -qxF "password = \"$(hash_de "$LECTURA")\";" "$F" && ok "password es el hash de la de lectura" || mal "sin la linea password correcta"
grep -qxF "enable_password = \"$(hash_de "$ESCRITURA")\";" "$F" && ok "enable_password es el hash de la de escritura" || mal "sin la linea enable_password correcta"
[[ "$(stat -c %a "$F")" == 640 ]] && ok "fichero en 0640" || mal "modo $(stat -c %a "$F")"
grep -q "aprendizaje activado" "$W/salida" && ok "dice que el aprendizaje queda activado" || mal "no dice el estado del aprendizaje"
sin_claro "lectura y escritura"

echo "controller-password: solo lectura"
correr "$GUION" RSPAMD_CONTROLLER_PASSWORD="$LECTURA"
[[ $RC -eq 0 ]] && ok "sale 0" || mal "sale $RC"
grep -qxF "password = \"$(hash_de "$LECTURA")\";" "$F" && ok "password es el hash de la de lectura" || mal "sin la linea password"
E1="$(sed -n 's/^enable_password = "\(.*\)";$/\1/p' "$F")"
if [[ -n "$E1" && "$E1" != "$(hash_de "$LECTURA")" ]]; then
  ok "enable_password existe y no es la de lectura: la lectura no puede escribir"
else
  mal "sin enable_password distinta de la lectura: Rspamd dejaria aprender con la contrasena de lectura"
fi
correr "$GUION" RSPAMD_CONTROLLER_PASSWORD="$LECTURA"
E2="$(sed -n 's/^enable_password = "\(.*\)";$/\1/p' "$F")"
[[ -n "$E2" && "$E2" != "$E1" ]] && ok "la escritura aleatoria cambia en cada arranque" || mal "la escritura aleatoria se repite"
grep -q "aprendizaje desactivado" "$W/salida" && ok "dice que el aprendizaje queda desactivado" || mal "no dice que el aprendizaje queda desactivado"
sin_claro "solo lectura"

echo "controller-password: ninguna"
correr "$GUION"
[[ $RC -eq 0 ]] && ok "sale 0" || mal "sale $RC"
! grep -q '^password' "$F" && grep -q '^enable_password = "\$2\$' "$F" && ok "sin password y con escritura aleatoria: controller cerrado" ||
  mal "el fichero sin contrasenas no queda cerrado: $(cat "$F")"
grep -q "lectura desactivada" "$W/salida" && ok "dice que la lectura queda desactivada" || mal "no dice que la lectura queda desactivada"

echo "controller-password: errores sin tocar el fichero anterior"
correr "$GUION" RSPAMD_CONTROLLER_PASSWORD="$LECTURA" RSPAMD_CONTROLLER_ENABLE_PASSWORD="$ESCRITURA"
cp "$F" "$W/anterior"
correr "$GUION" RSPAMD_CONTROLLER_PASSWORD="$LECTURA" RSPAMD_CONTROLLER_ENABLE_PASSWORD="$LECTURA"
[[ $RC -eq 1 ]] && grep -q "son la misma" "$W/salida" && ok "la misma contrasena en las dos se rechaza" || mal "la misma contrasena en las dos sale $RC"
cmp -s "$F" "$W/anterior" && ok "y el fichero anterior queda intacto" || mal "el rechazo cambio el fichero"
correr "$GUION" RSPAMD_CONTROLLER_PASSWORD="$LECTURA" RSPAMD_CONTROLLER_ENABLE_PASSWORD="$ESCRITURA" RSPAMADM_ROTO=1
[[ $RC -eq 1 ]] && ok "un rspamadm que no devuelve un hash hace fallar el arranque" || mal "rspamadm roto sale $RC"
cmp -s "$F" "$W/anterior" && ok "sin dejar un fichero a medias" || mal "rspamadm roto cambio el fichero"
[[ -z "$(find "$W" -name 'worker-controller-password.inc.*')" ]] && ok "sin temporales abandonados" || mal "quedan temporales"
sin_claro "errores"

echo "controller-password: mutacion (sin la escritura aleatoria)"
sed 's|^  escritura="\$(head -c 48 /dev/urandom.*|  escritura="$lectura"|' "$GUION" >"$W/mutante.sh"
if cmp -s "$GUION" "$W/mutante.sh"; then
  mal "la mutacion no se aplico: revisa el patron"
else
  correr "$W/mutante.sh" RSPAMD_CONTROLLER_PASSWORD="$LECTURA"
  E3="$(sed -n 's/^enable_password = "\(.*\)";$/\1/p' "$F" 2>/dev/null)"
  if [[ $RC -ne 0 || "$E3" == "$(hash_de "$LECTURA")" ]]; then ok "la prueba detecta la mutacion"; else mal "la mutacion pasa desapercibida"; fi
fi

[[ $FALLOS -eq 0 ]] || { echo "check-rspamd-controller-password: FALLA" >&2; exit 1; }
echo "check-rspamd-controller-password: OK"
