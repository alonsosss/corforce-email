#!/usr/bin/env bash
# Prueba sin docker de scripts/estado-produccion.sh y del modo DEPLOY_PLAN=1 de scripts/deploy-ecr.sh.
#
# Trabaja en un clon temporal (nunca en este repositorio) con un ssh falso que hace de servidor: sirve
# el .deployed-tag y el .deploy-log que se le indiquen y anota cada orden que recibe. Recorre:
#   - un servidor al dia: sale 0 y no lista nada pendiente;
#   - un commit nuevo con un servicio, una migracion de celda nueva y otra ya publicada que se edita:
#     sale 2, lista el servicio y la migracion nueva por su capa y marca la editada;
#   - un servidor sin .deployed-tag: sale 1 y lo dice;
#   - en ninguno de los casos llega al servidor una orden que escriba.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
FALLOS=0
ok() { echo "  OK: $*"; }
mal() { echo "  FALLA: $*" >&2; FALLOS=1; }

# Clon con el arbol actual commiteado: lo que se prueba es lo que hay en disco, no el ultimo commit.
REPO="$W/repo"
git clone -q -c advice.detachedHead=false --shared "$ROOT" "$REPO" || { echo "no se pudo clonar el repositorio" >&2; exit 1; }
git -C "$ROOT" diff --binary HEAD | git -C "$REPO" apply --allow-empty
(cd "$ROOT" && git ls-files --others --exclude-standard -z) | while IFS= read -r -d '' f; do
  mkdir -p "$REPO/$(dirname "$f")"; cp -p "$ROOT/$f" "$REPO/$f"
done
git -C "$REPO" add -A && git -C "$REPO" -c user.name=prueba -c user.email=prueba@ejemplo.invalid commit -q --allow-empty -m base
BASE_SHA="$(git -C "$REPO" rev-parse --short HEAD)"

mkdir -p "$W/bin"
cat >"$W/bin/ssh" <<'EOF'
#!/usr/bin/env bash
orden="${*: -1}"
printf '%s\n' "$orden" >>"$STUB_ORDENES"
case "$orden" in
  true) exit 0 ;;
  *.deployed-tag\ 2\>/dev/null*) [[ -n "${STUB_TAG:-}" ]] && printf '%s\n' "$STUB_TAG"; exit 0 ;;
  *.deploy-log*) printf '2026-09-23T01:00:00Z\tplataforma\t%s\tgateway\tops@puesto\n' "${STUB_TAG:-x}"; exit 0 ;;
  *docker\ ps*) exit 0 ;;
  *) echo "ssh falso: orden inesperada: $orden" >&2; exit 97 ;;
esac
EOF
# gh que no responde: el resultado no puede depender de la red ni de una sesion de GitHub.
printf '#!/usr/bin/env bash\nexit 1\n' >"$W/bin/gh"
chmod +x "$W/bin/ssh" "$W/bin/gh"

correr() { # correr <tag del servidor> -> salida en $W/salida, codigo en RC
  : >"$W/ordenes"
  (cd "$REPO" && PATH="$W/bin:$PATH" STUB_TAG="$1" STUB_ORDENES="$W/ordenes" ESTADO_SIN_MOTORES=1 \
    DEPLOY_HOST=servidor-de-prueba DEPLOY_SSH_KEY=/nonexistent \
    scripts/estado-produccion.sh >"$W/salida" 2>&1)
  RC=$?
}
sin_escrituras() {
  if grep -E '(^|[^2])>|\bmv\b|\brm\b|\bmkdir\b|\btee\b|docker (compose|run|rm|kill|load|pull)' "$W/ordenes" >/dev/null; then
    mal "$1: llego al servidor una orden que escribe: $(grep -E '(^|[^2])>|\bmv\b|\brm\b' "$W/ordenes" | head -1)"
  else
    ok "$1: ninguna orden que escriba en el servidor"
  fi
}

echo "estado-produccion: servidor al dia"
correr "$BASE_SHA"
[[ $RC -eq 0 ]] && ok "sale 0" || { mal "sale $RC (esperado 0)"; sed 's/^/    /' "$W/salida" >&2; }
grep -q '^servicios: ninguno$' "$W/salida" && ok "ningun servicio pendiente" || mal "lista servicios pendientes al dia"
grep -q 'produccion esta al dia' "$W/salida" && ok "veredicto al dia" || mal "sin veredicto de al dia"
grep -q 'gateway' "$W/salida" && ok "muestra el historial del .deploy-log" || mal "no muestra el historial"
sin_escrituras "al dia"

echo "estado-produccion: commit con servicio y migraciones"
MIG_EXISTENTE="$(git -C "$REPO" ls-files 'migrations/cell/canonical/mail-directory/*.sql' | head -1)"
printf -- '-- Schema: mail | Service: mail-directory\nSELECT 1;\n' >"$REPO/migrations/cell/canonical/mail-directory/99_prueba_estado.sql"
printf -- '-- cambio de prueba\n' >>"$REPO/$MIG_EXISTENTE"
printf '// prueba\n' >>"$REPO/services/billing/main.go"
git -C "$REPO" add -A && git -C "$REPO" -c user.name=prueba -c user.email=prueba@ejemplo.invalid commit -q -m cambio
correr "$BASE_SHA"
[[ $RC -eq 2 ]] && ok "sale 2" || { mal "sale $RC (esperado 2)"; sed 's/^/    /' "$W/salida" >&2; }
grep -q '^1 commit' "$W/salida" && ok "cuenta el commit pendiente" || mal "no cuenta el commit pendiente"
grep -qE '^servicios: .*\bbilling\b' "$W/salida" && ok "billing pendiente" || mal "no lista billing"
grep -q 'nueva      migrations/cell/canonical/mail-directory/99_prueba_estado.sql' "$W/salida" \
  && ok "migracion nueva en su capa" || mal "no lista la migracion nueva de celda"
grep -q "MODIFICADA $MIG_EXISTENTE" "$W/salida" && ok "marca la migracion publicada que se edito" || mal "no marca la migracion editada"
grep -q '^registro  ninguna' "$W/salida" && ok "registro sin migraciones" || mal "registro deberia estar sin migraciones"
sin_escrituras "con pendientes"

echo "estado-produccion: servidor sin .deployed-tag"
correr ""
[[ $RC -eq 1 ]] && ok "sale 1" || mal "sale $RC (esperado 1)"
grep -q 'no tiene un .deployed-tag' "$W/salida" && ok "explica por que" || mal "no explica la falta de .deployed-tag"

[[ $FALLOS -eq 0 ]] || { echo "check-estado-produccion: FALLA" >&2; exit 1; }
echo "check-estado-produccion: OK"
