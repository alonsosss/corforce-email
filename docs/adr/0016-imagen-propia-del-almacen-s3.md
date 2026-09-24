# ADR 0016: Imagen propia del almacen S3, compilada desde el codigo fuente de Silo

## Estado

Aceptado (2026-09-24). Sustituye la imagen `quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z.hotfix.7aa24e772`
que fijaban `docker-compose.selfhosted.yml` (servicios `minio`, `minio-volumen` y `minio-init`) y
`deploy/mail/docker-compose.e2e.yml` (perfil `ficheros`). Pendiente de desplegar en produccion con el
procedimiento de la ultima seccion.

## Contexto

El almacen de objetos del perfil autoalojado (`docs/adr/0012`, `docs/adr/0014`: imagenes de las plantillas
servidas por el gateway en `/media/public/*` y ficheros grandes de `mail-files` en
`private/<empresa>/mail-files/*`) corria la imagen oficial de MinIO fijada por digest. Comprobado el
2026-09-24:

- `quay.io/minio/minio` responde 401 "Requires authentication" para esa etiqueta y para
  `RELEASE.2025-09-07T16-13-09Z`, y `docker.io/minio/minio` tambien niega el token anonimo. La CI
  (`make e2e-mail`, flujo "Motores de correo (punta a punta)") falla en "compose up de MinIO" con
  `unauthorized`; produccion solo funciona porque tiene la imagen en su cache. Un servidor nuevo, una
  restauracion o una limpieza de imagenes dejarian la plataforma sin almacen.
- El repositorio publico `github.com/minio/minio` esta en mantenimiento: su ultima etiqueta es
  `RELEASE.2025-10-15T17-29-55Z` y ya no publica binarios ni imagenes. El commit del hotfix `7aa24e772`
  no es publico.
- Despues de esa etiqueta MinIO ha publicado avisos de seguridad corregidos solo en su producto de pago
  (AIStor), que afectan a todas las versiones abiertas, incluido el hotfix que corria produccion (compilado
  en 2026-03): CVE-2026-41145 y CVE-2026-40344 (alta: escritura de objetos sin firma valida conociendo solo
  una clave de acceso), CVE-2026-34204 (alta: inyeccion de metadatos de cifrado por cabeceras de
  replicacion), CVE-2026-39414 (alta: denegacion de servicio en S3 Select), CVE-2026-42600 (media:
  recorrido de rutas en la API interna entre nodos).

## Opciones evaluadas

1. **Compilar MinIO desde su codigo fuente** (`RELEASE.2025-10-15T17-29-55Z`, la release publica mas
   cercana al hotfix, con la correccion de CVE-2025-62506). Viable: se probo, compila con CGO desactivado
   en unos dos minutos y los binarios son reproducibles. Descartada como destino porque deja abiertos los
   fallos altos de 2026 anteriores, que nadie corregira en ese codigo; mantener nuestros propios parches
   de seguridad sobre un servidor de almacenamiento es justo lo que la regla de "motores maduros, capa
   propia" evita.
2. **La imagen publicada de Silo** (`docker.io/pgsty/silo`). Mantenida, pero vuelve a depender de un
   registro de terceros, que es el fallo que hay que quitar.
3. **Otro almacen S3-compatible** (Garage, SeaweedFS, Versity S3 Gateway, RustFS). Todos tienen imagen
   publica, pero exigen migrar los datos (copiar cada objeto con `mc mirror` o `rclone` a un formato de
   disco distinto, con parada de escritura o doble escritura), rehacer `selfhosted/minio/init.sh` (otra API
   de administracion y otro modelo de permisos: Garage, por ejemplo, da permisos por clave y bucket y no
   puede expresar "borrar solo en `private/*/mail-files/*`", que es la frontera que protege las imagenes de
   correos ya enviados), revalidar `pkg/objectstore` y el gateway contra otra implementacion de S3, y
   cambiar los respaldos y su restauracion (`ops/backup/*`), que hoy archivan el volumen de MinIO tal cual.
   Coste alto y riesgo de regresion sin ninguna ventaja que no de la opcion 4.
4. **Compilar Silo desde su codigo fuente** (`github.com/pgsty/silo`, bifurcacion mantenida de MinIO, AGPL,
   con releases firmadas y un registro publico de avisos). Corrige todos los avisos anteriores (por ejemplo,
   `PutObjectHandler` ya no decide si verifica la firma por la mera presencia de la cabecera
   `Authorization`, que es CVE-2026-41145) y declara congelados el formato en disco (`.minio.sys`), las
   variables `MINIO_*`, las rutas `/minio/health/*`, los modulos Go y las API S3 y de administracion. Su
   cliente (`github.com/pgsty/mc`) es la bifurcacion de `mc` que fija su propio `go.mod`.

## Decision

Opcion 4. El servicio sigue llamandose `minio` (volumen `minio-data`, variables `MINIO_*`, guiones de
`selfhosted/minio/`), pero su imagen es propia:

- **Receta**: `selfhosted/minio/imagen/Dockerfile`, multietapa. Go (`golang:1.27.1-alpine3.24`, lo que
  exige el `go.mod` de Silo) y la base (`alpine:3.24.2`) fijados por digest; Silo y su cliente fijados por
  etiqueta y commit completo, descargados solo en ese commit y comprobados (`git rev-parse HEAD`); modulos
  Go verificados por `go.sum` y la base de sumas publica; `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` y
  las mismas marcas `-X` de version que ponen sus Makefile, sin rutas del equipo que compila. Dos builds en
  frio dieron binarios identicos byte a byte.
- **Imagen**: los binarios `silo` (con el enlace `minio`, que es lo que ejecuta `selfhosted/minio/entrypoint.sh`)
  y `mc`, busybox de la base para `sh`, `stat`, `chown`, `cat`, `tr` y `wget` (el arranque, `minio-init`,
  `minio-volumen` y el chequeo de salud), sin `apk` ni `curl`, usuario `10001:10001` sin root y `/data` suyo
  con 0700. Etiquetas OCI de version, commit, fuente y licencia.
- **Etiqueta**: `core-force-mail/minio:silo-<release>-<12 hex del sha256 del Dockerfile>`, que calcula
  `scripts/imagen-minio.sh --referencia`. Cualquier cambio de la receta es otra etiqueta, asi que un
  servidor que tiene una etiqueta tiene exactamente esa imagen. `ops/scaffold/check-selfhosted-profile.sh`
  (en `make checks`) exige que los cinco servicios de los dos compose la referencien, con
  `pull_policy: never` (la imagen no esta en ningun registro: nunca se busca en uno), que las bases vayan por
  digest, que las fuentes vayan por commit completo y que `deploy-ecr.sh` la envie antes de levantar la
  infraestructura.
- **Contexto de build vacio**: `scripts/imagen-minio.sh --construir` pasa el Dockerfile por la entrada
  estandar; la imagen no depende de ningun otro fichero del repositorio.
- **Quien la construye**: el puesto de trabajo (despliegue), la CI y las pruebas (`ops/e2e/mail.sh`,
  `ops/scaffold/test-selfhosted-backup.sh`), siempre por `scripts/imagen-minio.sh --construir`, que no hace
  nada si la imagen ya esta. El servidor nunca compila.
- **Como llega al servidor**: `scripts/deploy-ecr.sh`, dentro de `aplicar_infra` y antes del `up` de la
  infraestructura, pregunta al servidor si tiene la etiqueta; si no, la construye aqui y la envia con
  `docker save | gzip | ssh docker load`, el mismo transporte que las imagenes de los servicios. El
  despliegue de `.github/workflows/release.yml` (el de AWS por SSM) no la envia: el perfil autoalojado se
  despliega con `deploy-ecr.sh`.

## Licencia

Silo y su cliente son AGPL-3.0-or-later, como MinIO. Se usan sin modificar y como servicio interno: MinIO no
tiene puerto en el host ni en el borde y ningun usuario interactua con el por red (el gateway y
`mail-files` le hablan dentro de `mail-internal`), asi que la seccion 13 de la AGPL no obliga a ofrecer el
codigo a los usuarios de la plataforma. La imagen solo se distribuye a nuestro propio servidor. Aun asi
lleva en `/licenses/` la licencia, los `CREDITS` y `NOTICE` de los dos proyectos y `FUENTE.txt`, que dice la
etiqueta y el commit exactos de cada uno y donde esta su codigo fuente, y las etiquetas
`org.opencontainers.image.licenses=AGPL-3.0-or-later` y `org.opencontainers.image.source`. Si algun dia la
imagen se entregara a un tercero, esa informacion basta para cumplir la obligacion de ofrecer el codigo
fuente correspondiente. Nada del codigo de la plataforma enlaza con Silo: `pkg/objectstore` usa
`minio-go` (Apache-2.0) por la red.

## Procedimiento de actualizacion

1. Leer el registro de avisos de Silo (`https://silo.pgsty.com/about/security-advisories/`) y las notas de
   la release nueva (`https://github.com/pgsty/silo/releases`). Un aviso alto o critico que afecte a lo que
   usamos (un solo nodo, S3 basico, IAM de usuarios y politicas; sin OIDC, LDAP, replicacion ni S3 Select)
   se atiende en los plazos del mantenimiento: 72 horas critico, 14 dias alto.
2. En el Dockerfile: `SILO_RELEASE` y `SILO_COMMIT` de la etiqueta nueva
   (`git ls-remote --tags https://github.com/pgsty/silo`, el commit es el de la linea `^{}`); `MC_COMMIT`
   el del `replace github.com/minio/mc` de su `go.mod` y `MC_RELEASE` su etiqueta en `pgsty/mc`; la imagen
   de Go que pida su directiva `go` y la base Alpine vigente, las dos por digest
   (`docker buildx imagetools inspect <imagen>:<version>`).
3. `scripts/imagen-minio.sh --referencia` da la etiqueta nueva: ponerla en los tres servicios de
   `docker-compose.selfhosted.yml` y los dos de `deploy/mail/docker-compose.e2e.yml`.
   `make checks` falla hasta que coinciden.
4. `scripts/imagen-minio.sh --construir` y `make e2e-mail` (la CI lo corre porque cambia `selfhosted/minio/**`):
   sube y baja ficheros de `mail-files` por el gateway contra la imagen nueva con `minio-init` real.
   `bash ops/scaffold/test-selfhosted-backup.sh` la usa de destino S3 de los respaldos.
5. Desplegar con el procedimiento de produccion de la seccion siguiente.

## Procedimiento de produccion (primera vez y cada actualizacion)

Desde el puesto de trabajo, con el commit en `main` y la CI en verde. `<srv>` es el servidor,
`/opt/core-force-mail/app` su `DEPLOY_PATH` y `app` el proyecto de compose.

1. Respaldo previo del volumen, y copia de la imagen que corre, que ya no se puede volver a descargar:

   ```bash
   ssh <srv> 'sudo systemctl start core-force-mail-backup-mail.service && systemctl status --no-pager core-force-mail-backup-mail.service | tail -3'
   ssh <srv> 'docker inspect -f "{{.Config.Image}}" app-minio-1 && docker save "$(docker inspect -f "{{.Config.Image}}" app-minio-1)" | gzip > /opt/core-force-mail/minio-imagen-anterior.tar.gz'
   ```

2. Desplegar la plataforma como siempre (`DEPLOY_HOST=<srv> ... scripts/deploy-ecr.sh`, con servicios o sin
   ellos: `aplicar_infra` corre en los tres caminos). El despliegue construye la imagen aqui si hace falta,
   la envia, y el `up -d` de la infraestructura recrea `minio-volumen` (no hace nada: el volumen ya es del
   uid 10001), `minio` con la imagen nueva sobre el MISMO volumen `app_minio-data` (el volumen no se toca
   ni se recrea; el corte es el arranque del contenedor, unos segundos en los que `/media/public/*` y las
   subidas de `mail-files` responden error) y `minio-init` tras `minio` sano. `esperar-sanos.sh` falla el
   despliegue si `minio` no llega a sano o `minio-init` no sale con 0.

   Solo MinIO, sin desplegar servicios, a mano en el servidor tras enviar la imagen con
   `scripts/imagen-minio.sh --construir && docker save <imagen> | gzip | ssh <srv> 'gunzip | docker load'`
   y sincronizar el commit (el camino de solo ficheros de `deploy-ecr.sh` ya hace las dos cosas):

   ```bash
   cd /opt/core-force-mail/app
   ops/security/secrets/with-secrets.sh docker compose $(ops/maintenance/perfil-despliegue.sh --compose) up -d minio minio-init
   ops/maintenance/esperar-sanos.sh --proyecto app minio minio-init
   ```

3. Comprobar en el servidor:

   ```bash
   docker inspect -f '{{.Config.Image}} {{.State.Health.Status}}' app-minio-1   # la etiqueta nueva, healthy
   docker exec app-minio-1 minio --version                                     # silo RELEASE.<...>
   docker logs app-minio-init-1 | tail -1   # "bucket <MINIO_BUCKET> privado y usuario de servicio con la politica core-force-media"
   ```

   Y con la cuenta raiz, sin que salga por la linea de ordenes (el envoltorio de secretos la pone en el
   entorno de `docker exec`):

   ```bash
   cd /opt/core-force-mail/app
   export MINIO_BUCKET="$(sed -n 's/^MINIO_BUCKET=//p' .env | tail -1)"
   ops/security/secrets/with-secrets.sh sh -c 'docker exec -e MC_HOST_l="http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@127.0.0.1:9000" app-minio-1 sh -c "mc --config-dir /tmp/mc anonymous get l/$MINIO_BUCKET; mc --config-dir /tmp/mc admin user info l $MINIO_ACCESS_KEY; mc --config-dir /tmp/mc admin policy info l core-force-media"'
   ```

   Debe decir `private`, el usuario de servicio `enabled` con solo `core-force-media`, y la politica con
   `s3:DeleteObject` solo sobre `arn:aws:s3:::<bucket>/private/*/mail-files/*`. Por ultimo, una imagen de
   una plantilla ya enviada por `https://<API_ORIGIN>/media/public/<clave>` (200) y una subida y descarga
   de un fichero grande desde el webmail.

4. Vuelta atras. Probado en local con la imagen exacta de produccion (la de quay por digest, que el puesto
   de trabajo aun tenia en cache) y con el mismo arranque y `minio-init` del perfil: un volumen con bucket,
   politica, usuario de servicio y objetos escritos por esa imagen lo abre Silo con los objetos intactos
   (sha256) y la misma clave del usuario de servicio, y despues de que Silo escriba en el, la imagen de
   produccion lo vuelve a abrir y lee lo escrito por Silo. Si Silo fallara, se despliega el commit
   anterior (`DEPLOY_ALLOW_ROLLBACK=1 scripts/deploy-ecr.sh`), cuyo compose vuelve a pedir la imagen de
   quay por digest: el servidor la resuelve de su almacen local mientras no se borre (no la borra nada:
   `ops/maintenance/docker-prune.sh` solo retira imagenes colgantes). La copia del paso 1 es el seguro si
   se hubiera borrado: `gunzip -c /opt/core-force-mail/minio-imagen-anterior.tar.gz | docker load`
   recupera la etiqueta, y si Docker no recupera tambien el digest (depende del almacen de imagenes del
   demonio) se referencia por esa etiqueta. Si el volumen quedara ilegible,
   `ops/backup/restore-mail-volume.sh minio-data --force` con `minio` parado, desde el respaldo del paso 1.

La imagen anterior y su copia se pueden borrar cuando Silo lleve un ciclo de respaldos sin incidencias.

## Consecuencias

- La plataforma ya no depende de ningun registro para el almacen: la CI y un servidor nuevo tienen la
  imagen en cuanto se construye (unos dos minutos en frio en una maquina de 12 nucleos, mas en un runner de
  la CI; con la cache de BuildKit, segundos). La imagen ocupa unos 140 MB y viaja comprimida en unos 46 MiB.
- La etiqueta de la imagen se calcula del Dockerfile; editar la receta sin actualizar los compose falla en
  `make checks`.
- Se cambia de proveedor del servidor de almacenamiento: de MinIO Inc. a una bifurcacion comunitaria.
  Riesgo aceptado frente a la alternativa real, un MinIO abierto sin correcciones. Si Silo dejara de
  mantenerse, la receta y el procedimiento son los mismos para otra bifurcacion con el mismo formato en
  disco; cambiar a un almacen distinto es la opcion 3, con su migracion de datos.
- Seguimos atentos a los avisos de Silo nosotros: no hay un escaneo automatico de esta imagen
  (`ops/security/escanear-motores.sh` solo cubre `deploy/mail`).
