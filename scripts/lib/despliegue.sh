# shellcheck shell=bash
# Piezas comunes de los despliegues que se lanzan desde el puesto de trabajo:
# scripts/deploy-ecr.sh (plataforma, proyecto compose app) y scripts/deploy-mail.sh (motores de
# correo, proyecto compose mail). Se sourcea y no cambia las opciones del shell de quien lo carga.
#
# Una sola implementacion del destino, del candado y de la guardia de retroceso: si los dos
# despliegues tuvieran cada uno la suya, dejarian de excluirse entre si y un retroceso podria
# colarse por el camino que se quedo atras.

# El transporte por defecto es el alias SSM de ~/.ssh/config (ops/aws/setup-ssm-local.sh); en un
# servidor propio se da DEPLOY_HOST, DEPLOY_USER y DEPLOY_SSH_KEY.
DEPLOY_HOST="${DEPLOY_HOST:-core-force-mail-ssm}"; DEPLOY_USER="${DEPLOY_USER:-deploy}"
DEPLOY_PATH="${DEPLOY_PATH:-/opt/core-force-mail/app}"
SSH_KEY="${DEPLOY_SSH_KEY:-$HOME/.ssh/core-force-mail-prod.pem}"
SSH=(ssh -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i "$SSH_KEY" "$DEPLOY_USER@$DEPLOY_HOST")

# Un despliegue a la vez por servidor, sea de la plataforma o de los motores: los dos tocan la red
# mail-engines, el disco de imagenes y la memoria de una maquina pequena.
DEPLOY_LOCK_DIR="${DEPLOY_LOCK_DIR:-/tmp/core-deploy.lock}"
DEPLOY_LOCK_ABANDONO_MIN="${DEPLOY_LOCK_ABANDONO_MIN:-30}"
DEPLOY_LOCK_INTENTOS="${DEPLOY_LOCK_INTENTOS:-60}"
DEPLOY_LOCK_PAUSA="${DEPLOY_LOCK_PAUSA:-10}"
CANDADO_TOMADO=0

# Comprobar el transporte ANTES de compilar. Sin esto, un fallo de conexion aparecia tras varios
# minutos de build, y el mensaje de ssh no distingue "SSM no responde" de "tu IP cambio".
despliegue_comprobar_conexion() {
  "${SSH[@]}" -o ConnectTimeout=45 true 2>/dev/null && return 0
  echo "no se pudo conectar a $DEPLOY_USER@$DEPLOY_HOST" >&2
  if [[ "$DEPLOY_HOST" == "core-force-mail-ssm" ]]; then
    echo "  El transporte es SSM. Comprueba:" >&2
    echo "    ops/aws/setup-ssm-local.sh        # instala el plugin y el alias, y lo prueba" >&2
    echo "    aws ssm describe-instance-information --region ${ECR_REGION:-us-east-1}" >&2
    echo "  Puerta de emergencia (el 22 sigue abierto a tu IP):" >&2
    echo "    DEPLOY_HOST=<ip-publica-de-la-instancia> $0" >&2
  else
    echo "  Transporte directo por el puerto 22: casi siempre es que tu IP cambio o la llave no es esa." >&2
    echo "  O usa SSM, que no depende de la IP:  ops/aws/setup-ssm-local.sh" >&2
  fi
  return 1
}

# El tag del despliegue es el hash de HEAD, asi que lo que viaja tiene que ser HEAD: con el arbol
# sucio la imagen incluiria cambios sin commitear, el tag mentiria y el rollback por tag repondria
# otra cosa.
despliegue_comprobar_arbol() {
  [[ -z "$(git status --porcelain --untracked-files=no)" || "${DEPLOY_ALLOW_DIRTY:-0}" == "1" ]] && return 0
  echo "arbol de trabajo con cambios sin commitear:" >&2
  git status --porcelain --untracked-files=no | sed 's/^/  /' >&2
  echo "commitea primero (el deploy va atado a un commit exacto) o, a proposito," >&2
  echo "salta la guarda con DEPLOY_ALLOW_DIRTY=1." >&2
  return 1
}

# Puerta de regresion de los motores (docs/Plan_Estrategico_Mejoras_Correo.md, B6): un motor solo se
# despliega si make e2e-mail (el flujo de GitHub <flujo>) esta en verde para lo que se va a desplegar.
# Vale una ejecucion verde de HEAD, o la ultima verde de un commit anterior de main si desde entonces
# no cambio nada de lo que ese flujo vigila (sus `paths` de push): un cambio de documentacion no
# obliga a esperar otra ejecucion de cuatro minutos y de un CI largo. Con una ejecucion de HEAD fallida
# o sin ninguna verde, no se despliega.
#
# MAIL_DEPLOY_REGRESION: exigir (por defecto) | avisar (dice lo que falta y sigue) | omitir (sin
# comprobar, con aviso). Necesita gh autenticado (gh auth login) en la maquina desde la que se despliega.
despliegue_comprobar_regresion() {
  local flujo="${1:?flujo}" modo="${MAIL_DEPLOY_REGRESION:-exigir}" runs veredicto
  case "$modo" in
    omitir) echo ">> AVISO: no se comprueba make e2e-mail (MAIL_DEPLOY_REGRESION=omitir)" >&2; return 0 ;;
    exigir | avisar) ;;
    *) echo "MAIL_DEPLOY_REGRESION debe ser exigir, avisar u omitir (es '$modo')" >&2; return 1 ;;
  esac
  if ! command -v gh >/dev/null 2>&1; then
    veredicto="falta gh: no se puede saber si make e2e-mail esta en verde (instalalo y usa gh auth login)"
  elif ! runs="$(gh run list --workflow "$flujo" --branch main --limit 50 --json headSha,conclusion 2>&1)"; then
    veredicto="gh no pudo consultar el flujo $flujo: ${runs:0:200}"
  else
    veredicto="$(RUNS="$runs" FLUJO_FICHERO=".github/workflows/$flujo" python3 - "$(git rev-parse HEAD)" <<'PY'
import fnmatch, json, os, re, subprocess, sys

head = sys.argv[1]
runs = json.loads(os.environ["RUNS"] or "[]")

def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True)

# Los paths de push del flujo: lo que esa prueba vigila.
paths, en_push = [], False
for linea in open(os.environ["FLUJO_FICHERO"], encoding="utf-8"):
    if re.match(r"^  push:", linea):
        en_push = True
    elif re.match(r"^  \w", linea) and not linea.startswith("  push:"):
        en_push = False
    elif en_push:
        m = re.match(r"^\s+- '([^']+)'", linea)
        if m:
            paths.append(m.group(1))
mas = [p for p in paths if not p.startswith("!")]
menos = [p[1:] for p in paths if p.startswith("!")]

def casa(patron, ruta):
    return ruta.startswith(patron[:-2]) if patron.endswith("/**") else fnmatch.fnmatch(ruta, patron)

def vigilado(ruta):
    return any(casa(p, ruta) for p in mas) and not any(casa(p, ruta) for p in menos)

de_head = [r for r in runs if r["headSha"] == head]
if any(r["conclusion"] == "success" for r in de_head):
    print("ok")
    sys.exit()
if any(r["conclusion"] == "failure" for r in de_head):
    print("make e2e-mail FALLA para el commit " + head[:8])
    sys.exit()

for r in runs:
    if r["conclusion"] != "success" or r["headSha"] == head:
        continue
    if git("merge-base", "--is-ancestor", r["headSha"], head).returncode != 0:
        continue
    cambiados = git("diff", "--name-only", r["headSha"], head).stdout.split()
    tocados = [f for f in cambiados if vigilado(f)]
    if tocados:
        print("hay cambios en lo que prueba make e2e-mail desde su ultimo verde (%s): %s%s" % (
            r["headSha"][:8], ", ".join(tocados[:3]), " ..." if len(tocados) > 3 else ""))
    else:
        print("ok")
    sys.exit()
print("no hay ninguna ejecucion verde de make e2e-mail que cubra el commit " + head[:8])
PY
)" || veredicto="no se pudo evaluar la puerta de regresion"
  fi
  [[ "$veredicto" == ok ]] && return 0
  echo "PUERTA DE REGRESION DE LOS MOTORES: $veredicto" >&2
  echo "  Espera al flujo $flujo en GitHub (o lanzalo con: gh workflow run $flujo)." >&2
  if [[ "$modo" == avisar ]]; then echo ">> AVISO: se sigue (MAIL_DEPLOY_REGRESION=avisar)" >&2; return 0; fi
  echo "  Solo con una razon: MAIL_DEPLOY_REGRESION=omitir (queda dicho en la salida)." >&2
  return 1
}

# Los Dockerfile de los servicios Go usan "RUN --mount=type=cache": sin BuildKit el build muere a
# media compilacion con un mensaje que no explica que falta.
despliegue_comprobar_buildkit() {
  docker buildx version >/dev/null 2>&1 && return 0
  echo "falta BuildKit: 'docker buildx' no esta disponible en esta maquina." >&2
  echo "  instalalo con: sudo apt install docker-buildx" >&2
  return 1
}

# adquirir_candado <quien>: el candado es un directorio (mkdir es atomico) con el tag, quien lo
# tiene y la hora; uno sin renovar hace mas de DEPLOY_LOCK_ABANDONO_MIN minutos se considera
# muerto y se roba. Sin esto, dos despliegues en carrera terminan con los contenedores del que
# acabo ultimo, gane quien gane las guardias. Va sin `cd`: el candado no depende de que exista
# el directorio de ningun despliegue.
adquirir_candado() {
  local quien="$1" intento=0 info
  while ! "${SSH[@]}" "mkdir $DEPLOY_LOCK_DIR 2>/dev/null && echo '${TAG:-?} $quien $(date -u +%FT%TZ)' > $DEPLOY_LOCK_DIR/info"; do
    info="$("${SSH[@]}" "cat $DEPLOY_LOCK_DIR/info 2>/dev/null" || true)"
    if "${SSH[@]}" "test -n \"\$(find $DEPLOY_LOCK_DIR -maxdepth 0 -mmin +$DEPLOY_LOCK_ABANDONO_MIN 2>/dev/null)\""; then
      echo ">> candado abandonado (${info:-sin info}); se libera." >&2
      "${SSH[@]}" "rm -rf $DEPLOY_LOCK_DIR" || true
      continue
    fi
    intento=$((intento + 1))
    if ((intento > DEPLOY_LOCK_INTENTOS)); then
      echo "otro despliegue lleva demasiado con el candado (${info:-sin info}); abortando." >&2
      return 1
    fi
    echo ">> otro despliegue en curso (${info:-sin info}); esperando..." >&2
    sleep "$DEPLOY_LOCK_PAUSA"
  done
  CANDADO_TOMADO=1
}

# Un despliegue largo (varios motores esperados uno a uno) renueva el candado entre pasos para que
# nadie lo tome por abandonado mientras sigue vivo.
renovar_candado() {
  [[ "$CANDADO_TOMADO" == "1" ]] || return 0
  "${SSH[@]}" "touch $DEPLOY_LOCK_DIR" >/dev/null 2>&1 || true
}

liberar_candado() {
  [[ "$CANDADO_TOMADO" == "1" ]] || return 0
  "${SSH[@]}" "rm -rf $DEPLOY_LOCK_DIR" >/dev/null 2>&1 || true
  CANDADO_TOMADO=0
}

# es_commit <tag>: cierto si el tag tiene forma de hash y es un commit de este repositorio.
es_commit() {
  [[ "$1" =~ ^[0-9a-f]{7,40}$ ]] && git cat-file -e "$1^{commit}" 2>/dev/null
}

# guardia_retroceso <proyecto> <tag> <servicio>...
#
# Desplegar un commit ANTERIOR al que ya corre es un retroceso que nada delata: el deploy termina
# "bien" y el sintoma es una funcionalidad que desaparece horas despues. Basta con dos sesiones
# desplegando a la vez desde commits distintos. Se llama dos veces: antes del build, para fallar
# pronto, y con el candado en la mano, que es la que cierra la carrera. Un rollback a proposito se
# declara con DEPLOY_ALLOW_ROLLBACK=1.
#
# El contenedor se busca por etiqueta de compose y no por nombre: Docker lo renombra cuando una
# recreacion se cruza consigo misma. Solo se juzgan imagenes etiquetadas por commit; latest o una
# imagen fijada por version (redis) no dicen de que commit vienen.
guardia_retroceso() {
  local proyecto="$1" tag="$2"; shift 2
  [[ "${DEPLOY_ALLOW_ROLLBACK:-0}" == "1" ]] && return 0
  local corriendo retrocesos=() svc tag_vivo
  corriendo="$("${SSH[@]}" "docker ps --filter label=com.docker.compose.project=$proyecto --format '{{.Label \"com.docker.compose.service\"}} {{.Image}}'" || true)"
  for svc in "$@"; do
    tag_vivo="$(awk -v s="$svc" '$1==s {n=split($2,a,":"); if (n>1) print a[n]; exit}' <<<"$corriendo")"
    [[ -z "$tag_vivo" || "$tag_vivo" == "$tag" || ! "$tag_vivo" =~ ^[0-9a-f]{7,40}$ ]] && continue
    if ! git cat-file -e "$tag_vivo^{commit}" 2>/dev/null; then
      echo ">> aviso: $svc corre $tag_vivo, commit desconocido en este repo; no se puede comparar." >&2
      continue
    fi
    if git merge-base --is-ancestor "$tag" "$tag_vivo" 2>/dev/null; then
      retrocesos+=("$svc ($tag_vivo -> $tag)")
    fi
  done
  [[ ${#retrocesos[@]} -eq 0 ]] && return 0
  echo "RETROCESO DE VERSION: el commit a desplegar ($tag) es ANTERIOR al que ya corre en:" >&2
  printf '  %s\n' "${retrocesos[@]}" >&2
  echo "desplegar asi retiraria de produccion lo publicado despues de $tag." >&2
  echo "si es un rollback a proposito, repite con DEPLOY_ALLOW_ROLLBACK=1;" >&2
  echo "si no, despliega desde HEAD (o haz pull/rebase primero)." >&2
  return 1
}

# servicios_sin_commit <proyecto> <servicio>...
#
# Servicios cuyo contenedor corre una imagen sin etiqueta de commit de este repositorio (":latest",
# o una compilada en el servidor). De esas no se puede saber que codigo corren: ni la guardia de
# retroceso ni la verificacion de imagen pueden juzgarlas, porque las dos comparan commits. Quien
# despliega las incluye para que salgan de ahi; es lo que migra un servidor anterior al etiquetado
# por commit sin lista escrita a mano, y en cuanto todos corren su commit no selecciona nada.
servicios_sin_commit() {
  local proyecto="$1"; shift
  local corriendo svc tag_vivo
  corriendo="$("${SSH[@]}" "docker ps --filter label=com.docker.compose.project=$proyecto --format '{{.Label \"com.docker.compose.service\"}} {{.Image}}'" || true)"
  for svc in "$@"; do
    tag_vivo="$(awk -v s="$svc" '$1==s {n=split($2,a,":"); if (n>1) print a[n]; exit}' <<<"$corriendo")"
    [[ -z "$tag_vivo" ]] && continue
    es_commit "$tag_vivo" || echo "$svc"
  done
}

# verificar_imagen_desplegada <proyecto> <tag> <servicio>...
#
# Imagen correcta no es lo mismo que despliegue aplicado: un pull que no trajo nada, un compose que
# reutilizo la imagen anterior o un up que no recreo dejan el despliegue "correcto" con codigo viejo
# dentro, y eso ya ha pasado en este repositorio. Se comprueba en vez de confiar.
#
# Por etiqueta de compose y no por nombre: Docker renombra el contenedor a "<id>_<nombre>" cuando
# una recreacion se cruza consigo misma, y entonces la comprobacion no encuentra nada y da el
# despliegue por no verificado.
verificar_imagen_desplegada() {
  local proyecto="$1" tag="$2"; shift 2
  local svc real fallo=0
  for svc in "$@"; do
    real="$("${SSH[@]}" "docker ps -a --filter label=com.docker.compose.project=$proyecto --filter label=com.docker.compose.service=$svc --format '{{.Image}}' | head -1" || true)"
    case "$real" in
      *":$tag") ;;
      "") echo "!! $svc: no se pudo verificar la imagen del contenedor" >&2 ;;
      *)
        echo "!! $svc corre '$real' y se esperaba el tag $tag" >&2
        echo "   el despliegue NO quedo aplicado para ese servicio" >&2
        fallo=1
        ;;
    esac
  done
  return $fallo
}

# registrar_despliegue <directorio remoto> <plano> <tag> <elemento>...
#
# Deja una linea en <directorio remoto>/.deploy-log por despliegue terminado: fecha UTC, plano
# (plataforma | motores), commit, que se recreo y desde donde se lanzo. .deployed-tag y
# .deployed-tags dicen que corre AHORA; este fichero dice como se llego ahi, y vive en el servidor,
# no en la memoria de quien desplego. Solo se anade, nunca se reescribe. Que no se pueda escribir
# no deshace el despliegue, que ya esta hecho: se avisa y se sigue. Lo lee scripts/estado-produccion.sh.
registrar_despliegue() {
  local dir="${1:?directorio}" plano="${2:?plano}" tag="${3:?tag}" linea origen
  shift 3
  origen="${USER:-desconocido}@$(hostname -s 2>/dev/null || echo desconocido)"
  printf -v linea '%s\t%s\t%s\t%s\t%s' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$plano" "$tag" "${*:-solo-ficheros}" "$origen"
  if ! printf '%s\n' "$linea" | "${SSH[@]}" "cat >> $dir/.deploy-log"; then
    echo ">> aviso: no se pudo anadir el despliegue a $dir/.deploy-log (el despliegue esta hecho)" >&2
  fi
  return 0
}
