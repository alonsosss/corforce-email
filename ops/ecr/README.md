# ECR — registro central de imágenes de Core Force Mail

Publica en Amazon ECR las imágenes propias (servicios con `build:` en
`docker-compose.yml`) para que cualquier servidor (prod/dev/ia/reportes) las
consuma por `pull` en lugar de reconstruirlas. El build sigue ocurriendo en el
EC2 de producción; estos scripts solo etiquetan y publican lo ya construido.

- **Cuenta:** descubierta con STS (no fijada en código).
- **Región:** `us-east-1` (override con `ECR_REGION`).
- **Namespace:** `core-force` → repos `core-force/<servicio>`.
- **Auth:** rol IAM de la instancia `core-force-mail-ec2-role` (política
  `core-force-ecr-build-push`). No se usan claves estáticas.

## Uso (en el EC2)

El usuario `ubuntu` necesita `sudo` para docker, por eso `DOCKER="sudo docker"`.

```bash
# 1) Crear los repos (idempotente) + lifecycle policy
ECR_REGION=us-east-1 ./create-repos.sh

# 2) Login + publicar todas las imágenes con tag latest
DOCKER="sudo docker" ./push.sh

# Publicar una versión concreta (además reetiqueta latest)
DOCKER="sudo docker" IMAGE_TAG=2026-06-29 ./push.sh

# Publicar solo algunos servicios
DOCKER="sudo docker" ./push.sh gateway identity fe-shell
```

## Lifecycle policy

`lifecycle-policy.json`: expira imágenes sin tag a los 7 días y conserva las
últimas 10 etiquetadas por repo, para acotar el costo de almacenamiento.

**Los repos creados desde que se separaron las credenciales no la tienen.** Ni
`core-force-deploy-local` (el usuario del PC que publica) ni `core-force-mail-ec2-role`
pueden llamar a `ecr:PutLifecyclePolicy`, así que `create-repos.sh` crea el repo
y falla al aplicarle la retención. Los repos antiguos sí la llevan porque se
crearon con una identidad de administrador. `core-force/scale` es el primero que
quedó sin ella; lo será cada servicio nuevo hasta que se conceda el permiso.

No da síntoma: las imágenes se acumulan sin límite y sólo aparece en la factura.
Adjunta `iam-policy-lifecycle.json` a cualquiera de las dos identidades y
reejecuta `create-repos.sh`, que es idempotente y aplica la política a todos los
repos de una pasada. Para comprobar cuáles siguen sin ella hace falta el mismo
permiso de lectura que la política concede.

## Escaneo de vulnerabilidades (bloqueado por permisos)

`enable-scanning.sh` activa el escaneo continuo de ECR sobre `core-force/*` e informa los
hallazgos. **Hoy no se puede ejecutar**: el rol de la instancia (`core-force-mail-ec2-role`)
puede publicar imágenes pero no configurar el escaneo —`ecr:GetRegistryScanningConfiguration`
y `ecr:PutRegistryScanningConfiguration` están denegados—. Adjunta
`iam-policy-scanning.json` al rol y el script funciona sin cambios.

Se pide `ENHANCED` con `CONTINUOUS_SCAN` y no `BASIC`: el básico mira la imagen una sola
vez, al subirla, así que una imagen que era limpia ayer y hoy tiene un CVE nuevo no se lo
dice a nadie.

Complementa a `govulncheck`, no lo duplica: govulncheck analiza nuestro código Go y sus
dependencias, esto analiza la imagen entera. Las imágenes `FROM scratch` saldrán limpias
por construcción (no traen paquetes de sistema); las de los micro-frontends, que corren
sobre nginx, son las que este escaneo cubre de verdad.

## Pull desde otros servidores (pendiente)

Los servidores solo-pull necesitan una política IAM de lectura
(`ecr:GetAuthorizationToken`, `BatchGetImage`, `GetDownloadUrlForLayer`,
`BatchCheckLayerAvailability`) y un compose que referencie
`image: <account>.dkr.ecr.us-east-1.amazonaws.com/core-force/<servicio>:<tag>`
sin sección `build:`. Se definirá al aprovisionar el primer servidor de pull.
