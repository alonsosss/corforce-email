"""Comprobaciones de forma del despliegue de la plataforma (las ejecuta check-deploy-preflight.sh).

Van en Python y no en el guion porque son lecturas de scripts_y_de_compose con expresiones
regulares, que en bash quedan ilegibles. Lo que vigilan: que ningun transporte vuelva a enviar una
etiqueta flotante, que los dos verifiquen la imagen del contenedor, que la deteccion incluya los
servicios que corren una imagen sin commit y que el override del transporte save cubra todos los
servicios con build, con etiqueta obligatoria y sin buscar en ningun registro.
"""
import os
import re
import sys

raiz = sys.argv[1]


def leer(rel):
    with open(os.path.join(raiz, rel), encoding="utf-8") as fh:
        return fh.read()


deploy = leer("scripts/deploy-ecr.sh")
save = leer("docker-compose.images.save.yml")
base = leer("docker-compose.yml")
fallos = []

if "app-%s:latest" in deploy:
    fallos.append("deploy-ecr.sh: el transporte save sigue enviando app-<svc>:latest (etiqueta flotante)")
if deploy.count('verificar_imagen_desplegada app "$TAG" "${SVCS[@]}" || exit 1') != 2:
    fallos.append("deploy-ecr.sh: los dos transportes no verifican que el contenedor corre la imagen del commit")
if deploy.count("up -d --no-deps --no-build") != 2:
    fallos.append("deploy-ecr.sh: algun transporte recrea sin --no-build (el servidor no compila nunca)")
if 'servicios_sin_commit app "${TODOS[@]}"' not in deploy:
    fallos.append("deploy-ecr.sh: la deteccion no incluye los servicios que corren una imagen sin commit")

# El camino save es la rama else del if de transporte, hasta su fi.
ini = deploy.find('if [[ "$TRANSPORT" == "ecr" ]]; then')
sep = deploy.find("\nelse\n", ini)
fin = deploy.find("\nfi\n", sep)
camino_save = deploy[sep:fin] if -1 < ini < sep < fin else ""
marcas = (
    'export DEPLOY_TAG="$TAG"',
    "-f docker-compose.images.save.yml",
    'IMAGENES+=("$NS/$s:$TAG")',
    "docker image inspect ${IMAGENES[*]}",
    "with-secrets.sh docker compose $COMPOSE_ARGS -f docker-compose.images.save.yml up -d --no-deps --no-build",
    "ops/ecr/prune-local-images.sh --apply",
)
for marca in marcas:
    if marca not in camino_save:
        fallos.append("deploy-ecr.sh: al transporte save le falta " + repr(marca))

drift = leer("scripts/check-image-drift.sh")
if "core-force-mail/*) NOW[\"$svc\"]=save" not in drift:
    fallos.append("check-image-drift.sh: no reconoce la imagen etiquetada por commit del transporte save")
if '[[ "${PREV[$s]:-}" == "ecr" || "${PREV[$s]:-}" == "save" ]] && REGRESSED+=("$s")' not in drift:
    fallos.append("check-image-drift.sh: no denuncia el retroceso de una imagen desplegada a una compilada en el servidor")

viajan = re.search(r"FICHEROS_SERVIDOR=\((.*?)\)", deploy, re.S)
if not viajan or "docker-compose.images.save.yml" not in viajan.group(1):
    fallos.append("deploy-ecr.sh: docker-compose.images.save.yml no viaja al servidor (FICHEROS_SERVIDOR)")

bloques = re.split(r"^  ([a-z0-9-]+):$", base, flags=re.M)
construidos = [bloques[i] for i in range(1, len(bloques), 2)
               if re.search(r"^    build:", bloques[i + 1], re.M)]
if not construidos:
    fallos.append("docker-compose.yml: no se pudo leer ningun servicio con build")
for nombre in sorted(construidos):
    esperado = (r"^  " + re.escape(nombre) + r":\n    image: core-force-mail/" + re.escape(nombre)
                + r":\$\{DEPLOY_TAG:\?[^}]*\}\n    pull_policy: never$")
    if not re.search(esperado, save, re.M):
        fallos.append("docker-compose.images.save.yml: " + nombre
                      + " sin imagen core-force-mail/<svc>:${DEPLOY_TAG:?...} y pull_policy: never")

for fallo in fallos:
    print("  FALLA: " + fallo, file=sys.stderr)
sys.exit(1 if fallos else 0)
