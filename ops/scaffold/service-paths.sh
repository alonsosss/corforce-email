#!/usr/bin/env bash
# Fuente unica del mapa "ruta del repositorio -> servicio de Compose". Se deriva de
# docker-compose.yml (context / dockerfile / APP_DIR), nunca de una lista escrita a mano:
# un microservicio nuevo queda cubierto por los despliegues sin editar ningun script.
#
#   ops/scaffold/service-paths.sh                     # servicio<TAB>ruta<TAB>clase
#   git diff --name-only A..B | ops/scaffold/service-paths.sh --changed
#   ops/scaffold/service-paths.sh --class go          # solo los de una clase
#   ops/scaffold/service-paths.sh --migrations        # los que hornean migrations/
#
# Clases (determinan que cambios compartidos afectan a que servicios):
#   go          context "." + dockerfile services/<svc>/Dockerfile -> comparte pkg/ y go.mod
#   frontend    context ./frontend + APP_DIR              -> comparte frontend/packages
#   standalone  context propio ./services/<svc>           -> solo su carpeta
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT/docker-compose.yml}"

# Raices compartidas: un cambio aqui invalida la imagen de TODOS los servicios de la clase.
GO_SHARED_RE='^(pkg/|go\.mod$|go\.sum$)'
# nginx.conf entra aqui porque frontend/Dockerfile lo hornea en LAS 24 imagenes
# (COPY nginx.conf /etc/nginx/conf.d/default.conf). Sin el, un cambio de politica de
# cache no despertaba a ningun servicio y no llegaba nunca a produccion, aunque el
# manual dijera que reconstruye los frontends.
FRONTEND_SHARED_RE='^frontend/(packages/|package\.json$|pnpm-lock\.yaml$|pnpm-workspace\.yaml$|Dockerfile$|nginx\.conf$|tsconfig)'

# Las migraciones viajan DENTRO de la imagen del servicio que las aplica, asi que una
# canonica nueva solo llega a las empresas si esa imagen se reconstruye. Sin esta raiz, un
# commit que solo anade un .sql no despertaba a ningun servicio: los ficheros llegaban al
# servidor por rsync pero nadie los aplicaba, y habia que meterlos a mano en cada base.
# Es el origen del atasco de 43 canonicas de 2026-08-08.
MIGRATIONS_SHARED_RE='^migrations/(tenant|registry)/'

service_map() {
  awk '
    # Clave de servicio (2 espacios de indentacion).
    /^  [a-z0-9][a-z0-9_-]*:[[:space:]]*$/ {
      flush()
      svc = $1; sub(/:.*/, "", svc); build = 0; ctx = ""; dfile = ""; appdir = ""
      next
    }
    /^    build:[[:space:]]*$/ { build = 1; next }
    build && /^      context:/    { ctx = $2; next }
    build && /^      dockerfile:/ { dfile = $2; next }
    build && /^        APP_DIR:/  { appdir = $2; next }
    # Cualquier otra clave de 4 espacios cierra la seccion build.
    /^    [a-z]/ { build = 0 }
    END { flush() }

    function flush(   path, class) {
      if (svc == "" || (ctx == "" && dfile == "")) { svc = ""; return }
      if (appdir != "") {
        path = "frontend/" appdir; class = "frontend"
      } else if (ctx != "" && ctx != ".") {
        path = ctx; class = "standalone"
      } else {
        path = dfile; sub(/\/[^\/]*$/, "", path); class = "go"
      }
      sub(/^\.\//, "", path)
      print svc "\t" path "\t" class
      svc = ""
    }
  ' "$COMPOSE_FILE"
}

# Servicios cuya imagen hornea las migraciones. Se descubre leyendo los Dockerfile en vez
# de escribir el nombre aqui: si otro servicio empieza a hornearlas, queda cubierto sin
# tocar este script, igual que el resto del mapa.
migration_services() {
  local svc path
  while IFS=$'\t' read -r svc path _; do
    [[ -z "$svc" ]] && continue
    for df in "$ROOT/$path"/Dockerfile*; do
      [[ -f "$df" ]] || continue
      if grep -qE '^[[:space:]]*COPY([[:space:]]|--)[^#]*migrations' "$df"; then
        echo "$svc"
        break
      fi
    done
  done < <(service_map) | sort -u
}

# Rutas del repositorio que una imagen COPIA desde fuera de su propia carpeta.
#
# Existe por un caso real: production-labeling entrega el agente de impresion y
# su Dockerfile lo compila desde edge/xp365b/. Esa ruta no esta bajo
# services/production-labeling, asi que tocar el agente no despertaba al
# servicio que lo sirve: los equipos de planta seguian bajando el binario
# anterior, sin error y sin nada que lo delatara.
#
# Se lee del propio Dockerfile, como el resto del mapa, para que el siguiente
# servicio que copie de una carpeta compartida quede cubierto sin editar esto.
#
# Solo se aplica a las imagenes cuyo contexto es la raiz ("."), que son las
# unicas donde el origen de un COPY es una ruta del repositorio tal cual. Con
# contexto propio, el origen es relativo a esa carpeta y tomarlo por una ruta
# del repositorio emparejaria ficheros que no tienen nada que ver.
extra_roots() {
  local svc path class df
  while IFS=$'\t' read -r svc path class; do
    [[ -z "$svc" || "$class" != "go" ]] && continue
    for df in "$ROOT/$path"/Dockerfile*; do
      [[ -f "$df" ]] || continue
      # COPY [--flags] origen... destino: se descarta el destino, las banderas y
      # los COPY --from=<etapa>, que no traen nada del repositorio.
      #
      # Un RUN tambien puede traer una ruta del repositorio a la imagen, y hasta ahora
      # no se miraba: carrier-integration hornea la extension del navegador con
      # "RUN cp -a extensions/carrier-shalom/. ...", asi que tocarla no despertaba al
      # servicio que la sirve y la plataforma seguia repartiendo la version anterior, sin error
      # y sin nada que lo delatara. De un RUN solo se toman los tokens con barra; el
      # filtrado de verdad (que exista, que no sea su propia carpeta, que no sea una raiz
      # compartida) lo hace el llamador, de modo que colar un token de mas no pasa de una
      # reconstruccion extra, que es el lado seguro en el que equivocarse.
      #
      # Las continuaciones con barra invertida se unen antes de mirar la linea: un COPY o
      # un RUN partido en varias lineas dejaba de verse entero.
      awk '
        { buf = buf $0
          if (buf ~ /\\[[:space:]]*$/) { sub(/\\[[:space:]]*$/, " ", buf); next }
          procesa(buf); buf = "" }
        END { if (buf != "") procesa(buf) }
        function procesa(l,   n, t, i, esCopy, esRun, ultimo) {
          gsub(/^[[:space:]]+|[[:space:]]+$/, "", l)
          if (l == "") return
          n = split(l, t, /[[:space:]]+/)
          esCopy = (t[1] == "COPY"); esRun = (t[1] == "RUN")
          if (!esCopy && !esRun) return
          if (esCopy && l ~ /--from=/) return
          ultimo = esCopy ? n - 1 : n
          for (i = 2; i <= ultimo; i++) {
            if (t[i] ~ /^--/) continue
            if (esRun && t[i] !~ /\//) continue
            print t[i]
          }
        }
      ' "$df"
    done | while read -r origen; do
      origen="${origen#./}"; origen="${origen%/.}"; origen="${origen%/}"
      [[ -z "$origen" || "$origen" == "." ]] && continue
      # Lo que ya cubren las raices compartidas o su propia carpeta, no se repite.
      [[ "$origen" == "$path"/* || "$origen" == "$path" ]] && continue
      [[ "$origen" =~ $GO_SHARED_RE || "$origen" =~ ^migrations/ || "$origen" == frontend/* ]] && continue
      # Y tiene que existir: filtra lo que no sea una ruta de verdad.
      [[ -e "$ROOT/$origen" ]] || continue
      printf '%s\t%s\n' "$svc" "$origen"
    done
  done < <(service_map) | sort -u
}

changed_services() {
  local map; map="$(service_map)"
  local mig; mig="$(migration_services | paste -sd' ' -)"
  local extra; extra="$(extra_roots)"
  awk -v map="$map" -v go_shared="$GO_SHARED_RE" -v fe_shared="$FRONTEND_SHARED_RE" \
      -v mig_shared="$MIGRATIONS_SHARED_RE" -v mig_services="$mig" -v extra="$extra" '
    BEGIN {
      n = split(map, lines, "\n")
      for (i = 1; i <= n; i++) {
        if (lines[i] == "") continue
        split(lines[i], f, "\t")
        svc[i] = f[1]; path[i] = f[2]; class[i] = f[3]
      }
      total = n
      e = split(extra, elines, "\n")
      for (i = 1; i <= e; i++) {
        if (elines[i] == "") continue
        split(elines[i], g, "\t")
        extraSvc[i] = g[1]; extraPath[i] = g[2]
      }
      extraTotal = e
    }
    {
      if ($0 == "") next
      # Cambio en una raiz compartida: afecta a toda la clase.
      if ($0 ~ go_shared) { for (i = 1; i <= total; i++) if (class[i] == "go") hit[svc[i]] = 1; next }
      if ($0 ~ fe_shared) { for (i = 1; i <= total; i++) if (class[i] == "frontend") hit[svc[i]] = 1; next }
      if ($0 ~ mig_shared) { m = split(mig_services, ms, " "); for (i = 1; i <= m; i++) hit[ms[i]] = 1; next }
      # Lo que una imagen copia de fuera de su carpeta la afecta igual. No lleva
      # "next": una misma ruta puede alimentar a varios servicios.
      for (i = 1; i <= extraTotal; i++) {
        if ($0 == extraPath[i] || index($0, extraPath[i] "/") == 1) hit[extraSvc[i]] = 1
      }
      # Cambio concreto: gana la ruta mas especifica (frontend/remotes/admin sobre frontend).
      best = ""; bestlen = 0
      for (i = 1; i <= total; i++) {
        p = path[i]
        if (index($0, p "/") == 1 && length(p) > bestlen) { best = svc[i]; bestlen = length(p) }
      }
      if (best != "") hit[best] = 1
    }
    END { for (s in hit) print s }
  ' | sort
}

# Un servicio con build: que se cayera del mapa dejaria de desplegarse por deteccion sin
# error visible: el servidor seguiria con la version anterior. Se verifica en CI.
check_map() {
  local build_services mapped missing
  build_services="$(awk '
    /^  [a-z0-9][a-z0-9_-]*:[[:space:]]*$/ { s=$1; sub(/:.*/,"",s); next }
    /^    build:/ { if (s!="") { print s; s="" } }
  ' "$COMPOSE_FILE" | sort)"
  mapped="$(service_map | cut -f1 | sort)"
  missing="$(comm -23 <(echo "$build_services") <(echo "$mapped"))"
  if [[ -n "$missing" ]]; then
    echo "servicios con build: fuera del mapa de rutas (cambiarlos no los desplegaria):" >&2
    echo "$missing" | sed 's/^/  - /' >&2
    return 1
  fi
  echo "OK: $(echo "$mapped" | wc -l) servicios con build: mapeados a su ruta."

  # Si ningun servicio hornea las migraciones, un .sql nuevo no despierta a nadie y las
  # canonicas dejan de llegar a las empresas SIN error visible: el sintoma aparece semanas
  # despues, cuando un modulo falla porque le falta una tabla.
  local mig; mig="$(migration_services)"
  if [[ -z "$mig" ]]; then
    echo "ningun servicio hornea migrations/ en su imagen: un .sql nuevo no se desplegaria" >&2
    return 1
  fi
  echo "OK: migrations/ despierta a $(echo "$mig" | paste -sd', ' -)."
}

case "${1:---map}" in
  --map)        service_map ;;
  --changed)    changed_services ;;
  --check)      check_map ;;
  --migrations) migration_services ;;
  --class)   service_map | awk -F'\t' -v c="${2:?indica la clase: go|frontend|standalone}" '$3==c{print $1}' ;;
  *)            echo "uso: $0 [--map | --changed | --check | --migrations | --class go|frontend|standalone]" >&2; exit 2 ;;
esac
