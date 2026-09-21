#!/usr/bin/env bash
# Seguimiento de deploy/mail/ frente a mailcow-dockerized, la base de la que salen los motores.
#
# Los motores se copiaron UNA vez (CLAUDE.md) y desde entonces cada version de mailcow se porta a
# mano. Esta herramienta no copia nada nunca: solo mide y avisa, para que portar sea aplicar una
# lista y no una investigacion.
#
#   upstream.sh verificar                 sin red: cada fichero de deploy/mail esta clasificado, los
#                                         identicos a mailcow siguen identicos y lo modificado esta
#                                         explicado en deploy/mail/UPSTREAM.md
#   upstream.sh estado <checkout>         estado de cada fichero contra un checkout de mailcow
#   upstream.sh informe [<checkout>] [<ref>]
#                                         que cambio en mailcow entre la base y <ref> (por defecto la
#                                         rama principal) en lo que copiamos, cruzado con el manifiesto
#   upstream.sh regenerar <checkout> [--base <sha> <fecha> <etiqueta>]
#                                         reescribe el manifiesto conservando las categorias; tras
#                                         portar una version, con la nueva base
#
# El manifiesto (deploy/mail/upstream-manifest.tsv) es la fuente de verdad legible por maquina:
#   #base<TAB><commit><TAB><fecha><TAB><etiqueta de mailcow>
#   identico<TAB><ruta><TAB><sha256>                     igual que mailcow
#   modificado<TAB><ruta><TAB><categorias><TAB><origen>  distinto de su equivalente en mailcow
#   nuevo<TAB><ruta><TAB><categorias><TAB>-              sin equivalente en mailcow
#   propio<TAB><ruta><TAB>-                              nuestro por definicion (compose, README)
# Las rutas son relativas a deploy/mail/. El porque de cada una vive en deploy/mail/UPSTREAM.md.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${UPSTREAM_RAIZ:-$(cd "$SCRIPT_DIR/../.." && pwd)}"
MAPA="$SCRIPT_DIR/mapa.tsv"
MANIFIESTO="$ROOT/deploy/mail/upstream-manifest.tsv"
LIBRO="$ROOT/deploy/mail/UPSTREAM.md"
REPO_MAILCOW="https://github.com/mailcow/mailcow-dockerized.git"
CATEGORIAS="etiqueta nombres postgres sogo-php servicios-go cron tls endurecimiento arranque cola"

falla() { echo "upstream: $*" >&2; exit 1; }

lista_ficheros() {
  if [[ -n "${UPSTREAM_FICHEROS:-}" ]]; then
    cat "$UPSTREAM_FICHEROS"
  else
    git -C "$ROOT" ls-files deploy/mail | sed 's|^deploy/mail/||'
  fi
}

# origen <checkout> <ruta local>: la ruta en mailcow de un fichero, o nada si no tiene equivalente.
origen() {
  local co="$1" rel="$2" top="${2%%/*}" sub="${2#*/}" u cand
  cand="$(awk -F'\t' -v r="$rel" '$1=="archivo" && $2==r {print $3}' "$MAPA")"
  if [[ -n "$cand" && -f "$co/$cand" ]]; then echo "$cand"; return; fi
  [[ "$rel" == */* ]] || return 0
  for u in $(awk -F'\t' -v t="$top" '$1=="motor" && $2==t {print $3}' "$MAPA" | tr ',' ' '); do
    for cand in "data/Dockerfiles/$u/$sub" "data/conf/$u/$sub" "data/assets/$u/$sub"; do
      if [[ -f "$co/$cand" ]]; then echo "$cand"; return; fi
    done
    if [[ "$sub" == conf/* && -f "$co/data/conf/$u/${sub#conf/}" ]]; then
      echo "data/conf/$u/${sub#conf/}"; return
    fi
  done
}

# estado <checkout>: una linea por fichero: estado, ruta, origen
estado() {
  local co="$1" rel up
  while IFS= read -r rel; do
    [[ -n "$rel" ]] || continue
    up="$(origen "$co" "$rel")"
    if [[ -z "$up" ]]; then
      case "$rel" in
        README.md|UPSTREAM.md|upstream-manifest.tsv|docker-compose*.yml) printf 'propio\t%s\t-\n' "$rel" ;;
        *) printf 'nuevo\t%s\t-\n' "$rel" ;;
      esac
    elif cmp -s "$co/$up" "$ROOT/deploy/mail/$rel"; then
      printf 'identico\t%s\t%s\n' "$rel" "$up"
    else
      printf 'modificado\t%s\t%s\n' "$rel" "$up"
    fi
  done < <(lista_ficheros | LC_ALL=C sort)
}

base_del_manifiesto() { awk -F'\t' '$1=="#base" {print $2}' "$MANIFIESTO"; }

# verificar: sin red. Comprueba el manifiesto contra el arbol.
verificar() {
  [[ -f "$MANIFIESTO" ]] || falla "falta $MANIFIESTO"
  [[ -f "$LIBRO" ]] || falla "falta $LIBRO"
  local errores=0 n_id=0 n_mod=0 n_nuevo=0 n_propio=0
  err() { echo "  FALLA: $*"; errores=$((errores + 1)); }

  local base; base="$(base_del_manifiesto)"
  [[ "$base" =~ ^[0-9a-f]{12}$ ]] || err "el manifiesto no declara la base de mailcow (#base<TAB><12 hex>)"

  declare -A est cats sha
  local e r c o l
  while IFS=$'\t' read -r e r c o; do
    [[ -z "$e" || "$e" == "#base" || "$e" == \#* ]] && continue
    case "$e" in
      identico) est[$r]=identico; sha[$r]="$c" ;;
      modificado|nuevo) est[$r]="$e"; cats[$r]="$c" ;;
      propio) est[$r]=propio ;;
      *) err "estado desconocido '$e' en el manifiesto ($r)" ;;
    esac
  done < "$MANIFIESTO"

  local presentes; presentes="$(mktemp)"
  while IFS= read -r r; do
    [[ -n "$r" ]] || continue
    echo "$r" >> "$presentes"
    e="${est[$r]:-}"
    case "$e" in
      "") err "sin clasificar: $r (no esta en el manifiesto; ejecuta 'upstream.sh regenerar' y explicalo en UPSTREAM.md)" ;;
      identico)
        n_id=$((n_id + 1))
        l="$(sha256sum "$ROOT/deploy/mail/$r" | awk '{print $1}')"
        [[ "$l" == "${sha[$r]}" ]] || err "diverge de mailcow sin registrar: $r era identico y ahora es distinto (si es a proposito, pasa a modificado y explicalo en UPSTREAM.md)" ;;
      modificado|nuevo)
        [[ "$e" == modificado ]] && n_mod=$((n_mod + 1)) || n_nuevo=$((n_nuevo + 1))
        c="${cats[$r]:-}"
        if [[ -z "$c" || "$c" == sin-clasificar ]]; then
          err "sin categoria: $r"
        else
          for l in ${c//,/ }; do
            [[ " $CATEGORIAS " == *" $l "* ]] || err "categoria desconocida '$l' en $r (validas: $CATEGORIAS)"
          done
        fi
        grep -qF "\`$r\`" "$LIBRO" || err "no explicado en UPSTREAM.md: $r" ;;
      propio) n_propio=$((n_propio + 1)) ;;
    esac
  done < <(lista_ficheros | LC_ALL=C sort)

  for r in "${!est[@]}"; do
    grep -qxF "$r" "$presentes" || err "esta en el manifiesto pero ya no existe: $r"
  done

  rm -f "$presentes"
  if [[ $errores -ne 0 ]]; then
    echo "  $errores problema(s) en el seguimiento de mailcow."
    return 1
  fi
  echo "  OK: $n_id identicos a mailcow, $n_mod modificados, $n_nuevo nuevos y $n_propio propios, todos clasificados; base $base."
}

clonar_mailcow() {
  local dir="$1"
  git clone -q --filter=blob:none --no-checkout "$REPO_MAILCOW" "$dir"
  git -C "$dir" sparse-checkout init --cone >/dev/null
  git -C "$dir" sparse-checkout set data/Dockerfiles data/conf data/assets >/dev/null
}

# regenerar <checkout> [--base <sha> <fecha> <etiqueta>]: deja el checkout en la base y reescribe.
regenerar() {
  local co="$1"; shift
  local base fecha etiqueta
  base="$(base_del_manifiesto)"
  fecha="$(awk -F'\t' '$1=="#base" {print $3}' "$MANIFIESTO")"
  etiqueta="$(awk -F'\t' '$1=="#base" {print $4}' "$MANIFIESTO")"
  if [[ "${1:-}" == "--base" ]]; then base="$2"; fecha="$3"; etiqueta="$4"; fi
  git -C "$co" checkout -q --detach "$base"
  declare -A viejas
  local e r c o
  while IFS=$'\t' read -r e r c o; do
    [[ "$e" == modificado || "$e" == nuevo ]] && viejas[$r]="$c"
  done < "$MANIFIESTO"
  local tmp; tmp="$(mktemp)"
  printf '#base\t%s\t%s\t%s\n' "$base" "$fecha" "$etiqueta" > "$tmp"
  while IFS=$'\t' read -r e r o; do
    case "$e" in
      identico) printf 'identico\t%s\t%s\n' "$r" "$(sha256sum "$ROOT/deploy/mail/$r" | awk '{print $1}')" ;;
      modificado) printf 'modificado\t%s\t%s\t%s\n' "$r" "${viejas[$r]:-sin-clasificar}" "$o" ;;
      nuevo) printf 'nuevo\t%s\t%s\t-\n' "$r" "${viejas[$r]:-sin-clasificar}" ;;
      propio) printf 'propio\t%s\t-\n' "$r" ;;
    esac
  done < <(estado "$co") >> "$tmp"
  mv "$tmp" "$MANIFIESTO"
  echo "manifiesto reescrito con base $base; lo 'sin-clasificar' hay que clasificarlo y explicarlo en UPSTREAM.md"
}

# informe [<checkout>] [<ref>]
informe() {
  local co="${1:-}" destino="${2:-origin/master}" limpiar=""
  if [[ -z "$co" ]]; then
    co="$(mktemp -d)"; limpiar="$co"; clonar_mailcow "$co"
  fi
  local base; base="$(base_del_manifiesto)"
  [[ -n "$base" ]] || falla "el manifiesto no declara la base"
  git -C "$co" checkout -q --detach "$base"
  local salida; salida="$(mktemp)"
  estado "$co" | awk -F'\t' '$3!="-" {print $3 "\t" $1 "\t" $2}' | LC_ALL=C sort > "$salida"

  local cambios; cambios="$(git -C "$co" diff --name-status "$base" "$destino" -- data/Dockerfiles data/conf data/assets)"
  local limpio="" revisar="" omitido="" sinequiv="" total_rel=0 st up_ruta nuevo_ruta linea motivo
  while IFS=$'\t' read -r st up_ruta nuevo_ruta; do
    [[ -n "$st" ]] || continue
    [[ "$st" == R* ]] && up_ruta="$nuevo_ruta"
    motivo="$(awk -F'\t' -v p="$up_ruta" 'substr($1,1,1)!="#" && $1=="omitido" && index(p,$2)==1 {print $3; exit}' "$MAPA")"
    if [[ -n "$motivo" ]]; then
      omitido+="- \`$up_ruta\` ($st): no copiado: $motivo"$'\n'; continue
    fi
    linea="$(awk -F'\t' -v p="$up_ruta" '$1==p {print $2 "\t" $3}' "$salida" | head -1)"
    if [[ -z "$linea" ]]; then
      sinequiv+="- \`$up_ruta\` ($st): sin equivalente local; decidir si nos interesa"$'\n'; total_rel=$((total_rel + 1))
    elif [[ "${linea%%$'\t'*}" == identico ]]; then
      limpio+="- \`${linea#*$'\t'}\` <- \`$up_ruta\` ($st): identico a mailcow, el cambio se reaplica igual"$'\n'; total_rel=$((total_rel + 1))
    else
      revisar+="- \`${linea#*$'\t'}\` <- \`$up_ruta\` ($st): modificado por nosotros; ver UPSTREAM.md y reaplicar sobre nuestra version"$'\n'; total_rel=$((total_rel + 1))
    fi
  done <<< "$cambios"

  # Commits y versiones solo de los motores que copiamos: nginx, SOGo y PHP no nos afectan.
  local rutas=() u
  for u in $(awk -F'\t' '$1=="motor" {print $3}' "$MAPA" | tr ',' ' '); do
    rutas+=("data/Dockerfiles/$u" "data/conf/$u" "data/assets/$u")
  done
  local commits n_commits seg
  commits="$(git -C "$co" log --format='%h %ad %s' --date=short "$base..$destino" -- "${rutas[@]}" | head -60)"
  n_commits="$(git -C "$co" rev-list --count "$base..$destino" -- "${rutas[@]}")"
  seg="$(printf '%s\n' "$commits" | grep -iE 'cve|secur|vulnerab|exploit|auth bypass|xss|injection' || true)"
  local versiones dockerfiles=()
  for u in $(awk -F'\t' '$1=="motor" {print $3}' "$MAPA" | tr ',' ' '); do dockerfiles+=("data/Dockerfiles/$u/Dockerfile"); done
  versiones="$(git -C "$co" diff -U0 "$base" "$destino" -- "${dockerfiles[@]}" 2>/dev/null | grep -E '^[+-](FROM|ARG [A-Z_]*(VER|VERSION)|.*VERSION=)' || true)"

  echo "RELEVANTES: $total_rel"
  echo "## Cambios de mailcow-dockerized pendientes de portar"
  echo
  echo "Base: \`$base\`. Destino: \`$destino\` ($(git -C "$co" rev-parse --short "$destino")). Commits que tocan lo que copiamos: $n_commits."
  echo
  if [[ -n "$seg" ]]; then echo "### Posible seguridad"; echo '```'; echo "$seg"; echo '```'; echo; fi
  if [[ -n "$versiones" ]]; then echo "### Versiones e imagenes base"; echo '```'; echo "$versiones"; echo '```'; echo; fi
  [[ -z "$limpio" ]] || { echo "### Cambio limpio (nuestro fichero es identico al de mailcow)"; printf '%s\n' "$limpio"; }
  [[ -z "$revisar" ]] || { echo "### A revisar (nuestro fichero difiere del de mailcow)"; printf '%s\n' "$revisar"; }
  [[ -z "$sinequiv" ]] || { echo "### Sin equivalente local"; printf '%s\n' "$sinequiv"; }
  [[ -z "$omitido" ]] || { echo "### No copiado por diseno (informativo)"; printf '%s\n' "$omitido"; }
  if [[ $total_rel -eq 0 ]]; then echo "Sin cambios relevantes desde la base."; fi
  echo
  echo "Procedimiento: deploy/mail/UPSTREAM.md, seccion 3. Esta herramienta no copia ficheros."
  rm -f "$salida"; [[ -z "$limpiar" ]] || rm -rf "$limpiar"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  verificar) verificar ;;
  estado) [[ $# -ge 1 ]] || falla "uso: estado <checkout>"; estado "$1" ;;
  informe) informe "$@" ;;
  regenerar) [[ $# -ge 1 ]] || falla "uso: regenerar <checkout> [--base <sha> <fecha> <etiqueta>]"; regenerar "$@" ;;
  *) falla "uso: upstream.sh verificar | estado <checkout> | informe [<checkout>] [<ref>] | regenerar <checkout> [--base ...]" ;;
esac
