# ECR: registro central de imágenes de Core Force Mail

Publica en Amazon ECR las imágenes propias (servicios con `build:` en
`docker-compose.yml`) para que los servidores las consuman por `pull`. Las imágenes se
compilan fuera del servidor: en el PC con `scripts/deploy-ecr.sh` o en GitHub Actions con
`.github/workflows/release.yml`. **Nunca se compila en el servidor**
(`docs/Operacion_Despliegue.md`, 5): compite por CPU con lo que está sirviendo y deja
imágenes locales que `scripts/check-image-drift.sh` delata.

- **Cuenta:** descubierta con STS (no fijada en código).
- **Región:** `us-east-1` (override con `ECR_REGION`).
- **Namespace:** `core-force-mail` → repos `core-force-mail/<servicio>`.
- **Auth:** sin claves estáticas en el servidor. El PC publica con el usuario
  `core-force-deploy-local` y el servidor usa el rol de la instancia
  `core-force-mail-ec2-role`; los permisos de ambos los declara `ops/aws/setup-iam.sh`.
  CI publica con el rol OIDC `github-ecr-push` (`ops/aws/setup-github-oidc.sh`).

## Uso (desde el PC)

El despliegue no llama a estos scripts: `scripts/deploy-ecr.sh` compila, etiqueta por
commit y publica. Lo que queda aquí prepara el registro y cubre casos puntuales, con la
credencial del PC:

```bash
# 1) Crear los repos (idempotente) + lifecycle policy
ops/ecr/create-repos.sh

# 2) Publicar imágenes ya compiladas en esta máquina (<COMPOSE_PROJECT>-<servicio>:latest)
ops/ecr/push.sh

# Publicar una versión concreta (además reetiqueta latest)
IMAGE_TAG=2026-06-29 ops/ecr/push.sh

# Publicar solo algunos servicios
ops/ecr/push.sh gateway identity
```

## Lifecycle policy

`lifecycle-policy.json`: expira imágenes sin tag a los 7 días y conserva las
últimas 10 etiquetadas por repo, para acotar el costo de almacenamiento.

Aplicarla exige `ecr:PutLifecyclePolicy`, que `ops/aws/setup-iam.sh` concede al usuario
de deploy (política `core-force-ecr-retencion`). Un repo creado por una identidad sin ese
permiso se queda sin retención: `create-repos.sh` lo crea, avisa del fallo al aplicarla y
sigue.

No da síntoma: las imágenes se acumulan sin límite y sólo aparece en la factura.
`create-repos.sh` es idempotente: reejecutarlo con la credencial del PC aplica la política
a todos los repos de una pasada.

## Escaneo de vulnerabilidades

`enable-scanning.sh` activa el escaneo continuo de ECR sobre `core-force-mail/*` e informa los
hallazgos. Configurarlo es una acción de nivel de registro: requiere la política
`core-force-ecr-scanning`, que `ops/aws/setup-iam.sh` concede al rol de la instancia, así
que se ejecuta desde el servidor (o con credenciales de administrador).

Se pide `ENHANCED` con `CONTINUOUS_SCAN` y no `BASIC`: el básico mira la imagen una sola
vez, al subirla, así que una imagen que era limpia ayer y hoy tiene un CVE nuevo no se lo
dice a nadie.

Complementa a `govulncheck`, no lo duplica: govulncheck analiza nuestro código Go y sus
dependencias, esto analiza la imagen entera. Las imágenes `FROM scratch` saldrán limpias
por construcción (no traen paquetes de sistema); la de la aplicación web (`web`), que
corre sobre nginx, es la que este escaneo cubre de verdad.

## Pull desde otros servidores (pendiente)

Los servidores solo-pull necesitan una política IAM de lectura
(`ecr:GetAuthorizationToken`, `BatchGetImage`, `GetDownloadUrlForLayer`,
`BatchCheckLayerAvailability`) y un compose que referencie
`image: <cuenta>.dkr.ecr.<región>.amazonaws.com/core-force-mail/<servicio>:<tag>`
sin sección `build:`. Se definirá al aprovisionar el primer servidor de pull.
