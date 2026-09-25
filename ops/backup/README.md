# Respaldo y recuperación

Dos cosas distintas se respaldan aquí, y las dos hacen falta para volver de un desastre:

* las **bases de datos** (registro, celdas y empresas), un archivo por base;
* los **volúmenes de correo** de `deploy/mail`: los buzones y la clave de `mail_crypt` con la
  que Dovecot los cifra. Sin esa clave, los buzones respaldados son ilegibles. En el perfil
  autoalojado, también el **almacén de objetos** (`minio-data`): las imágenes que los correos ya
  enviados siguen mostrando.

El diseño de datos es **una base por empresa**, y el respaldo lo respeta: un archivo por base, no
una foto del servidor entero. Es la diferencia entre poder devolver a UNA empresa al estado de
ayer y tener que devolver a todas. Un snapshot del disco o del servicio gestionado sirve para el
desastre completo; no sirve para "a esta empresa le borraron una lista de contactos el martes".

## Los guiones

| Script | Qué hace |
|---|---|
| `backup-tenants.sh` | Vuelca cada base (`mail_%`: registro, celdas y empresas) a `-Fc`, la lee entera para comprobar que `pg_restore` la entiende, deja su `.sha256` y la sube al destino externo si hay. Con el almacén en OpenBao, añade su instantánea (`openbao.snap`) a la misma corrida |
| `verify-restore.sh` | Restaura el último volcado en una base desechable y comprueba que trae lo que dice traer. Sin argumentos, una base de cada clase y la instantánea de OpenBao en una instancia desechable (`ops/security/openbao/verificar-instantanea.sh`) |
| `restore-tenant.sh` | Restaura UNA base. Por defecto a una base nueva; sobrescribir exige `--force` y confirmación escrita |
| `backup-mail-volumes.sh` | Archiva los volúmenes de correo (por defecto `crypt-vol` y `vmail-vol`) y, en el perfil autoalojado, `minio-data` de la plataforma; los lee enteros, comprueba que la clave privada de `mail_crypt` corresponde a su pública y sube cifrado |
| `restore-mail-volume.sh` | Restaura un volumen de correo o `minio-data`, por defecto a un volumen NUEVO; sobrescribir el vivo exige `--force`, que ningún contenedor lo monte y confirmación escrita |
| `install-timers.sh` | Instala o repone los temporizadores de systemd en un servidor, aprovisionado o no (`sudo`) |
| `destino-externo.sh` | Biblioteca: configuración, credenciales y cifrado del destino externo S3-compatible |
| `report-metric.sh` | Biblioteca: publica el resultado de cada corrida como métrica |

`verify-restore.sh` es el que importa. Un respaldo que nadie restauró nunca es una suposición, no
una garantía; esto la convierte en un hecho comprobado cada semana. Sin argumentos toma la última
corrida completa (la que respaldó el registro) y verifica **una base de cada clase**: el registro,
la celda con el volcado más grande y la empresa con el volcado más grande.

Qué comprueba, sin suponer filas de negocio (una empresa recién dada de alta no tiene ninguna y su
respaldo vale lo mismo):

- la suma `.sha256` del volcado antes de tocarlo: un fichero dañado en disco no llega a
  `pg_restore`;
- que cada esquema y cada tabla del índice del volcado (`pg_restore --list`) existe en la base
  restaurada: `restore-tenant.sh` muestra los errores de `pg_restore`, pero no se detiene en ellos;
- `platform.event_outbox`, que existe en las tres clases de base;
- empresa y registro: el historial `public.schema_migrations` con filas (son datos, no esquema) y
  el esquema que declara la cabecera `-- Schema:` de cada migración registrada, leída de
  `migrations/` del árbol desplegado; en el registro, además, al menos un usuario;
- celda: sus migraciones se aplican a mano y no dejan historial, así que se exigen los esquemas que
  declaran todas las migraciones de celda del árbol desplegado. Una migración de celda con un
  esquema nuevo, desplegada y aún sin aplicar, hace fallar la verificación de esa celda hasta que
  se aplica.

## La herramienta de Postgres, según el perfil

En el perfil **autoalojado** (`DEPLOY_PROFILE=selfhosted`) `postgres-primary` no se publica en el
host: su nombre solo existe en la red interna de Docker y exige TLS. Por eso `cf_psql`,
`cf_pg_dump` y `cf_pg_restore` (`ops/db/pg-credentials.sh`) corren en un contenedor efímero **de la
misma imagen que el Postgres en marcha**, en su red, con `sslmode=verify-full` contra la CA interna
(`INTERNAL_TLS_DIR/publico/ca.crt`), sin capacidades, de solo lectura y con el uid del usuario del
respaldo. La contraseña entra por el entorno (`-e PGPASSWORD`, solo el nombre), nunca por
argumentos.

Que el cliente venga de la imagen del servidor no es un detalle: el `pg_dump` 17 del host
(Debian 13) escribe el formato 1.16 y el `pg_restore` 16 de la imagen **no lo lee**
(`unsupported version (1.16) in file header`). Respaldar con una herramienta y restaurar con otra
convierte la copia en una apuesta; así las tres —volcado, verificación y restauración— son siempre
la misma versión, y siguen al servidor cuando se actualice.

En el perfil **aws** nada de esto cambia: se usan los binarios del host contra RDS, como hasta
ahora, y `PGSSLMODE` no se toca.

Por el mismo camino van los demás guiones que abren la base: `ops/db/apply-migration.sh`,
`ops/apply-all-canonical.sh`, `ops/db/cell-service-role.sh`, `ops/db/cell-engine-role.sh`,
`ops/db/tenant-service-role.sh` y `ops/db/bootstrap-platform.sh`. Tres consecuencias de hacerlo en
contenedor, resueltas ahí:

- **la entrada estándar es de quien la pide**, y por eso hay dos familias de funciones:
  `cf_psql`, `cf_pg_dump` y `cf_pg_restore` **no** leen la entrada estándar del guion (se sustituye
  por `/dev/null`), y `cf_psql_entrada` la recibe porque ES su SQL o su fichero (un heredoc,
  `<fichero`, una tubería). `docker run -i` se lleva la entrada entera, y eso rompió en producción
  `tenant-service-role.sh --all`: el bucle `while read svc; do …; done < <(servicios_de_empresa)`
  perdió la lista en la primera consulta, creó **un solo rol** y salió con 0; los diez servicios
  restantes se quedaron sin rol y PgBouncer devolvía `password authentication failed` (Postgres
  dice eso también cuando el rol no existe), con 404 en dominios, contactos, plantillas, tareas y
  auditoría. Dentro de un bucle `while read`, una función de la familia "con entrada" solo es
  segura con su propia redirección, que es justo lo que la hace "con entrada";
- las migraciones entran por la **entrada estándar** (no con `-f`), así que el fichero no tiene
  que existir dentro del contenedor;
- los secretos que el SQL necesita —el verificador SCRAM de un rol, la contraseña del primer
  superadmin— se pasan por el **entorno** (`cf_pg_pasar_entorno`) y se leen con `\getenv`, nunca
  con `-v`: con `-v` quedarían en la línea de órdenes de `psql`, en la de `docker run` y en
  `docker inspect` del contenedor efímero. El verificador SCRAM lo sigue calculando `python3` en
  el host, que es donde está la contraseña; a Postgres solo viaja el verificador.

Cada uno conserva el respaldo al `psql` del host (`declare -F cf_psql || cf_psql()`, y lo mismo
para `cf_psql_entrada`) para cuando se ejecuta con `PGHOST` ya en el entorno, como en `make e2e`.

`ops/scaffold/check-backups.sh` ata las dos reglas de la entrada estándar: una orden con heredoc o
`<fichero` tiene que usar la familia "con entrada", y una de esa familia dentro de un bucle
`while read` tiene que traer su redirección. Además lo comprueba ejecutando: un bucle de tres
líneas con `cf_psql` dentro tiene que dar tres vueltas, y el heredoc de `cf_psql_entrada` tiene que
llegar al contenedor.

## Uso

```bash
cd /opt/core-force-mail/app

ops/backup/backup-tenants.sh                      # todas las bases
ops/backup/backup-tenants.sh mail_tenant_demo      # una sola
ops/backup/backup-mail-volumes.sh                  # buzones y claves de mail_crypt

# Recuperar: por defecto NO toca la base viva, crea mail_tenant_demo_restore_<fecha>
ops/backup/restore-tenant.sh mail_tenant_demo
ops/backup/restore-tenant.sh mail_tenant_demo s3://<bucket>/postgres/mail_tenant_demo/<sello>.dump.gpg
ops/backup/restore-tenant.sh mail_tenant_demo --into mail_prueba

# Reemplazar la base viva (irreversible; pide escribir el nombre para confirmar)
ops/backup/restore-tenant.sh mail_tenant_demo --force

ops/backup/verify-restore.sh                      # registro + celda + empresa
ops/backup/verify-restore.sh mail_tenant_demo     # una base concreta

# Volúmenes de correo: a un volumen nuevo, o encima del vivo con los motores parados
ops/backup/restore-mail-volume.sh vmail-vol --into mail_vmail-prueba
ops/backup/restore-mail-volume.sh crypt-vol s3://<bucket>/correo/crypt-vol/<sello>.tar.gz.gpg
ops/backup/restore-mail-volume.sh vmail-vol --force
ops/backup/restore-mail-volume.sh minio-data --into app_minio-data-prueba   # objetos de MinIO
```

Casi siempre lo correcto es restaurar a una base o a un volumen nuevos y sacar de ahí lo que falta.
Sobrescribir lo vivo descarta todo lo hecho desde el respaldo.

Tras restaurar los buzones encima del volumen vivo hay que reconstruir los índices, que viven en
otro volumen y no se respaldan: `docker exec dovecot-mail doveadm force-resync -A '*'`.

## Volúmenes de correo: qué entra y con qué consistencia

| Volumen | Se respalda | Por qué |
|---|---|---|
| `crypt-vol` | **Sí** | La clave global de `mail_crypt` (`ecprivkey.pem`, `ecpubkey.pem`). Dovecot cifra con ella cada mensaje y cada adjunto: sin la clave, los buzones respaldados no se pueden leer. Se comprueba que la privada corresponde a la pública. Es diminuta y **nunca sale del servidor sin cifrar**, tampoco a AWS |
| `vmail-vol` | **Sí** | Los buzones (Maildir). Se excluye `_garbage` (lo que Dovecot ya borró) |
| `vmail-index-vol` | No | Índices y caché de búsqueda: Dovecot los reconstruye (`doveadm force-resync`) |
| `postfix-vol` | No | La cola. Un volcado de una cola viva es incoherente por definición, y un mensaje entrante no entregado lo reintenta el emisor. Antes de una parada planificada, `postfix flush` |
| `redis-vol` (motores) | No | Bayes de Rspamd, listas grises y contadores. Se reaprende; perderlo empeora la clasificación unos días, no pierde correo |
| `rspamd-vol` | No | Estado y mapas de Rspamd, que se regeneran del directorio de la celda y de Redis |
| `clamd-db-vol` | No | Firmas de ClamAV: `freshclam` las vuelve a bajar |
| `ssl-vol`, `acme-challenge-vol` | No | Certificados públicos: `acme-mail` los reemite |
| `acme-conf-vol` | No | Cuenta de ACME y **credencial DNS del proveedor**. Se vuelve a crear con la credencial del almacén; respaldarlo sería poner una credencial de terceros en el archivo, y solo tendría sentido cifrado |
| `postfix-tlspol-vol` | No | Caché de políticas TLS |
| `minio-data` (plataforma, `<COMPOSE_PROJECT_NAME>_minio-data`) | **Sí**, solo en el perfil autoalojado | Los objetos de MinIO: las imágenes de las plantillas, que los correos ya enviados siguen mostrando y no se pueden regenerar. Se excluye `.minio.sys/tmp`. Lleva además la configuración IAM de MinIO con la clave del usuario de servicio **en claro**: como `crypt-vol`, **nunca sale del servidor sin cifrar**. Tras restaurarlo, `minio-init` vuelve a fijar la clave del almacén de secretos en el siguiente despliegue |

Los buzones se archivan **en caliente**: Maildir escribe en `tmp/` y renombra, así que ningún
mensaje queda a medias, pero uno que cambie de carpeta o de marcas durante el archivado puede
faltar en ESA copia (está en la siguiente). `tar` lo señala con el código 1, que aquí es un aviso.
Un archivo sin ese riesgo exige `doveadm backup` buzón a buzón (más lento, y duplica el espacio) o
parar Dovecot; hoy no se hace ninguna de las dos, y queda anotado como mejora.

## Lo que el respaldo no trae y hay que guardar aparte

Los volcados (`mail_%`) llevan TODOS los esquemas de cada base, incluidos los de `mail_dav`,
`mail_migration` y `audit` (`chain_anchors` y las filas de la cadena de hash de la versión 2), y
`verify-restore.sh` exige los esquemas de las migraciones aplicadas: un servicio nuevo con su esquema
queda cubierto sin tocar los guiones. Lo que **no** viaja en un volcado, y sin lo cual una
restauración en un servidor nuevo queda a medias, son las claves del almacén de secretos:

| Clave | Sin ella tras un desastre |
|---|---|
| `MAIL_ENCRYPTION_KEY` (y `MAIL_ENCRYPTION_KEYS_OLD`) | Las credenciales cifradas en la base (relayhosts, claves DKIM, SES propio, contraseñas de origen de migraciones) y los secretos del segundo factor (consola y buzones) no se pueden descifrar: nadie con segundo factor activo puede completarlo |
| `AUDIT_HASH_KEY` (y `AUDIT_HASH_KEYS_OLD`) | Las filas de versión 2 de la cadena de auditoría se restauran pero no se pueden verificar (`hash_key_missing`); `verify-restore.sh` avisa cuando la base restaurada las tiene |
| `JWT_SIGNING_KEY`, `MAIL_LINK_SIGNING_KEY` | Las sesiones caen (se emiten de nuevo) y los enlaces de baja ya enviados dejan de validar |
| Contraseñas de los roles de Postgres | Los roles (`mail_svc_*`, celda) no viajan en un volcado por base: se recrean con `ops/db/tenant-service-role.sh --all`, `cell-service-role.sh` y `cell-engine-role.sh` desde el almacén |

Con el almacén en OpenBao (`docs/adr/0011-almacen-de-secretos-openbao.md`) estas claves **sí** viajan en
cada corrida, dentro de `openbao.snap`, pero cifradas con la llave de desbloqueo de OpenBao, que no está en
ningún archivo de `BACKUP_DIR` ni del bucket. Lo que hay que tener fuera del servidor es esa llave
(`base64 -w0 /opt/core-force-mail/secrets/openbao-llave/desbloqueo.key`, una vez y cada vez que se rote),
guardada con el mismo cuidado que la frase de cifrado: con ella y la última instantánea se recupera el
almacén entero. Sin OpenBao, las claves no están en el respaldo y hay que copiarlas una a una. Tampoco se respaldan el JetStream de NATS (los eventos en vuelo: los críticos siguen en la
`event_outbox` de su base y se republican) ni el Redis de la plataforma (sesiones y contadores).

## Configuración

Las credenciales de Postgres salen del resolvedor común `ops/db/pg-credentials.sh`: la
**contraseña** del almacén de secretos (`ops/security/secrets`) y el **host y usuario** del `.env`,
que es configuración y no secreto. Estos guiones no leen el `.env` para la contraseña: lo hacían, y
el día que las credenciales pasaron al almacén se quedaron con la variable vacía.

| Variable (entorno o `.env`) | Por defecto | Para qué |
|---|---|---|
| `BACKUP_DIR` | `/opt/core-force-mail/backups` | Destino local. Las bases en `<BACKUP_DIR>/<sello>`, el correo en `<BACKUP_DIR>/correo/<sello>` |
| `BACKUP_KEEP_DAYS` | `3` | Retención local. Solo se poda tras una corrida sin fallos: si hoy no se pudo volcar, los respaldos de ayer son los únicos buenos |
| `BACKUP_S3_BUCKET` | *(sin definir)* | Copia externa. **Sin esto solo hay copia local, que se pierde con el servidor** |
| `BACKUP_S3_ENDPOINT` | *(sin definir)* | Vacío: Amazon S3 con el rol de la instancia. Con valor: S3-compatible (Cloudflare R2), que exige credencial propia y cifrado |
| `BACKUP_S3_REGION` | *(la del CLI)* | Región de firma; en R2, `auto` |
| `BACKUP_SECRETS_FILE` | `/opt/core-force-mail/env/backup.env` | Secretos del respaldo, 0600 del usuario que lo corre |
| `MAIL_COMPOSE_PROJECT` | `mail` | Proyecto de compose de `deploy/mail`: prefija los nombres de los volúmenes |
| `BACKUP_MAIL_VOLUMES` | `crypt-vol vmail-vol` | Volúmenes de correo a respaldar |
| `COMPOSE_PROJECT_NAME` | `app` | Proyecto de compose de la plataforma: prefija `minio-data` |
| `BACKUP_PLATFORM_VOLUMES` | `minio-data` en el perfil autoalojado; ninguno en aws | Volúmenes de la plataforma a respaldar en la misma corrida |
| `BACKUP_S3_CLI_IMAGE`, `BACKUP_ARCHIVE_IMAGE` | imágenes fijadas por digest | El CLI de S3 y el `tar` que corren en contenedor |

Secretos, en `BACKUP_SECRETS_FILE` y declarados en
`ops/security/secrets/secret-keys-backup.txt`: `BACKUP_S3_ACCESS_KEY_ID`,
`BACKUP_S3_SECRET_ACCESS_KEY` (vacías en EC2: rol de la instancia; en el servidor propio, las del usuario
`core-force-mail-respaldos` que crea `ops/aws/setup-iam.sh`, que escribe y lee pero no borra) y
`BACKUP_ENCRYPTION_PASSPHRASE`. **No van en el `.env`**: Compose entrega el `.env` entero a cada
contenedor, y con la credencial del bucket y la frase de cifrado, quien comprometa un servicio
cualquiera lee y borra el histórico de respaldos. El fichero se rechaza si no es del usuario del
respaldo o si otros lo pueden leer.

```bash
sudo install -d -m 0700 -o deploy -g deploy /opt/core-force-mail/env
sudo -u deploy install -m 0600 /dev/null /opt/core-force-mail/env/backup.env
sudo -u deploy editor /opt/core-force-mail/env/backup.env   # tres claves, una por linea
```

### Cifrado en reposo de lo que sale del servidor

Con `BACKUP_S3_ENDPOINT` (un tercero que no es la cuenta de AWS del despliegue) el cifrado es
**obligatorio**: sin `BACKUP_ENCRYPTION_PASSPHRASE` no se sube nada y la corrida termina en error,
aunque la copia local sí se hace. Con un bucket de la propia cuenta de AWS es opcional (el bucket
ya cifra en reposo y el acceso lo da el rol), y `crypt-vol` se cifra siempre.

Es `gpg` simétrico (AES-256, con integridad), que la plantilla del servidor ya instala: sin
dependencias nuevas. La frase entra por la entrada estándar, nunca por argumentos. Cada archivo se
descifra y se compara con el original antes de subirlo, y el objeto se guarda con sufijo `.gpg`;
`restore-tenant.sh` y `restore-mail-volume.sh` lo descifran al bajarlo.

**La frase hay que guardarla fuera del servidor.** Con el servidor perdido y la frase solo en él,
el histórico externo no se puede descifrar: es el único caso en que un respaldo cifrado es peor que
ninguno.

Con Amazon S3, el bucket (distinto al de medios, `MINIO_BUCKET`) debe tener acceso público
bloqueado en las cuatro dimensiones, cifrado en reposo por defecto, versionado y ciclo de vida
(`STANDARD_IA` a los 30 días, expiración a los 180, versiones no vigentes a los 30, multiparte
incompletas a los 7), solo por TLS y candado de retencion de 30 dias en modo gobernanza.
`ops/aws/setup-buckets.sh` lo crea si falta y lo deja asi; `setup-iam.sh` da el permiso (escribir y leer
`postgres/`, `correo/` y `openbao/`, nunca borrar) al rol de EC2 o al usuario del servidor propio. Los dos
derivan el nombre de la cuenta con el prefijo `cfm-` (`cfm-backups-<cuenta>`) y leen la misma
`BACKUP_S3_BUCKET`: la cuenta la comparte otro proyecto con buckets `cf-*`, que no se tocan.
`setup-auditoria.sh` actua sobre toda la cuenta (CloudTrail): no se corre en una cuenta compartida sin
acordarlo con el otro proyecto.

En produccion (2026-09-24): Amazon S3 `cfm-backups-<cuenta>` en us-east-1, usuario
`core-force-mail-respaldos` con su clave en `BACKUP_SECRETS_FILE` y cifrado gpg de todo lo que sale. La
frase de cifrado y la llave de desbloqueo de OpenBao estan tambien fuera del servidor, en el equipo de
quien opera. Recuperacion probada desde fuera del servidor: un volcado bajado del bucket se descifra con
la copia externa de la frase en un `PGDMP` valido. Con R2 o cualquier otro S3-compatible, el
equivalente se configura en su panel: bucket privado, versionado y credencial limitada a ese
bucket.

## Programación

`ops/backup/install-timers.sh` instala las unidades de `ops/backup/systemd/` con el **usuario y la
ruta de este servidor** (`DEPLOY_USER`, `DEPLOY_PATH`), prepara `BACKUP_DIR`, el directorio de
métricas y el de secretos con su dueño, activa los temporizadores y retira las entradas de cron que
hubiera (correrían el respaldo dos veces). Es idempotente y lo llama
`ops/server-template/bootstrap.sh`; en un servidor ya aprovisionado se ejecuta tal cual:

```bash
sudo /opt/core-force-mail/app/ops/backup/install-timers.sh
systemctl list-timers 'core-force-mail-*'
```

En el perfil autoalojado se niega si el usuario del respaldo no está en el grupo `docker`: sin él no
puede lanzar las herramientas de Postgres en la red interna.

| Unidad | Cuándo (UTC en el fichero) | Hora de Lima |
|---|---|---|
| `core-force-mail-backup.timer` | `02,08,14,20:15` cada seis horas | 21:15, 03:15, 09:15, 15:15 |
| `core-force-mail-backup-mail.timer` | `02,08,14,20:45` cada seis horas | 21:45, 03:45, 09:45, 15:45 |
| `core-force-mail-backup-verify.timer` | domingos `09:30` | domingos 04:30 |

El `OnCalendar` está en **UTC**: al cambiarlo, no leerlo como hora local. Los tres con prioridad
baja de CPU y disco (`Nice`, `IOSchedulingClass=idle`) y `UMask=0077`, y corriendo como el usuario
de despliegue, nunca como root. Un cerrojo en `BACKUP_DIR` evita que dos corridas se pisen: la
verificación espera a que el respaldo termine antes de leer un volcado.

## Vigilancia

Cada corrida publica su resultado en `/opt/core-force-mail/metrics/*.prom`, que node-exporter monta
y Prometheus recoge. Las alertas están en `ops/observability/prometheus/rules/plataforma.yml`
(grupo `respaldos`), con pruebas en `prometheus/tests/respaldos_test.yml`:

| Alerta | Cuándo |
|---|---|
| `RespaldoSinExitoReciente` | más de 36 h sin un respaldo bueno de las bases o del correo |
| `RespaldoFallido` | la última corrida de cualquiera de los tres trabajos terminó con error |
| `RespaldoNoSeRegistra` | el respaldo de las bases no publica nada: no llega a ejecutarse |
| `VerificacionDeRespaldoSinExitoReciente` | más de 10 días sin comprobar que restaura |
| `VerificacionDeRespaldoNoSeRegistra` | ídem, sin serie que vigilar |

Lo que se vigila es la **marca de tiempo del último éxito**, no el código de salida. El caso
peligroso no es el respaldo que falla —ese deja un error— sino el que dejó de ejecutarse:
temporizador desactivado, servidor reinstalado. Un servidor sin respaldo se ve exactamente igual
que uno con respaldo, hasta el día que hace falta. Por eso hay dos alertas por trabajo: la de
antigüedad no puede dispararse si la serie desaparece, porque entonces no hay nada que comparar.
El respaldo de correo no tiene alerta de ausencia: un host sin los motores de `deploy/mail` no lo
ejecuta y la alerta sonaría para siempre.

## Qué revisar cuando falle

Los guiones salen con código distinto de cero ante cualquier problema; la salida queda en el
journal (`journalctl -u core-force-mail-backup.service`, `-u core-force-mail-backup-mail.service`,
`-u core-force-mail-backup-verify.service`). Los fallos que importan:

- **`volcado ilegible o vacío`**: el archivo existe pero `pg_restore` no lo entiende. No es un
  respaldo; queda renombrado a `.ilegible`, hay que repetirlo y averiguar por qué.
- **`no coincide con su suma`**: el volcado se dañó en disco después de crearse. Se usa el de otra
  corrida y se revisa el disco.
- **`no existe el volumen <proyecto>_<volumen>`**: `MAIL_COMPOSE_PROJECT` no es el proyecto de
  compose con el que corren los motores (o, para `minio-data`, `COMPOSE_PROJECT_NAME` no es el de la
  plataforma, o `minio` aún no se ha desplegado).
- **`la clave privada de mail_crypt no corresponde a la publica`**: con esa pareja, los buzones
  respaldados no se pueden descifrar. Se investiga antes de tocar nada en `crypt-vol`.
- **`no subió a s3://...`** o **`no se pudo cifrar`**: sobrevive a un borrado accidental, no a la
  pérdida del servidor.
- **`la base restaurada no tiene lo que el volcado trae`**: la restauración terminó pero faltan
  esquemas o tablas del índice. Es el fallo más peligroso porque un `ls` del directorio se ve
  perfectamente normal.
- **`public.schema_migrations está vacía`** o **`faltan esquemas que declaran sus migraciones`**: la
  base restaurada no es la que su historial dice que era.

## Comprobaciones

- `ops/scaffold/check-backups.sh` (sin docker, sección 12 de `validate.sh`, dentro de
  `make checks`).
- `bash ops/scaffold/test-selfhosted-backup.sh` (con docker): reproduce el perfil autoalojado con
  `postgres-primary` sin puerto en el host y recorre todo lo de arriba de punta a punta, incluido un
  destino S3-compatible con MinIO desechable y un volcado dañado.
