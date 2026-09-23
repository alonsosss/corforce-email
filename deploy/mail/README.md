# Motores de correo (deploy/mail)

Motores de correo de Core Force Mail, tomados de mailcow-dockerized (commit
`02552ffefdf0` de agosto de 2026) y adaptados para leer el directorio de correo
desde PostgreSQL (esquema `mail`, rol `mail_engine`) y para delegar en dos
servicios Go de la plataforma, `mail-auth` y `mail-policy`, todo lo que mailcow
resolvia con su capa PHP. Aqui no hay interfaz web, ni MariaDB, ni SOGo, ni
nginx, ni memcached, ni ofelia.

Los ficheros de configuracion copiados conservan sus comentarios originales en
ingles. Los comentarios nuevos estan en espanol.

Que ficheros difieren de mailcow, por que y como se rehace cada cambio al portar una version
esta en `UPSTREAM.md`; `upstream-manifest.tsv` es su version legible por maquina y
`ops/scaffold/check-upstream-ledger.sh` la vigila.

## Estructura

| Directorio | Contenido | Origen en mailcow |
|---|---|---|
| `postfix/` | Dockerfile, `postfix.sh` (genera los mapas `pgsql_*.cf`), `conf/` (main.cf, master.cf, pcre, cidr) | `data/Dockerfiles/postfix`, `data/conf/postfix` |
| `dovecot/` | Dockerfile, entrypoint, scripts de cron, `conf/` (dovecot.conf, sieve globales, fts, `auth/passwd-verify.lua`), `crontab` | `data/Dockerfiles/dovecot`, `data/conf/dovecot` |
| `rspamd/` | Dockerfile, entrypoint, `local.d/`, `override.d/`, `lua/`, `custom/`, `plugins.d/`, `settings.conf` | `data/Dockerfiles/rspamd`, `data/conf/rspamd` |
| `unbound/` | Resolver DNSSEC validante (`unbound.conf`) | `data/Dockerfiles/unbound`, `data/conf/unbound` |
| `clamav/` | Dockerfile (compila ClamAV 1.4.6), `clamd.conf`, `freshclam.conf` | `data/Dockerfiles/clamd`, `data/conf/clamav` |
| `olefy/` | Analizador de macros Office para Rspamd | `data/Dockerfiles/olefy` |
| `postfix-tlspol/` | Companion de politicas TLS (DANE / MTA-STS) para Postfix | `data/Dockerfiles/postfix-tlspol` |
| `netfilter/` | Fail2ban propio sobre nftables/iptables, alimentado por Redis | `data/Dockerfiles/netfilter` |
| `acme/` | Cliente Let's Encrypt (HTTP-01 o DNS-01) | `data/Dockerfiles/acme` |
| `dockerapi/` | API HTTPS interna para reiniciar contenedores y ejecutar tareas (doveadm, mailq) | `data/Dockerfiles/dockerapi` |
| `watchdog/` | Vigilante de salud con reinicio automatico y notificaciones | `data/Dockerfiles/watchdog` |
| `migration-runner/` | Ejecutor de la migracion de buzones: envoltorio Go y imapsync (seccion "Migracion de buzones") | nuevo |
| `redis/redis-conf.sh` | Arranque de Redis con `requirepass` y el usuario ACL `quota_notify` | `data/conf/redis` |
| `ssl-example/` | Certificado snake-oil inicial y `dhparams.pem` | `data/assets/ssl-example` |
| `templates/quota.tpl` | Plantilla Jinja del aviso de cuota | `data/assets/templates` |
| `docker-compose.mail.yml` | Compose solo de los motores | nuevo |
| `docker-compose.mail.images.yml` | Imagenes de despliegue por commit (`scripts/deploy-mail.sh`) | nuevo |

No se copiaron: `data/web`, sogo, phpfpm, nginx, mysql, `dynmaps/*.php`,
`meta_exporter/*.php`, `mailcowauth.php`, ldap, backup, `update.sh`,
`_modules`, `helper-scripts`, imapsync, `quarantine_notify.py`,
`pw_reset_*.tpl`, `quarantine.tpl`.

## Que cambio respecto a mailcow

* **Base de datos.** Todos los mapas de Postfix son `proxy:pgsql:` y se generan en
  `postfix.sh` contra `mail.*`. El userdb y los dicts de Dovecot son `pgsql`.
  La espera de arranque usa `pg_isready`. Los Dockerfiles instalan
  `postfix-pgsql`, `dovecot-pgsql`, `lua-sql-postgres` y `postgresql-client`
  en lugar de las variantes MySQL/MariaDB.
* **Autenticacion.** `passwd-verify.lua` llama a `MAIL_AUTH_URL` (por defecto
  `https://mail-auth:9082`). El entrypoint de Dovecot renderiza la plantilla con
  `envsubst` en `/etc/dovecot-auth/passwd-verify.lua`, fuera del bind mount, para
  no reescribir el fichero del repositorio.
* **Politicas dinamicas.** Todo `http://nginx:8081/<x>.php` pasa a
  `http://mail-policy:8081/<x>` y `http://nginx:9081/<x>.php` a
  `http://mail-policy:9081/<x>`. Rspamd espera a esos dos puertos al arrancar.
* **SOGo.** Eliminado del entrypoint de Dovecot (`sogo_trusted_ip.conf`,
  `sogo-sso.conf`, credenciales de cron), de `dovecot.conf`, del watchdog, del
  dockerapi (`sogo rename_user`), de rspamd (`SOGO_CONTACT`) y de netfilter
  (regex 8 y 9). El usuario maestro de Dovecot se conserva bajo
  `@platform.local`: lo usa el webmail (seccion "Webmail") y solo existe si se definen
  `DOVECOT_MASTER_USER`/`DOVECOT_MASTER_PASS`; sin ellas es aleatorio.
* **Eliminado tambien:** imapsync (scripts, cron, dependencias Perl; vuelve como servicio propio en
  `migration-runner/`, seccion "Migracion de buzones"), la tabla
  `versions`/GUID, `quarantine_notify.py`, la regla `PUSHOVERMAIL` y el selector
  `mailcow_rcpt` de `metadata_exporter.conf`, los checks de nginx, mysql,
  mysql_repl, phpfpm, sogo y `external_checks` del watchdog, los handlers
  `mysql_upgrade`, `mysql_tzinfo_to_sql` y `reload nginx` del dockerapi, y los
  mapas BCC de Postfix (mailcow ya los aplicaba solo desde Rspamd).
* **Cron.** Los jobs de Dovecot que lanzaba ofelia ahora los ejecuta busybox
  `crond` dentro del contenedor, gestionado por supervisord (ver mas abajo).
* **Maildir de un buzon borrado.** mailcow lo mueve a `_garbage` desde su PHP por dockerapi
  en el momento del borrado; aqui lo hace `maildir_reconcile.sh` dentro del contenedor de
  Dovecot, a partir de la marca de baja que deja mail-directory (seccion "Maildir de un buzon
  borrado").
* **Nombres.** `MAILCOW_HOSTNAME` -> `MAIL_HOSTNAME`, `DBUSER/DBPASS/DBNAME` ->
  `MAIL_DB_*`, `REDISPASS` -> `MAIL_REDIS_PASSWORD`, `MAILCOW_REPLICA_IP` ->
  `MAIL_REPLICA_IP`, `ONLY_MAILCOW_HOSTNAME` -> `ONLY_MAIL_HOSTNAME`,
  `mail_name = Core Force Mail`, `mailcow.local` -> `platform.local`
  (`allow_mailcow_local.regexp` se elimino: ver "Webmail"),
  `mailcow_networks.map` -> `platform_networks.map`, contenedores `<svc>-mail`,
  red `mail-engines` (bridge `br-mail`). El resto de variables genericas de
  mailcow (`TZ`, `SKIP_FTS`, `MASTER`, `LOG_LINES`, `IPV4_NETWORK`...) conservan
  su nombre para no romper los scripts que las leen.
* **Spamhaus.** Las listas abiertas de Spamhaus solo se activan si
  `SPAMHAUS_ASN_CHECK_URL` responde 200 (mailcow consultaba su propio servicio);
  sin esa variable ni `SPAMHAUS_DQS_KEY` quedan desactivadas por seguridad.
* **Comprobacion de DNS.** El canario `dig mailcow.email` pasa a
  `dig letsencrypt.org`.

Los simbolos de Rspamd que llevan `MAILCOW_` en el nombre (`MAILCOW_AUTH`,
`RCPT_MAILCOW_DOMAIN`, `MAILCOW_WHITE`, `MAILCOW_BLACK`, `MAILCOW_FUZZY_*`,
`MAILCOW_DOMAIN_HEADER_FROM`) se conservan: son identificadores internos
referenciados desde composites, multimap, lua, metadata_exporter y el UCL que
devuelve `/settings`; renombrarlos no aporta nada y es una fuente de errores.

## Requisitos externos

1. **PostgreSQL** de la celda con el esquema `mail` migrado
   (`migrations/cell/canonical/mail-directory/01_mail.sql`) y el rol
   `mail_engine` con contrasena. Debe ser alcanzable desde la red `mail-engines`
   por el nombre que se pase en `MAIL_DB_HOST` (si es un contenedor, unirlo a la
   red; si es externo, IP o DNS resoluble por unbound).
2. **`mail-auth`** y **`mail-policy`** (servicios Go) unidos a la red
   `mail-engines` con esos alias, o `MAIL_AUTH_URL`/`MAIL_POLICY_HOST` apuntando
   a donde esten. El unbound de la red solo resuelve nombres publicos: los alias
   los resuelve el DNS interno de Docker.
3. **HTTP-01 de ACME:** el gateway de la plataforma debe servir
   `http://<dominio>/.well-known/acme-challenge/` desde el volumen
   `acme-challenge-vol` (montado en acme en `/var/www/acme`). Alternativa:
   `ACME_DNS_CHALLENGE=y` con la configuracion de `acme/load-dns-config.sh`.
   En la produccion autoalojada (`docs/Operacion_Despliegue.md`, 11) se usa DNS-01
   (`ACME_DNS_PROVIDER=dns_cf`) y el proxy de borde de la plataforma lee `cert.pem`
   y `key.pem` de `ssl-vol` (volumen `<proyecto>_ssl-vol`, `MAIL_SSL_VOLUME`): el
   host publico de la web va en `ADDITIONAL_SAN`. Ahi `MAIL_DB_HOST=postgres` es
   PgBouncer, que ese perfil une a la red `mail-engines`; Postgres solo acepta TLS
   y no entra en esa red.
4. Si `mail-policy` escribe los mapas de `rspamd/custom/` (listas globales), debe
   correr con uid/gid 82, que es el propietario que fija el entrypoint de Rspamd.

## Variables de entorno

Comunes a casi todos: `TZ`, `LOG_LINES`, `IPV4_NETWORK` (por defecto `172.22.1`),
`IPV6_NETWORK`, `REDIS_SLAVEOF_IP`/`REDIS_SLAVEOF_PORT` (replica), `MAIL_REDIS_HOST`
(por defecto `redis`), `MAIL_REDIS_PASSWORD`.

| Contenedor | Variables propias |
|---|---|
| `postfix-mail` | `MAIL_HOSTNAME`, `MAIL_DB_HOST`, `MAIL_DB_PORT`, `MAIL_DB_NAME`, `MAIL_DB_USER`, `MAIL_DB_PASSWORD`, `MAIL_POLICY_HOST`, `SKIP_LETS_ENCRYPT`, `SPAMHAUS_DQS_KEY`, `SPAMHAUS_ASN_CHECK_URL` |
| `dovecot-mail` | `MAIL_HOSTNAME`, `MAIL_DB_*`, `MAIL_AUTH_URL`, `DOVECOT_MASTER_USER`, `DOVECOT_MASTER_PASS`, `DOVECOT_MASTER_ALLOWED_NETS`, `DOVECOT_MIGRATION_MASTER_USER`, `DOVECOT_MIGRATION_MASTER_PASS`, `DOVECOT_MIGRATION_MASTER_ALLOWED_NETS`, `MAIL_MIGRATION_IPV4_NETWORK`, `DOVEADM_API_KEY`, `MAIL_REPLICA_IP`, `DOVEADM_REPLICA_PORT`, `MAILDIR_GC_TIME`, `MAILDIR_RECONCILE_GRACE`, `MAILDIR_RECONCILE_MAX_MOVES`, `ACL_ANYONE`, `SKIP_FTS`, `FTS_HEAP`, `FTS_PROCS`, `MAILDIR_SUB`, `MASTER`, `COMPOSE_PROJECT_NAME` |
| `rspamd-mail` | `MAIL_POLICY_HOST`, `SPAMHAUS_DQS_KEY`, `SKIP_OLEFY` |
| `acme-mail` | `MAIL_HOSTNAME`, `MAIL_DB_*`, `ADDITIONAL_SAN`, `AUTODISCOVER_SAN`, `SKIP_LETS_ENCRYPT`, `DIRECTORY_URL`, `ENABLE_SSL_SNI`, `SKIP_IP_CHECK`, `SKIP_HTTP_VERIFICATION`, `ONLY_MAIL_HOSTNAME`, `LE_STAGING`, `SNAT_TO_SOURCE`, `SNAT6_TO_SOURCE`, `ACME_DNS_CHALLENGE`, `ACME_DNS_PROVIDER`, `ACME_ACCOUNT_EMAIL`, `COMPOSE_PROJECT_NAME` |
| `watchdog-mail` | `MAIL_HOSTNAME`, `USE_WATCHDOG`, `WATCHDOG_NOTIFY_EMAIL`, `WATCHDOG_NOTIFY_BAN`, `WATCHDOG_NOTIFY_START`, `WATCHDOG_SUBJECT`, `WATCHDOG_NOTIFY_WEBHOOK`, `WATCHDOG_NOTIFY_WEBHOOK_BODY`, `WATCHDOG_VERBOSE`, `IP_BY_DOCKER_API`, `CHECK_UNBOUND`, `SKIP_CLAMD`, `SKIP_OLEFY`, `SKIP_LETS_ENCRYPT`, `*_THRESHOLD`, `MAILQ_CRIT`, `DEV_MODE`, `COMPOSE_PROJECT_NAME` |
| `netfilter-mail` | `SNAT_TO_SOURCE`, `SNAT6_TO_SOURCE`, `MAIL_REPLICA_IP`, `DISABLE_NETFILTER_ISOLATION_RULE` (usa `IPV4_NETWORK.249` para Redis porque va en `network_mode: host`) |
| `postfix-tlspol-mail` | `DEV_MODE` |
| `redis-mail` | `MAIL_REDIS_PASSWORD`, `MAIL_REDIS_MASTER_PASSWORD` |
| `clamd-mail` | `SKIP_CLAMD` |
| `olefy-mail` | `OLEFY_*`, `SKIP_OLEFY` |
| `mail-migration-runner` | `ENVIRONMENT`, `MIGRATION_API_URL`, `MAIL_MIGRATION_RUNNER_KEY`, `MIGRATION_RUNNER_ID`, `MIGRATION_DEST_HOST`, `MIGRATION_DEST_PORT`, `MAIL_HOSTNAME` (nombre TLS del destino), `DOVECOT_MIGRATION_MASTER_USER` y `DOVECOT_MIGRATION_MASTER_PASS` (ya opcionales: solo para el flujo sin credencial por trabajo), `MIGRATION_CLAMD_ADDR`, `MIGRATION_ALLOW_UNSCANNED`, `MIGRATION_SOURCE_PORTS` (de `MAIL_MIGRATION_SOURCE_PORTS`), `MIGRATION_JOB_TIMEOUT`, `MIGRATION_POLL_INTERVAL` |
| `unbound-mail` | `SKIP_UNBOUND_HEALTHCHECK` |
| `dockerapi-mail` | solo las comunes de Redis |

Obligatorias sin valor por defecto: `MAIL_HOSTNAME`, `MAIL_DB_HOST`,
`MAIL_DB_NAME`, `MAIL_DB_PASSWORD`, `MAIL_REDIS_PASSWORD`. Ninguna va en el
compose: llegan por `.env` o por el orquestador.

`MAIL_DB_USER` es el rol de ESTA celda, `<MAIL_DB_NAME>_engine`, que crea
`ops/db/cell-engine-role.sh --cell <code>` con `MAIL_DB_PASSWORD` del almacen.
Es miembro del grupo `mail_engine`, de donde hereda exactamente los permisos que
enumeran las migraciones de la celda (SELECT sobre lo que consultan Postfix y
Dovecot, escritura solo en `mail.quota_usage`, nada sobre `mail.app_passwords`
ni `mail.sasl_logins`), y tiene CONNECT propio solo a la base de su celda.
`mail_engine` sigue existiendo como grupo; como rol de LOGIN compartido se
retira con `cell-engine-role.sh --cell <code> --retire-shared` cuando ninguna
celda lo usa ya. Esa segunda fase es la que cierra el aislamiento entre celdas:
la membresia no distingue permisos de tabla de permisos de base, asi que
mientras `mail_engine` conserve el CONNECT que necesitan los motores todavia sin
recrear, cada rol de celda lo hereda y alcanza las bases de las demas. Hasta
entonces el rol por celda ya evita que la contrasena de una celda sea la de
todas, pero no el alcance. Orden y ventana, en `docs/Operacion_Despliegue.md`, 2.

## Red, IPs fijas y puertos

Red `mail-engines` (`${IPV4_NETWORK}.0/24`, bridge `br-mail`): unbound `.254`,
redis `.249`, dovecot `.250`, postfix `.253`; rspamd con `hostname: rspamd`.
`netfilter-mail` corre en `network_mode: host` y aisla los puertos 3306, 6379,
8983 y 12345 del bridge salvo para `MAIL_REPLICA_IP`.

Red `mail-migration` (`${MAIL_MIGRATION_IPV4_NETWORK}.0/24`, por defecto `172.22.2`, bridge `br-mail-migr`): dovecot
(alias `dovecot`), clamd (alias `clamd`), `mail-migration-runner` y, desde el compose de la plataforma,
`mail-migration`. Es la unica red del ejecutor de migracion (seccion "Migracion de buzones").

Red `mail-scan` (`${MAIL_SCAN_IPV4_NETWORK}.0/24`, por defecto `172.22.3`, bridge `br-mail-scan`, `internal`):
clamd (alias `clamd`) y, desde el compose de la plataforma, `templates`, que analiza cada imagen de las
plantillas antes de guardarla (`TEMPLATES_CLAMD_ADDR`). Existe para que templates no entre en `mail-engines`,
que es `mynetworks` de Postfix.

Publicados: 25 (SMTP), 465 (SMTPS), 587 (submission), 143/993 (IMAP), 110/995
(POP3), 4190 (ManageSieve). Internos: postfix 588 (submission interna sin TLS
obligatorio, `submission_host` de Dovecot y avisos de cuota), 590 (reinyeccion
de cuarentena), 591 (copias BCC), 589 (watchdog), 10025/10465/10587 (HAProxy);
dovecot 24 (LMTP), 10001 (SASL para Postfix), 12345 (doveadm), 8443 (API HTTP de doveadm con
TLS, solo con `DOVEADM_API_KEY`); rspamd 9900
(milter), 11332-11334 y `/var/lib/rspamd/rspamd.sock`; postfix-tlspol 8642;
clamd 3310; olefy 10055; dockerapi 443.

## Despliegue en un servidor (`scripts/deploy-mail.sh`)

Los motores se despliegan desde el puesto de trabajo, como la plataforma y con las mismas piezas
(`scripts/lib/despliegue.sh`: destino, candado del servidor y guardia de retroceso). El servidor
nunca compila:

```bash
DEPLOY_HOST=<srv> DEPLOY_USER=deploy DEPLOY_SSH_KEY=~/.ssh/<llave> scripts/deploy-mail.sh              # motores con cambios
DEPLOY_HOST=<srv> DEPLOY_USER=deploy DEPLOY_SSH_KEY=~/.ssh/<llave> scripts/deploy-mail.sh dovecot-mail # motores explicitos
```

1. Exige un arbol sin cambios sin commitear (`DEPLOY_ALLOW_DIRTY=1` lo salta a proposito) y lee el
   modelo de los motores del propio compose (`docker compose config`): contexto de build, ficheros
   montados del repositorio, dependencias y la etiqueta `core-force-mail.solo-servidor`.
2. Sin argumentos despliega los motores que corren en el servidor y cuyo contexto, ficheros montados
   o compose cambiaron desde el commit que corre cada uno (`<MAIL_DEPLOY_PATH>/.deployed-tags`, una
   linea `motor commit`, o la etiqueta de su imagen). Un motor de commit desconocido (imagen
   `:latest`) o que nunca se levanto se pide por nombre.
3. Rechaza `acme-mail`, `netfilter-mail`, `watchdog-mail` y `dockerapi-mail` si el demonio de docker
   del destino es el de esta maquina (compara sus identificadores). Guardia de retroceso antes del
   build (`DEPLOY_ALLOW_ROLLBACK=1` para un rollback a proposito).
   Puerta de regresion (V, 2026-09-21, `ops/scaffold/check-deploy-mail.sh` con mutaciones): tambien
   antes del build exige que `make e2e-mail` este en verde para lo que se despliega, segun el flujo
   `mail-engines.yml` de GitHub consultado con `gh` (`gh auth login` en el puesto de trabajo). Vale una
   ejecucion verde de HEAD o la ultima verde de un commit anterior de main si desde entonces no cambio
   nada de lo que el flujo vigila (sus `paths`); una ejecucion de HEAD fallida, o ninguna que lo cubra,
   detiene el despliegue. `MAIL_DEPLOY_REGRESION=avisar` dice lo que falta y sigue; `omitir` no
   comprueba y lo deja dicho en la salida: solo con una razon.
4. Construye en local (`MAIL_BUILD_LOTE` a la vez) con `docker-compose.mail.images.yml`, que etiqueta
   cada motor `core-force-mail/<motor>:<commit>` con `pull_policy: never`, y solo envia
   (`docker save | gzip | ssh docker load`) lo que el servidor no tenga ya con esa etiqueta. No hay
   transporte por ECR: los motores no tienen repositorios alli.
5. Con el candado del servidor (el mismo de `deploy-ecr.sh`: nunca corren los dos a la vez) repite la
   guardia, extrae `deploy/mail`, `ops/security/secrets`, `esperar-sanos.sh` y
   `prune-local-images.sh` de HEAD en `MAIL_DEPLOY_PATH` (por defecto
   `/opt/core-force-mail/mail-src`) y retira los ficheros borrados del repositorio desde el commit
   de cada motor. El `tar` y el `rm` corren **dentro de un contenedor efimero como root**
   (`docker run --rm --network none -v <MAIL_DEPLOY_PATH>:/arbol`, con la imagen fijada de un motor
   que no se construye: hoy la de `redis-mail`), porque el bind mount es de ida y vuelta: rspamd
   reescribe `local.d`, `override.d`, `plugins.d` y `custom/*` con el uid de su contenedor y deja
   esos directorios sin permiso de escritura para el usuario de despliegue, y un `tar` suyo falla
   con «File exists» y «Cannot utime» (pasó en el servidor real el 2026-09-17, antes de recrear
   nada). Solo se tocan las rutas del archivo: lo que los motores **generan** no está versionado
   (ver «Ficheros generados en tiempo de ejecución» y `.gitignore`), así que no se reemplaza ni
   cambia de dueño —los `sql/*.cf` 640 `root:postfix` que Postfix lee mientras corre siguen
   intactos—. Lo versionado queda de root con los modos del repositorio (legible por todos) y cada
   motor vuelve a poner lo suyo al arrancar. Reemplazar lo versionado con HEAD es correcto: ahí
   ningún motor guarda estado que no rehaga solo (los mapas versionados de `rspamd/custom/` son
   listas base y el entrypoint solo los `touch`ea). Si alguna vez un servicio escribe esos mapas
   (requisito 4 de «Requisitos externos»), hay que desplegar `rspamd-mail` en la misma ventana: su
   arranque es lo que devuelve `custom/*` a uid 82.
6. Recrea los motores de uno en uno, en orden de dependencias: `with-secrets.sh docker compose -p
   <MAIL_PROJECT> --env-file <DEPLOY_PATH>/.env ... up -d --no-deps --no-build --force-recreate`,
   comprueba que el contenedor corre la imagen del commit y espera con `esperar-sanos.sh` (sano si la
   imagen declara chequeo, como unbound y clamd; si no, corriendo sin reiniciarse
   `MAIL_DEPLOY_ESTABLE` segundos) hasta `MAIL_DEPLOY_PLAZO` (900 s: clamd carga firmas varios
   minutos). Solo entonces anota el motor en `.deployed-tags` y pasa al siguiente: Postfix y Dovecot
   nunca caen a la vez y un motor que no arranca detiene el despliegue con su registro, con los
   siguientes intactos.
7. Avisa de los motores no recreados que ya tienen configuracion nueva en disco (la leerian en su
   proximo reinicio) y retira las imagenes viejas (`prune-local-images.sh`, conserva tres por motor
   para el rollback).

La red `mail-engines` y los volumenes (`<MAIL_PROJECT>_ssl-vol` entre ellos) los crea Compose al
levantar el primer motor, con las etiquetas y la configuracion del compose; una red creada antes a
mano con `com.docker.compose.project=mail` y `com.docker.compose.network=mail-engines` se acepta tal
cual. La plataforma no los crea: `scripts/deploy-ecr.sh` comprueba antes de recrear nada que existen
(`ops/maintenance/recursos-externos.sh`) y, si no, se detiene pidiendo desplegar antes los motores.

Rollback: desde el commit anterior, `DEPLOY_ALLOW_ROLLBACK=1 scripts/deploy-mail.sh <motores>`; si
la imagen sigue en el servidor no se reconstruye. Guardarrailes: `ops/scaffold/check-deploy-mail.sh`
(sin docker, en `make checks`) y `ops/scaffold/test-deploy-mail.sh` (con docker, contra un servidor
simulado con su propio demonio y sshd).

## Tamano de los mensajes

Un mismo tope recorre la cadena y `ops/scaffold/check-mail-size-limits.sh` (en `validate.sh`,
dentro de `make checks`) compara los ficheros:

| Limite | Donde | Valor | Regla |
|---|---|---|---|
| `message_size_limit` | `postfix/conf/main.cf.base` | 104857600 (100 MiB) | el mayor mensaje que acepta la celda |
| `max_message` | `rspamd/local.d/options.inc`, en bytes y en ningun otro fichero de `rspamd/` | 105906176 (101 MiB) | al menos Postfix + 1 MiB |
| `max_size` del antivirus | `rspamd/local.d/antivirus.conf`, en bytes y en ningun otro fichero de `rspamd/local.d/` | 105906176 (101 MiB) | al menos Postfix + 1 MiB |
| `StreamMaxLength`, `MaxFileSize` | `clamav/clamd.conf` | 101M | al menos Postfix + 1 MiB |
| `MaxScanSize` | `clamav/clamd.conf` | 202M | al menos `MaxFileSize`; el resto, margen para lo que se extrae de un comprimido |
| `AlertExceedsMax` | `clamav/clamd.conf` | `yes` | obligatorio |
| `VIRUS_SCAN_FAILED` | `rspamd/local.d/force_actions.conf` | `soft reject` sobre `CLAM_VIRUS_FAIL` | obligatorio |
| techo de `MAIL_QUARANTINE_MAX_BODY_MB` | `maxPipeMaxBodyMiB` de `services/mail-security/main.go`, `domain.MaxQuarantineMaxSizeBytes` y el CHECK de la migracion 08 | 101 MiB | de Postfix + 1 MiB a `max_message` + 1 MiB; el defecto (50) y `.env.example`, de 1 al techo; es tambien el mayor `max_size_bytes` que una empresa puede pedir |
| `postfixMessageSizeLimit` | `services/webmail/main.go` | 100 MiB | igual que `message_size_limit`: techo de `WEBMAIL_MAX_MESSAGE_BYTES`, `WEBMAIL_MAX_BODY_PART_BYTES` y `WEBMAIL_MAX_ATTACHMENT_BYTES` |

`postfix/conf/main.cf` no se versiona: `postfix/postfix.sh` lo genera en cada arranque desde
`main.cf.base` (hasta la marca `Overrides`), las DNSBL y `extra.cf`, en un temporal que renombra
encima. Un valor de Postfix se cambia en `main.cf.base`; las comprobaciones de `ops/scaffold/`
leen ese fichero.

Postfix pasa cada mensaje por el milter de Rspamd (`smtpd_milters`, `non_smtpd_milters`). Uno mayor
que `max_message` no se analiza: el proxy del milter contesta tempfail a cualquier fallo del worker y
Postfix (`milter_default_action = tempfail`) lo rechaza temporalmente hasta devolverlo, sin
entregarlo. Con el defecto de Rspamd 4.1.4 (50 MiB, `DEFAULT_MAX_MESSAGE`) le pasaba a todo mensaje
de 50 a 100 MiB. El MiB de margen cubre las cabeceras que reconstruye el milter y el envoltorio
multipart de `/pipe` con sus metadatos. `max_message` acota tambien lo que aprende el controller.
`MAIL_QUARANTINE_MAX_BODY_MB` puede quedar por debajo de Postfix (menos memoria por peticion en
`/pipe`): un mensaje mayor sigue su accion, pero no queda en la cuarentena.

El antivirus falla de otra manera: **callado**. Rspamd manda a clamd el mensaje ENTERO
(`scan_mime_parts = false` en `antivirus.conf`), no cada parte, y los dos topes que lo acotan dejan
pasar el mensaje como si estuviera limpio:

* Un mensaje mayor que `max_size` NO se analiza. Rspamd lo anota en `info` (`skip clamav check as it
  is too large`), no deja ningun simbolo y el mensaje sigue su camino. Con los 20 MiB que traia la
  copia, todo mensaje de 20 a 100 MiB entraba sin pasar por ClamAV.
* clamd, con un flujo mayor que `StreamMaxLength`, responde `INSTREAM size limit exceeded. ERROR` y
  con flujos bastante mayores corta la conexion a medias; pero con un fichero mayor que `MaxFileSize`
  (o un total mayor que `MaxScanSize`) lo analiza TRUNCADO y responde `stream: OK`, un veredicto
  limpio falso. Comprobado con la imagen de `clamav/` y sus firmas reales: 30 y 60 MiB con EICAR al
  final dan `OK` con `MaxFileSize 25M`, y `Heuristics.Limits.Exceeded.MaxFileSize FOUND` con
  `AlertExceedsMax yes`.

De ahi la regla: **ningun mensaje se entrega sin veredicto**. Los topes cubren lo que Postfix acepta;
`AlertExceedsMax` convierte un analisis recortado en una deteccion; `clamav.lua` traduce
`Heuristics.Limits.Exceeded` (y cualquier error, plazo agotado o respuesta que no entiende) al
simbolo `CLAM_VIRUS_FAIL`, que por si solo puntua 0 y no hace nada; y la regla `VIRUS_SCAN_FAILED` de
`force_actions.conf` lo convierte en `soft reject`, o sea 451 al remitente. Postfix conserva el
mensaje en cola y lo reintenta: una parada de clamd APLAZA el correo entrante en vez de entregarlo
sin analizar. `SKIP_CLAMD=y` por si solo hace exactamente eso (es lo correcto, pero rara vez lo que
se busca); apagar el antivirus a proposito es vaciar `antivirus.conf`, porque sin regla no hay
simbolo y `VIRUS_SCAN_FAILED` no dispara. Analizar 101 MiB cuesta menos de dos segundos con las
firmas al dia, de ahi `timeout = 15` en `antivirus.conf`, por debajo del `task_timeout` de 25 s del
worker (`override.d/worker-normal.inc`); agotarlo tambien acaba en `soft reject`, nunca en entrega.

## Contrato HTTP de `mail-auth`

Lo llama `passwd-verify.lua` en cada autenticacion IMAP/POP3/ManageSieve/SMTP
(Postfix delega el SASL en Dovecot). HTTPS con certificado no verificado
(`insecure = true`, igual que mailcow), timeout 30 s.

```
POST /            Content-Type: application/json
{"username": "user@dominio", "password": "...", "real_rip": "1.2.3.4", "service": "imap|pop3|sieve|smtp|lmtp|webmail"}

200  {"success": true}                          (con service "webmail": {"success": true, "display_name": "..."})
401  {"success": false}
400  {"success": false}   cuerpo incompleto
```

Con `"service": "dav"` (solo entonces) un 200 lleva ademas `username` (el del buzon, en minusculas), `tenant_id` y
`mailbox_id`: `mail-dav` no puede deducir la empresa ni el buzon del nombre, y las necesita para elegir la base de la
empresa y acotar cada consulta. Un rechazo no lleva ninguno. Con `webmail` la respuesta lleva `display_name`; con los
demas servicios, solo `success`.

Con `"service": "migration"` (solo lo envia `migration-verify.lua`, para una contrasena que empieza por `cfmj1.`) la
peticion es la credencial de destino de un trabajo de migracion, no una contrasena del buzon: `mail-auth` no la compara
con el hash del buzon ni con las contrasenas de aplicacion, ni mira los flags `*_access`. La acepta solo si la IP real
esta en `MAIL_AUTH_JOB_ALLOWED_NETS` (la red del ejecutor) y `mail-migration` confirma que el token esta vivo y es de ese
buzon (`POST /internal/mail-migration/credentials/verify`), y el buzon existe todavia, es el mismo del trabajo (por id y
por empresa) y tiene `active = 1`. La respuesta 200 lleva solo `{"success": true}`; cualquier rechazo, `401`. Deja un
inicio en `mail.sasl_logins` con `service = migration`. Sin `MAIL_MIGRATION_URL` y `MAIL_AUTH_JOB_ALLOWED_NETS` (las dos
juntas: con una sola no arranca) esas credenciales se deniegan.

Cualquier otro codigo o un JSON invalido se trata como fallo de contrasena
(`PASSDB_RESULT_PASSWORD_MISMATCH`), lo que invalida la cache de auth de ese
usuario. El servicio debe validar contrasena principal y `mail.app_passwords`
(acotadas por `service`), respetar `mailboxes.active = 1` y los flags
`imap_access`/`pop3_access`/`smtp_access`/`sieve_access`, y registrar el login en
`mail.sasl_logins`. Dovecot cachea resultados 300 s (negativos 60 s); `mail-security` vacia la
entrada de un buzon con cada evento de buzon ("Revocacion en Dovecot", abajo).

Lo implementa `services/mail-auth`: listener TLS en `MAIL_AUTH_TLS_PORT` (9082)
con `MAIL_AUTH_TLS_CERT`/`MAIL_AUTH_TLS_KEY` (sin ellos, certificado autofirmado
en memoria avisado en log; con ellos, relee el par cada 15 s cuando cambia en disco, sin
reiniciar; en el perfil autoalojado los emite la CA interna con SAN `mail-auth`,
`docs/Operacion_Despliegue.md` 11); `POST /` y `POST /auth`; `service` se traduce a flag
(`imap`, `pop3`, `smtp`/`submission`/`lmtp` -> `smtp_access`,
`sieve`/`managesieve` -> `sieve_access`; `dav` -> `dav_access`, el servicio con el que
`mail-dav` (CardDAV y CalDAV) verifica a cada buzon por HTTP Basic, con la contrasena principal o una de aplicacion
con `dav_access` (docs/adr/0004); `webmail` exige `imap_access` Y
`smtp_access`, porque el webmail lee y envia con la credencial maestra, que no vuelve a
pasar por aqui; cualquier otro se deniega); bcrypt sobre la contrasena principal y
despues sobre las de aplicacion activas con ese flag (actualiza `last_used_at`; el
webmail solo acepta la principal); `active = 2` y `active = 0` no entran;
`force_pw_update` no bloquea. Freno de fuerza bruta en el Redis de la plataforma
por `(username, real_rip)` y por `real_rip` (`MAIL_AUTH_MAX_FAILURES` 10, de 1 a 1000;
`MAIL_AUTH_MAX_FAILURES_PER_IP` 50, de 1 a 10000; `MAIL_AUTH_FAILURE_WINDOW` 15m y
`MAIL_AUTH_LOCK_TTL` 30m, de 1m a 24h). Un valor fuera de rango, o un puerto fuera de 1 a
65535, impide arrancar aunque Redis no responda; sin Redis arranca sin freno y lo avisa. Metrica
`mail_auth_attempts_total{auth_service,result}`. El listener HTTP de `MAIL_AUTH_PORT`
(8041) solo sirve `/healthz`, `/metrics` y
`GET /internal/mail-auth/logins?username=&limit=` (tras `X-Gateway-Token` +
`X-Tenant-ID`). El contenedor debe unirse a `mail-engines` con el alias
`mail-auth`.

## Revocacion en Dovecot (API HTTP de doveadm)

Dovecot guarda cada autenticacion correcta en su cache (`auth_cache_ttl = 300s`; clave
`%s:%u:%w` de la passdb Lua: servicio, buzon y contrasena) y mientras dura no vuelve a preguntar
a `mail-auth`, y una sesion IMAP o POP3 abierta sigue hasta que el cliente se va. Sin mas, un
buzon apagado, borrado o con la contrasena cambiada seguiria entrando hasta cinco minutos con la
credencial vieja y conservaria sus sesiones. `mail-security` lo cierra con los eventos del
directorio:

* **Canal**: el API HTTP de doveadm de Dovecot 2.3 (`POST /doveadm/v1`) en el listener `http`
  del servicio `doveadm`: puerto 8443, `ssl = yes` con el certificado del servidor de correo, sin
  publicar (solo se alcanza en la red `mail-engines`). mailcow no tenia equivalente: su
  `dockerapi` ejecuta `doveadm` con el socket de docker del host y recibe ordenes sin respuesta
  por `MC_CHANNEL`; el API de Dovecot autentica cada llamada, responde por orden y no necesita
  privilegios del host.
* **Credencial**: `DOVEADM_API_KEY`, del almacen de secretos (`ops/security/secrets/secret-keys.txt`)
  y la misma en `dovecot-mail` y en `mail-security`: de 32 a 256 caracteres de `[A-Za-z0-9_-]`
  (`openssl rand -hex 32`). El entrypoint la escribe en `/etc/dovecot-auth/doveadm-api.conf`
  (600, root), fuera del bind mount de `/etc/dovecot`, con `doveadm_allowed_commands = kick,auth
  cache flush` (con replica, ademas `dsync-server`, porque la regla vale tambien para el puerto
  12345): la clave solo vacia la cache y echa a un buzon; no lee correo ni lista quien esta
  conectado. Sin clave no se abre el listener. `mail-security` la manda en
  `Authorization: X-Dovecot-API <base64>` a `DOVEADM_API_URL` (`https://dovecot:8443`,
  `https://host[:puerto]` sin ruta, usuario, consulta ni fragmento: en claro o mal formada no
  arranca), por TLS verificado contra `DOVEADM_API_TLS_SERVER_NAME` (vacio = `MAIL_HOSTNAME`), con
  `DOVEADM_API_TLS_CA_FILE` para una CA propia, sin proxy y sin seguir redirecciones. Sin clave,
  `mail-security` solo arranca con `ENVIRONMENT=development|test`.
* **Eventos**: consumidor durable propio, `mail-security-dovecot`, sobre `mail.mailbox.>` (stream
  `MAIL_DIRECTORY`), aparte de `mail-security-redis` para que un Dovecot caido no retenga los de
  Redis. Cada evento es una sola peticion con `authCacheFlush` del buzon (quita sus entradas,
  positivas y negativas; la clave de mailcow no cambia: el vaciado por buzon la encuentra) y, si
  toca, `kick` despues, para que un cliente echado que vuelve a entrar no encuentre la entrada
  vieja. Decide con el estado real del buzon (`mail.v_routing_mailboxes`), no con el evento:

| Evento | Estado del buzon | Accion |
|---|---|---|
| `mail.mailbox.deleted` | no se lee | vaciar y echar |
| `mail.mailbox.credentials_changed` (contrasena principal o de aplicacion) | no se lee: una sesion no dice con que credencial entro | vaciar y echar |
| `mail.mailbox.updated` | ya no esta, o `active` distinto de 1 (0 apagado, 2 solo recibe; tambien la baja de su empresa) | vaciar y echar |
| `mail.mailbox.updated` | `active = 1` (cuota, nombre visible, TLS, relayhost, protocolos) | solo vaciar: la siguiente autenticacion vuelve a `mail-auth`, que aplica `imap_access` y los demas; un protocolo retirado llega ademas como `credentials_changed`, que echa |
| `mail.mailbox.created` | - | nada |

  Echar a quien aun puede entrar no abre nada, pero molesta (el cliente vuelve a entrar solo con su
  credencial): por eso un cambio que no retira nada no echa a nadie.

  `mail-directory` publica `mail.mailbox.credentials_changed` con `credential`: `password` al
  cambiar la contrasena principal y cuando el propio buzon pierde un inicio de sesion que tenia (se
  apaga, pasa a solo recepcion o pierde `imap_access`, `pop3_access`, `smtp_access` o
  `sieve_access`: lo pierden todas sus credenciales, `domain.MailboxLoginsRevoked`), en la misma
  transaccion que su `mail.mailbox.updated`, para cerrar la sesion ya abierta con ese protocolo; y
  `app_password` cuando una contrasena de aplicacion pierde un
  inicio de sesion que tenia (se desactiva, se borra activa, pierde `imap_access`, `pop3_access`,
  `smtp_access` o `sieve_access`). Darla de alta, reactivarla, ampliar sus
  protocolos o renombrarla no publica nada: no deja en la cache una credencial que ya no valga, y la
  cache negativa no bloquea una contrasena buena. `dav_access` (del buzon o de la contrasena de aplicacion) tampoco
  publica `credentials_changed`: `mail-dav` verifica cada peticion contra `mail-auth`, sin sesion ni cache, y
  perderlo se aplica en la siguiente; el evento `mail.mailbox.updated` si lo lista en `changed`. Este consumidor no lee `credential`; el webmail si, y con `app_password` no cierra sus
  sesiones, porque solo admite la principal. La baja de la empresa no lo publica: cada buzon que
  apaga ya sale como `mail.mailbox.updated`, que echa, y ninguno puede reactivarse despues.

  `mail.mailbox.updated` y `mail.mailbox.credentials_changed` llevan ademas `changed`, la lista de
  atributos que cambio ese hecho (`domain.MailboxChanges`), con los nombres del JSON del buzon, o
  `password` / `app_password` cuando lo que cambio fue la credencial misma. Es aditivo y lo usa el
  WEBMAIL para no cerrar la sesion de su usuario ante un cambio que no la invalida (cuota, nombre
  visible, `kind`, TLS, relayhost, `force_pw_update`, `pop3_access`, `sieve_access`, `dav_access`), revocando ante
  cualquier otro atributo, ante uno que no reconozca y ante la falta del campo. Este consumidor NO lo
  lee: decide con el estado real del buzon, que es mas fiable que cualquier lista.
* **Idempotencia y reintento**: un `kick` sin sesiones (salida 68) es un exito y vaciar dos veces no
  cambia nada. Un fallo deja el evento sin confirmar: JetStream lo reentrega cada 90 s y tras 20
  entregas lo guarda en `EVENTS_DLQ` (`pkg/events`). Metricas
  `mail_security_dovecot_revocations_total{action}` y
  `mail_security_dovecot_revocation_failures_total{reason}` (`unreachable`, `rejected`,
  `command`, `directory`), nacidas a cero, y alerta `RevocacionEnDovecotFallida`
  (`docs/arquitectura/OBSERVABILIDAD.md`).
* **Cache**: `auth_cache_ttl` sigue en 300 s y la cache negativa en 60 s. El mecanismo es el
  vaciado; el plazo solo acota lo que queda si falla, y no acota las sesiones abiertas. Bajarlo
  multiplicaria los bcrypt de `mail-auth` en cada reconexion. La cache negativa no bloquea una
  contrasena buena: un fallo en la passdb con cache sigue en la segunda, que pregunta siempre a
  `mail-auth`.
* **Despliegue**: `DOVEADM_API_KEY` en el almacen; despues `dovecot-mail` recreado (abre el 8443);
  despues `mail-security`. La primera vez el consumidor recorre los eventos de buzon que retiene el
  stream (7 dias) y echa las sesiones de los buzones apagados o con la credencial cambiada en ese
  tiempo; quien pueda entrar vuelve solo.

V (2026-09-15, Dovecot 2.3.21.1): el vaciado por buzon con la clave `%s:%u:%w`, la cache negativa
que no bloquea, las respuestas del API y `doveadm_allowed_commands` sobre la imagen de
`deploy/mail/dovecot`; y con `make e2e-mail`: sin clave el API responde 401 y con ella `doveadm
user` sale `unAuthorized`; un buzon que acaba de entrar por IMAP y se apaga por el API se rechaza en
segundos (el control, apagarlo en la base sin evento, prueba que la cache le seguia abriendo), su
sesion IMAP abierta se cierra y al reactivarlo vuelve a entrar; tras cambiar la contrasena la
anterior se rechaza, la nueva entra y la sesion abierta se cierra; cambiar el nombre visible vacia
la cache sin cerrar la sesion, y la baja de la empresa cierra la de un buzon que acababa de entrar.
Una contrasena de aplicacion con la que el buzon acababa de entrar por IMAP se rechaza en segundos
al desactivarla y al borrarla y su sesion IMAP abierta se cierra, mientras la contrasena principal
sigue entrando y la sesion del webmail sigue abierta; reactivada, vuelve a entrar al momento.

V (2026-09-15, las dos caras de la revocacion en el webmail con `make e2e-mail`): con la sesion del
webmail abierta, cambiar la cuota y el nombre visible del buzon NO la cierra (el webmail atiende el
evento, lo registra como cambio que no invalida la sesion y la sesion sigue sirviendo carpetas) y
mail-security no echa a nadie de Dovecot por ellos; quitarle `imap_access`, apagar el buzon y
cambiar su contrasena SI la cierran al momento.

P: `dsync-server` con replica no esta probado. Queda una carrera: una autenticacion que
`mail-auth` acepto antes del cambio y que Dovecot guardara despues del vaciado (mas lenta que el
rele de la outbox) valdria hasta `auth_cache_ttl`.

## Gestor de la cola de Postfix (agente `queue-agent`)

V (2026-09-21, unitarias del agente y de mail-security, y `make e2e-mail` con Postfix real): el superadmin ve
los mensajes que Postfix aun no entrego en su celda y por que, y puede reintentarlos, retenerlos, liberarlos,
borrarlos y vaciar la cola diferida (`docs/Plan_Estrategico_Mejoras_Correo.md`, C4).

* **Por que no `dockerapi`**: mailcow lo hace con `dockerapi` (`postqueue` y `postsuper` por el socket de
  docker del host, sin autenticacion dentro de la red de los motores). Dar a un servicio Go acceso a ese API
  seria darle ejecucion de ordenes en cualquier contenedor del host. Aqui `dockerapi` no se toca ni se amplia.
* **El agente**: `postfix/queue-agent/`, un binario de Go con solo la biblioteca estandar que se compila en
  la imagen de Postfix y corre dentro del contenedor, como root porque `postsuper` solo lo admite del
  superusuario. Atiende por HTTPS en el 8590 (sin publicar; solo la red `mail-engines`) con el certificado del
  servidor de correo (se relee cada minuto, asi que la renovacion de acme no lo reinicia) y exige
  `Authorization: Bearer <QUEUE_AGENT_API_KEY>`, comparada en tiempo constante. Solo admite:
  `GET /v1/queue?limit=` (`postqueue -j`, hasta 5000, sin contenido ni asunto), `POST /v1/queue/{id}/{retry|hold|
  unhold|delete}` (`postqueue -i`, `postsuper -h|-H|-d`), `POST /v1/queue/flush` (`postqueue -f`). El
  identificador (`[0-9A-Za-z]{5,25}`) es lo unico que llega a un proceso: sin shell, sin otros argumentos, con
  los binarios como rutas absolutas fijas, una sola operacion a la vez (429 si hay otra) y plazo de 20 s. Un
  fallo de Postfix devuelve al cliente un mensaje fijo; el detalle queda en el registro del contenedor.
  `ops/scaffold/check-queue-agent.sh` (en `make checks`) exige que siga sin dependencias ni shell.
* **Endurecimiento (revision de 2026-09-21).** El agente corre como root junto a Postfix, asi que se acota lo que
  puede hacerle al contenedor:
  * **Un fallo suyo no detiene Postfix.** `stop-supervisor.sh` detenia el contenedor con la salida de CUALQUIER
    proceso, y `autorestart=true` no lo evitaba: supervisord anuncia `PROCESS_STATE_EXITED` tambien cuando va a
    reiniciar. Un panic, una falta de memoria o un `kill` al agente paraban el SMTP de la celda (comprobado con el
    supervisord real de la imagen). Ahora el oyente lee el evento y ignora solo los de `queue-agent`; los de
    Postfix, syslog-ng o un evento ilegible siguen deteniendo el contenedor. Si el agente entra en `FATAL`
    (no consigue arrancar), queda apagado y `GestorDeColaSinRespuesta` lo avisa, pero el correo sigue.
  * **Ofrecido al matador de memoria.** Se escribe `1000` en su `oom_score_adj`: si el contenedor se queda sin
    memoria, el nucleo mata antes al agente que a Postfix. Con `GOMEMLIMIT` blando de 256 MiB.
  * **Proceso no volcable** (`PR_SET_DUMPABLE` a 0 y sin volcado de memoria): otro proceso del contenedor no lee su
    memoria ni su entorno (`QUEUE_AGENT_API_KEY`).
  * **TLS 1.3 como minimo** (el unico cliente es `mail-security`, en Go), como mucho 32 conexiones abiertas a la
    vez (las demas esperan en la cola del sistema) y un solo aviso por minuto de peticiones sin clave, con su
    contador, para que un flujo de peticiones no llene el registro compartido del contenedor.
  * **Una cola gigante no lo deja ciego.** Un mensaje con decenas de miles de destinatarios produce una linea de
    `postqueue -j` mayor que el tope de lectura y antes hacia fallar el listado entero mientras siguiera en cola:
    ni la pantalla ni la vigilancia veian nada, y era justo el mensaje que habia que encontrar y borrar. Ahora una
    linea demasiado larga se descarta hasta el salto de linea conservando su comienzo (cola, identificador, llegada y
    tamano), asi que se cuenta y aparece en el listado con `recipients_capped`. Mas alla del limite pedido no se
    decodifican los destinatarios (con `limit=1`, el del monitor, cuesta una expresion regular por mensaje en lugar
    de una decodificacion JSON completa), y remitente y destinatarios se recortan a 512 caracteres.
    `postqueue -j` corre con la prioridad de CPU y de disco al minimo para no competir con la entrega.
  * **Contrapresion del monitor.** `mail-security` espera el doble, el cuadruple y como mucho cuatro intervalos
    tras fallos seguidos en lugar de repetir la consulta cada minuto contra un agente ya sobrecargado; vuelve al
    ritmo normal al primer exito. `GestorDeColaSinRespuesta` sigue viendo un fallo cada pocos minutos.
  * Sin cambios de configuracion: nada de esto exige variables nuevas.
* **Credencial**: `QUEUE_AGENT_API_KEY`, del almacen de secretos (opcional en `secret-keys.txt`), la misma en
  `postfix-mail` y en `mail-security`: de 32 a 256 caracteres de `[A-Za-z0-9_-]` (`openssl rand -hex 32`). Sin
  ella el agente no abre el puerto (y no termina, para que supervisord no lo reinicie en bucle) y `mail-security` arranca igual con el gestor desactivado (503 `NOT_CONFIGURED`), de modo que desplegar
  el codigo no exige la clave. `QUEUE_AGENT_URL` (por defecto `https://postfix:8590`),
  `QUEUE_AGENT_TLS_SERVER_NAME` (vacio = `MAIL_HOSTNAME`) y `QUEUE_AGENT_TLS_CA_FILE` los lee `mail-security`.
* **API**: `GET /api/v1/mail-security/queue?limit=` (por defecto 100, como mucho 500),
  `POST /queue/{id}/{retry|hold|unhold}` y `DELETE /queue/{id}` (204), `POST /queue/flush` (202). Permisos de
  plataforma `mail_security/queue/{read,update,delete}` (`033_mail_security_queue_permissions.sql`), que solo
  tiene el superadmin, y el caso de uso lo vuelve a exigir: la cola mezcla el correo de todas las empresas de la
  celda. Son rutas de plataforma como las del cortafuegos: el operador las alcanza en la celda destino. Cada
  accion queda en el registro de `mail-security` con quien la pidio. Pantalla: `/platform/mail-queue`.
* **Vigilancia**: `mail-security` consulta la cola cada `MAIL_QUEUE_POLL_INTERVAL` (por defecto 1 minuto, de 10 s
  a 5 min; solo con el agente configurado), pidiendo un solo mensaje porque el agente devuelve el conteo por
  cola y la llegada del mas antiguo (sin contar los retenidos) de la cola entera. Publica
  `mail_security_postfix_queue_messages{queue}`, `mail_security_postfix_queue_oldest_arrival_timestamp_seconds`,
  `mail_security_postfix_queue_last_poll_success_timestamp_seconds` y
  `mail_security_postfix_queue_poll_failures_total`. Alertas `ColaDePostfixAtascada` (un mensaje sin entregar
  y sin retener desde hace mas de 4 horas, el aviso de retraso de Postfix) y `GestorDeColaSinRespuesta`, en
  `ops/observability/prometheus/rules/plataforma.yml`, con pruebas de `promtool` (`make check-alertas`).
* **Despliegue**: migracion 033 del registro; `QUEUE_AGENT_API_KEY` en el almacen; `mail-security` (sin
  clave sigue igual); despues `postfix-mail` recreado (compila el agente). Con la clave puesta en los dos
  lados, el gestor se activa sin mas cambios.
* **Prueba**: `make e2e-mail` deja dos mensajes diferidos con `defer_transports=smtp` y comprueba, con el
  superadmin por el gateway y contra Postfix real, listar, retener, liberar, reintentar, vaciar y borrar; que un
  administrador de empresa recibe 403; y, contra el agente, la clave, el identificador y la accion.

## Webmail (usuario maestro y envio)

Avisos en tiempo real (V, 2026-09-21): ademas de las conexiones cortas por peticion, el webmail mantiene una
sesion IMAP en IDLE por buzon con la interfaz abierta (usuario maestro, INBOX en solo lectura) y la reparte por
Server-Sent Events; ver `docs/Arquitectura_Core_Force_Mail.md`. Para Dovecot son conexiones largas: cuentan en
`mail_max_userip_connections` (500 aqui, sobrado con el tope de 5 pestanas por buzon) y cada una es un proceso
`imap` residente, por lo que `WEBMAIL_EVENTS_MAX_MAILBOXES` (100 por defecto) acota la memoria. Si la celda
crece, la salida es la hibernacion de IMAP de Dovecot (`imap_hibernate_timeout` y el servicio `imap-hibernate`),
que se prueba aparte antes de activarla; no esta activa.

`services/webmail` lee por IMAP y envia por submission en nombre del buzon sin guardar su
contrasena (la comprueba `mail-auth` con service `webmail` al abrir la sesion). Cada celda
despliega el suyo contra sus motores y su `mail-auth`, con su `CELL_CODE`, que va en cada token
de sesion; el gateway lleva cada buzon al de la celda de su dominio (`WEBMAIL_CELL_HOSTS`,
`docs/Modelo_de_Datos_y_Celdas.md` 5.5):

* **IMAP**: `LOGIN "<buzon>*<maestro>@platform.local" <contrasena maestra>` contra
  `WEBMAIL_IMAP_ADDR` (`dovecot:993`, TLS implicito verificado contra `MAIL_HOSTNAME`). La
  passdb maestra (`dovecot-master.passwd`, `master = yes`, `auth_master_user_separator = *`)
  la escribe el entrypoint desde `DOVECOT_MASTER_USER`/`DOVECOT_MASTER_PASS` (32 caracteres
  o mas; el usuario solo `[a-z0-9._-]`) con `{SHA512-CRYPT}`, permisos `640 root:dovecot`
  y `allow_nets=${DOVECOT_MASTER_ALLOWED_NETS}` (por defecto `${IPV4_NETWORK}.0/24`, la red
  de los motores): una credencial maestra filtrada no abre buzones desde fuera. Tras
  autenticar al maestro, Dovecot solo busca el buzon en el userdb SQL (`active IN (1, 2)`);
  `active = 1`, `imap_access` y `smtp_access` los exige `mail-auth` al abrir la sesion y la
  revocacion por eventos (`mail.mailbox.*`) cierra las sesiones abiertas si cambian.
* **SMTP**: `WEBMAIL_SMTP_ADDR` (`postfix:587`, STARTTLS obligatorio) con `AUTH PLAIN` de
  la misma credencial. Postfix delega el SASL en Dovecot, que devuelve el nombre del BUZON;
  `reject_authenticated_sender_login_mismatch` con `smtpd_sender_login_maps` decide si el
  remitente le pertenece (el propio buzon, sus aliases con `sender_allowed` o `sender_acl`).
  El webmail escribe el mismo remitente en el sobre y en la cabecera From, y solo ofrece y
  admite las direcciones concretas que esa misma regla acepta (`mail.sender_identities`, que
  le sirve `GET /internal/mail-directory/sender-identities`); los comodines (catch-all con
  `sender_allowed`, `@dominio` o `*` en `sender_acl`) valen por SMTP pero no se enumeran.
* **Envio sin duplicados**: cada envio del webmail lleva una clave de idempotencia. Si la
  conexion con el submission se corta despues del punto final de DATA sin respuesta, el
  mensaje pudo quedar en cola (RFC 5321, 6.1): el webmail no lo reintenta con esa clave y
  responde `DELIVERY_UNCERTAIN`. Una respuesta 4xx o 5xx al final de DATA si es definitiva.
* **Puerto 588**: se retiro `check_sasl_access regexp:allow_platform_local.regexp`, que
  dejaba a cualquier nombre SASL `*@platform.local` enviar como cualquier remitente (y cuya
  expresion, sin anclar ni escapar el punto, casaba tambien con dominios como
  `platformXlocal.com`). Sin SOGo nadie la necesitaba y el usuario maestro ya no es inutil.
* **`pgsql_virtual_sender_acl`** llama a `mail.sender_login_owners('%s')`
  (`migrations/cell/canonical/mail-directory/07_sender_identities.sql`, con `EXECUTE` para
  `mail_engine`): es la consulta que tenia el mapa, sin cambios de logica, movida a la base
  de la celda para que el webmail use la misma regla. Incluye el propio buzon
  (`mail.mailboxes`, `active = 1`): mail-directory no crea el alias `buzon -> buzon` que
  mailcow daba por supuesto, y sin el todo envio autenticado desde la direccion del propio
  buzon se rechazaba.
* **ClamAV**: el webmail analiza cada adjunto con `clamd:3310` (INSTREAM) antes de enviarlo o
  guardarlo en Borradores o Enviados: el APPEND por IMAP no pasa por Rspamd. Falla cerrado, y
  cualquier respuesta que no sea un `OK` explicito (incluido pasarse de `StreamMaxLength`) es
  `503 SCAN_UNAVAILABLE`: el adjunto no se guarda. Por eso `StreamMaxLength` y `MaxFileSize`
  cubren el mayor adjunto que el servicio admite (`WEBMAIL_MAX_ATTACHMENT_BYTES`, con techo en
  `message_size_limit`), y `check-mail-size-limits.sh` compara los dos ficheros.
* **Red**: el webmail se une a `mail-engines`, que es `mynetworks` de Postfix. La garantia
  contra la suplantacion descansa en que el webmail siempre se autentica; un proceso
  comprometido dentro de esa red podria enviar sin autenticar (modelo heredado de mailcow).

V contra Dovecot y Postfix reales (2026-09-13, `make e2e-mail`): `allow_nets` se aplica a la
passdb maestra (el maestro entra desde la red de los motores y no desde fuera, donde el buzon
si entra con su contrasena); con `buzon*maestro` el nombre SASL que ve Postfix es el del
buzon (con esa credencial, `ana@` se acepta y `bea@` se rechaza con 553 citando al buzon);
el mapa `pgsql_virtual_sender_acl` llama a la funcion y responde el buzon, sus aliases con
permiso, su direccion en un dominio alias y `sender_acl`; las identidades que ofrece el webmail
son exactamente direcciones que Postfix acepta, y el webmail envia, guarda en Enviados, no
repite con la misma `Idempotency-Key` y para un adjunto EICAR con ClamAV. V contra Postgres:
que `mail.sender_login_owners` devuelve los mismos duenos que
la consulta anterior del mapa para cada caso (alias exacto y catch-all, `active` 1, 2 y 0,
dominios y dominios alias activos e inactivos, `sender_acl` concreta, `@dominio` y `*`), que
`mail_engine` puede llamarla y que `mail_app` no puede enumerar remitentes
(`services/mail-directory/internal/adapters/postgres/sender_identities_integration_test.go`).

## Migracion de buzones (ejecutor `mail-migration-runner`)

Un cliente con correo en otro proveedor entra sin perder su historial: `mail-migration` (servicio Go de la
plataforma, base de la empresa) guarda los trabajos y `mail-migration-runner` copia el correo con imapsync
hacia Dovecot. Decision y controles: `docs/adr/0002-migracion-de-buzones-con-imapsync.md`. Aqui, lo que el
ejecutor hace y lo que hay que operar.

* **Contenedor** (`migration-runner/`, `Dockerfile` multi-etapa): un binario Go de biblioteca estandar (sin
  dependencias, sin shell, sin base de datos, Redis ni NATS; `ops/scaffold/check-migration-runner.sh` lo
  vigila) mas imapsync de Alpine 3.23 (community, serie **2.314** fijada; en Debian y Ubuntu ese paquete no
  existe). Usuario 10001, `read_only`, `cap_drop: ALL`, `no-new-privileges`, `pids_limit`, memoria y CPU
  acotadas, sin puertos publicados, `ulimit core=0`. Los ficheros de trabajo van en el tmpfs
  `/run/migration` (0700, `noexec`).
* **Red.** Solo la red `mail-migration` (`MAIL_MIGRATION_NETWORK`, subred `MAIL_MIGRATION_IPV4_NETWORK`.0/24,
  bridge `br-mail-migr`), que crea `docker-compose.mail.yml`. Sus miembros: el ejecutor, Dovecot (alias
  `dovecot`), clamd (alias `clamd`) y `mail-migration` (lo une el compose de la plataforma, que la declara
  `external`). El ejecutor NO esta en `mail-engines` ni en la red de la plataforma: no ve Postfix, Redis,
  mail-auth, mail-policy ni la base. Sale a Internet por NAT hacia los IMAP de origen. Dentro de esa red
  Dovecot expone todos sus puertos, no solo el 993: **regla recomendada en el host**, en `DOCKER-USER`,
  para que el ejecutor solo pueda llegar a Dovecot por 993, a clamd por 3310 y a `mail-migration` por 8057,
  y no a las redes privadas ni al metadata del proveedor (`br-mail-migr`; la guarda de origen del
  ejecutor ya rechaza esas direcciones, la regla es la segunda barrera). Esa regla no la pone este compose.
* **Sin la clave no hace nada.** Sin `MAIL_MIGRATION_RUNNER_KEY` arranca, lo avisa por el registro, queda sano
  y no reclama trabajos. Con la configuracion incompleta o incoherente (falta un dato, `MIGRATION_ALLOW_PRIVATE_SOURCES`
  fuera de `development` y `test`, sin `MIGRATION_CLAMD_ADDR` y sin `MIGRATION_ALLOW_UNSCANNED=true`) tampoco
  reclama, y queda no sano para que se vea, sin reiniciarse en bucle. El registro dice cual es el dato.
* **Credencial de destino por trabajo (recomendada; sustituye al maestro compartido).** Con
  `MAIL_MIGRATION_JOB_CREDENTIALS=true` en `mail-migration`, cada trabajo reclamado trae su propia credencial,
  `cfmj1.<empresa>.<trabajo>.<secreto>`, que abre solo el buzon destino de ese trabajo, solo desde la red del ejecutor y
  solo mientras el trabajo esta en curso (`docs/adr/0002`, "Credencial de destino por trabajo"). El ejecutor entra al
  destino como el propio buzon (`--user2=<buzon>`, la credencial por `--passfile2`), sin maestro. En Dovecot la resuelve
  `migration-verify.lua`, la primera passdb (`dovecot.conf`): solo IMAP, solo contrasenas con el prefijo `cfmj1.` (las demas
  pasan de largo sin llamar a nadie, y una que la passdb rechace sigue por la normal), y pregunta a `mail-auth` con
  `service = migration`. **Su clave de cache lleva `%{session}`**: es unica por conexion, asi que Dovecot no reutiliza un exito y
  cancelar o cerrar el trabajo revoca en el acto; sin eso `auth_cache_ttl` (300 s) la mantendria viva cinco minutos (probado con
  Dovecot 2.3.21 real). Solo hay que recrear Dovecot para que lea la passdb y el Lua nuevos.
* **Usuario maestro propio (flujo anterior, se conserva mientras no haya credencial por trabajo).** Dovecot admite un
  segundo usuario maestro, `DOVECOT_MIGRATION_MASTER_USER` y
  `DOVECOT_MIGRATION_MASTER_PASS` (32 caracteres o mas, usuario `[a-z0-9._-]`, distinto del del webmail, las dos
  del almacen de secretos y las mismas en el ejecutor): el entrypoint anade su linea a
  `dovecot-master.passwd` con `{SHA512-CRYPT}` y `allow_nets=` `DOVECOT_MIGRATION_MASTER_ALLOWED_NETS` (por
  defecto `${MAIL_MIGRATION_IPV4_NETWORK}.0/24`, la red de la migracion) y su linea a `dovecot-master.userdb`.
  El maestro del webmail no cambia y no entra por la red de la migracion, ni el de la migracion por la de los
  motores. Sin las variables no se anade nada. El ejecutor entra al destino por IMAP con TLS implicito verificado
  contra `MIGRATION_DEST_TLS_SERVER_NAME` (`MAIL_HOSTNAME`), con `<buzon>*<maestro>@platform.local`. **Abre cualquier
  buzon de la celda desde la red del ejecutor: es el riesgo residual que la credencial por trabajo cierra.** El ejecutor
  ya no lo exige: con la credencial del trabajo prefiere esa, y sin maestro configurado solo acepta trabajos que la traigan
  (uno sin ella falla con `destination_failed` sin lanzar imapsync). Con maestro configurado lo avisa en cada arranque.
* **Que ve y que no.** Ve, por trabajo, la contrasena del buzon de origen (la recibe una vez al reclamar), el
  correo de ese buzon y el del destino. No ve la base, Redis, NATS, las claves de cifrado ni ninguna otra
  credencial. La contrasena de origen y la de destino (la credencial del trabajo o, sin ella, la del maestro) llegan a imapsync por ficheros 0400 (`--passfile1/2`) en un
  directorio de nombre aleatorio y 0700 del tmpfs; nunca por argumento ni entorno ni registro, y se borran al
  terminar el trabajo, tambien si se mata a imapsync. imapsync corre con un entorno minimo explicito (no hereda
  la clave del ejecutor ni ningun secreto), en su propio grupo de procesos y con `Pdeathsig`. De la salida de imapsync
  solo se guardan contadores; el motivo de un fallo es uno de `source_auth_failed`, `source_unreachable`,
  `source_blocked_address`, `source_tls_failed`, `destination_failed`, `quota_exceeded`, `timeout`,
  `virus_found` o `imapsync_failed`, con un mensaje fijo.
* **El origen es un servidor ajeno.** Antes de ejecutar nada, el ejecutor resuelve el host de origen y rechaza
  cualquier direccion privada, loopback, de enlace local (incluido el metadata `169.254.169.254` y
  `fd00:ec2::254`), CGNAT, multicast, no especificada, reservada o de documentacion, las IPv4 mapeadas en IPv6 y
  las formas numericas ambiguas (`2130706433`, `0x7f000001`); una resolucion con una sola direccion no permitida
  rechaza el trabajo. imapsync se conecta a la **IP ya resuelta y validada** (`--host1=<IP>`), de modo que una
  segunda resolucion no puede cambiarla (DNS rebinding), y verifica el certificado contra el nombre que dio el
  usuario (`SSL_verify_mode=1`, `SSL_verifycn_name`, SNI con `SSL_hostname`) con TLS 1.2 o mas; con una IP literal se
  verifica contra la propia IP. No hay opcion para desactivar la verificacion. Solo con `ENVIRONMENT` en
  `development` o `test`, `MIGRATION_ALLOW_PRIVATE_SOURCES=true` admite tambien lo privado (nunca lo no enrutable,
  multicast ni el metadata); en cualquier otro entorno el ejecutor se niega a trabajar con esa variable.
* **ClamAV.** El correo migrado entra por IMAP `APPEND`, que no pasa por Postfix ni por Rspamd: el ejecutor pasa
  **cada mensaje** por `imapsync --pipemess`, que lanza `migration-runner scan-filter`. El filtro envia el mensaje
  por INSTREAM a `MIGRATION_CLAMD_ADDR` (por defecto `clamd:3310`), lo devuelve intacto solo con `stream: OK` y en
  cualquier otro caso (infectado, clamd caido o con una respuesta inesperada, mensaje mayor que
  `MIGRATION_SCAN_MAX_BYTES`) sale con error: imapsync no copia el mensaje y no borra nada del origen. Falla
  cerrado: sin clamd imapsync ni empieza (la comprobacion inicial de `--pipemess` falla) y, si clamd cae a
  mitad, el ejecutor corta la pasada tras cinco mensajes sin analizar. Un mensaje rechazado deja el trabajo en
  `failed` con `virus_found` y los contadores de lo que si se copio. Sin `MIGRATION_CLAMD_ADDR` el ejecutor se niega a
  reclamar, salvo `MIGRATION_ALLOW_UNSCANNED=true`, que es una decision explicita del operador y queda en el
  registro como aviso en cada arranque. imapsync ejecuta el filtro por un shell (necesita las redirecciones de
  `--pipemess`); la linea es fija y la ruta del binario se valida.
* **Dos pasadas y limites.** Cada trabajo reclamado corre `initial` y, si la primera llego al final del buzon
  (con o sin mensajes rechazados), `catchup`, para lo que llego mientras copiaba; repetir un trabajo no duplica
  correo (imapsync compara por `Message-ID`). El ejecutor late cada mitad del arrendamiento con contadores
  (carpetas hechas y totales, mensajes copiados, omitidos y fallidos, bytes, por carpeta), respeta la cancelacion
  del servicio, abandona sin cerrar ante `409 LEASE_LOST` (o si pasa un arrendamiento entero sin latido aceptado)
  y cierra con reintentos y espera acotada. `MIGRATION_JOB_TIMEOUT` (24 h por pasada) mata el grupo de procesos.
  Las lineas de salida de imapsync y los cuerpos de las respuestas de la API estan acotados.
* **Variables** del ejecutor: `MIGRATION_API_URL` (por defecto `http://mail-migration:8057`),
  `MAIL_MIGRATION_RUNNER_KEY`, `MIGRATION_RUNNER_ID`, `MIGRATION_DEST_HOST` (`dovecot`), `MIGRATION_DEST_PORT`
  (993), `MIGRATION_DEST_TLS_SERVER_NAME` (`MAIL_HOSTNAME`), `DOVECOT_MIGRATION_MASTER_USER`,
  `DOVECOT_MIGRATION_MASTER_PASS`, `MIGRATION_CLAMD_ADDR`, `MIGRATION_ALLOW_UNSCANNED` (false),
  `MIGRATION_JOB_TIMEOUT` (24h), `MIGRATION_POLL_INTERVAL` (15s), `MIGRATION_SCAN_MAX_BYTES` (100 MiB, por debajo de
  `StreamMaxLength` de clamd), `MIGRATION_SCAN_TIMEOUT` (5m), `MIGRATION_SOURCE_PORTS` (143,993; el compose la toma
  de `MAIL_MIGRATION_SOURCE_PORTS`, la misma del servicio, para que las dos listas no diverjan), `ENVIRONMENT`.
  `MIGRATION_ALLOW_PRIVATE_SOURCES` no esta en `docker-compose.mail.yml`: solo lo pone `docker-compose.e2e.yml`.
* **Revision de seguridad (2026-09-21).** El proceso del ejecutor se marca no volcable en el arranque
  (`prctl(PR_SET_DUMPABLE, 0)`): imapsync corre con el mismo usuario y habla con servidores ajenos, y sin eso
  un imapsync comprometido leeria por `/proc/<pid>/environ` la clave del servicio y la contrasena del maestro de
  Dovecot; los hijos recuperan su estado normal al hacer `exec`. El ejecutor solo conecta a los puertos de
  `MIGRATION_SOURCE_PORTS` aunque el servicio (o su base) le diga otro: un trabajo manipulado no lo convierte en un
  sondeo de puertos. Su lista de rangos IPv6 excluye todo `2001::/23` (asignaciones de protocolos: Teredo, anycast
  de PCP y TURN, AMT, ORCHID), igual que el servicio. El analizador guarda como mucho 500 carpetas aunque el
  origen tenga millones. Del lado del servicio (`docs/adr/0002`): topes de trabajos por empresa y dia y de
  credenciales rechazadas por cuenta de origen y hora, un trabajo a la vez por ejecutor y un tope global de
  trabajos reclamados, la contrasena cifrada atada a su empresa y a su trabajo (AAD) y el limitador del listener
  del ejecutor sin fiarse de `X-Forwarded-For`.
* **Activar la credencial por trabajo y retirar el maestro (orden).** Cada paso es seguro por si solo y sin la
  configuracion nueva nada cambia:
  1. Desplegar `mail-auth`, `mail-migration`, los motores (recrear Dovecot: passdb y `migration-verify.lua` nuevos) y
     el ejecutor. La migracion `04_destination_credential.sql` la aplica el runner de empresas al desplegar.
  2. En `mail-auth`: `MAIL_MIGRATION_URL` (`http://mail-migration:8056`) y `MAIL_AUTH_JOB_ALLOWED_NETS` (la red de la
     migracion, `${MAIL_MIGRATION_IPV4_NETWORK}.0/24`) en `.env`; recrear `mail-auth`. `mail-auth` debe alcanzar a
     `mail-migration` por la red de la plataforma y con el mismo `INTERNAL_GATEWAY_TOKEN`.
  3. En `mail-migration`: `MAIL_MIGRATION_JOB_CREDENTIALS=true`; recrearlo. El aviso "migracion de buzones con el maestro
     compartido" desaparece de su arranque.
  4. Lanzar una migracion real de un buzon: en `mail.sasl_logins` aparecen inicios con `service = migration` desde la IP
     del ejecutor y ninguno con el maestro.
  5. Retirar `DOVECOT_MIGRATION_MASTER_USER` y `DOVECOT_MIGRATION_MASTER_PASS` del almacen de secretos
     (`ops/security/secrets/remove-secret.sh <CLAVE> --apply`, una y otra) y recrear Dovecot
     y el ejecutor: sin ellos no queda ninguna credencial que abra mas de un buzon. Revertir es volver a ponerlos.
  Si algo falla antes del paso 5, poner `MAIL_MIGRATION_JOB_CREDENTIALS=false` devuelve el flujo anterior sin tocar nada mas.
* **Despliegue.** `scripts/deploy-mail.sh mail-migration-runner` (imagen `core-force-mail/mail-migration-runner:<commit>`);
  Dovecot y clamd hay que recrearlos una vez para que se unan a la red. La red la crea el primer motor que la
  usa y la plataforma la declara `external`: los motores se despliegan antes que `mail-migration`. Secretos
  nuevos en el almacen: `MAIL_MIGRATION_RUNNER_KEY`, `DOVECOT_MIGRATION_MASTER_USER`, `DOVECOT_MIGRATION_MASTER_PASS`.
* **Version de imapsync.** Alpine 3.23 community, `2.314-r0` en la construccion de 2026-09-21; el `Dockerfile` la
  fija a la serie 2.314 (`imapsync~=2.314`) y comprueba `imapsync --version`. `ops/security/escanear-motores.sh` la
  escanea con el resto de las imagenes (recorre `deploy/mail/*/Dockerfile`).
* **Limites conocidos.** La verificacion del certificado usa el almacen de CA del sistema de la imagen, sin
  anclaje de certificados por proveedor. IPv6 esta soportado por la guarda y por imapsync (IO::Socket::IP), pero
  la prueba con imapsync real fue solo con IPv4 y con una IP literal. OAuth2 (Gmail y Microsoft 365 con
  autenticacion moderna) queda fuera de la primera version (ADR 0002). Un buzon con mensajes mayores que
  `MIGRATION_SCAN_MAX_BYTES` los deja sin copiar y sin analizar, y el trabajo termina en `failed`.

## Contrato HTTP de `mail-policy`

Lo sirve el servicio Go `mail-security` (`services/mail-security`): sus listeners 8081 y
9081 van en el mismo contenedor, unido a la red `mail-engines` con el alias `mail-policy`,
de modo que los ficheros de Rspamd y Postfix copiados no cambian. Sin gateway ni JWT: la
unica autenticacion es la IP de origen (`MAIL_ENGINE_ALLOWED_CIDRS`, por defecto RFC1918 y
loopback) y el cuerpo va acotado.

Todos los endpoints son HTTP plano en la red interna. Los cuerpos de respuesta
son `text/plain` salvo donde se indica. Las direcciones llegan con la etiqueta
`+tag` incluida; el servicio debe quitarla (`local+tag@d` -> `local@d`) como
hacia el PHP.

Rspamd 4 pide cada mapa HTTP (`/settings`, `/forwardinghosts`) con `HEAD` antes del `GET`
(capturado con `rspamd-4.1.4`): si el `HEAD` no responde 2xx o 304, marca la carga como
fallida y no reintenta en unos 15 minutos, y el mapa no llega a aplicarse. Los dos responden
a `HEAD` con las mismas cabeceras que el `GET` (V, 2026-09-13, `make e2e-mail`: la regla
`watchdog` de `/settings` se aplica en Rspamd).

### Puerto 8081 (mapas dinamicos)

| Ruta | Quien la llama | Peticion | Respuesta |
|---|---|---|---|
| `/aliasexp` | Rspamd, simbolo `TAG_MOO` (prefiltro, prioridad 19) | `POST`, cuerpo vacio, cabecera `Rcpt: <direccion>` | `200` con el username del buzon final **solo si** la expansion termina en exactamente un buzon; `200` vacio en cualquier otro caso (varios buzones, dominio ajeno, alias `postmaster`). `502` error SQL, `504` sin Redis |
| `/bcc` | Rspamd, simbolo `BCC` (postfiltro, prioridad 20); una llamada por destinatario con `Rcpt:` y una por remitente con `From:` | `POST`, cuerpo vacio, cabecera `Rcpt:` o `From:` | `201` + direccion BCC cuando la vista `mail.v_routing_bcc_maps` tiene fila activa (`type='rcpt'` con `local_dest = Rcpt`, o `type='sender'` con `local_dest = From`); `200` vacio si no hay. Rspamd solo actua con `201`; la copia sale por `postfix:591` |
| `/footer` | Rspamd, simbolo `DOMAIN_WIDE_FOOTER` (prioridad 1) | `POST`, cabeceras `Domain:` (dominio del envelope-from, ya resuelto alias -> target), `Username:` (usuario SASL), `From:` (envelope-from) | `200` JSON `{"html":"","plain":"","skip_replies":0,"vars":{}}` cuando no hay pie; con pie: `{"html","plain","skip_replies","vars":{atributo: valor}}` (`mbox_exclude` y `alias_domain_exclude` se evaluan en el servidor). `502` error |
| `/forwardinghosts?host=IP` | Postfix, tabla `tcp:127.0.0.1:10027` via `whitelist_forwardinghosts.sh` (postscreen_access_list) | `GET` | cuerpo `200 PERMIT` si la IP cae en algun CIDR de la hash Redis `WHITELISTED_FWD_HOST`, si no `200 DUNNO` (protocolo tcp_table de Postfix, siempre HTTP 200) |
| `/forwardinghosts` (sin `host`) | Rspamd `greylist.conf` (`whitelisted_ip`) | `GET` | lista de CIDR, uno por linea, empezando siempre por `240.240.240.240` (mapa nunca vacio) |
| `/settings` | Rspamd `settings.conf` (modulo settings, polling periodico) | `GET` con `If-Modified-Since` | `304` + `Last-Modified` si nada cambio; `200 text/plain` con UCL `settings { ... }` + `Last-Modified`. Como minimo debe emitir la regla `watchdog` (rcpt `/null@localhost/i`, from `/watchdog@localhost/i`, `reject = 9999.0`, `want_spam = yes`, `symbols_disabled = [HISTORY_SAVE, ARC, ARC_SIGNED, DKIM, DKIM_SIGNED, CLAM_VIRUS]`): el watchdog comprueba que `required_score` sea 9999. Encima van las reglas por dominio/buzon (listas blancas y negras, `settingsmap`, `MAILCOW_INTERNAL_ALIAS` para `mail.aliases.internal`). El `Last-Modified` es de la celda y no del proceso (`mail_security.engine_documents`): todas las replicas de `mail-security` responden con la misma marca |

### Puerto 9081 (exportacion de metadatos de Rspamd)

| Ruta | Quien la llama | Peticion | Respuesta |
|---|---|---|---|
| `/pipe` | `metadata_exporter`, regla `QUARANTINE` (selector `reject_no_global_bl`: accion reject/add header/rewrite subject sin lista negra global) | `POST multipart/form-data` (frontera entre comillas, cada parte `Content-Transfer-Encoding: binary`): fichero `message` (`message.eml`, RFC822 crudo) y campo `metadata` (JSON con `qid`, `subject`, `score`, `rcpt[]`, `user`, `ip`, `action`, `from`, `symbols[]`, `fuzzy[]`, `message_id`). Rspamd 4 manda cada simbolo como objeto (`name`, `score`, `options`, `groups`) y `rcpt` como la cadena `unknown` si no hay destinatarios SMTP; se admiten tambien simbolos como cadenas y se guarda el nombre de cada uno | `200` guardado; `400` partes ausentes o JSON invalido; `505` mensaje mayor que `Q_MAX_SIZE` MiB; `502` error resolviendo destinatarios; `503` error al insertar (`504` no se da: `/pipe` no depende de Redis). Expande cada rcpt hasta sus buzones finales (misma logica que `/aliasexp`) y aplica los ajustes de la EMPRESA de cada buzon (`mail_security.quarantine_settings`: tamano, dominios excluidos, retencion); guarda una fila por buzon en `mail_security.quarantine` y recorta por buzon. `505` solo si ningun buzon lo guardo por tamano |
| `/pipe_rl` | `metadata_exporter`, regla `RLINFO` (selector `ratelimited`, formato json) | `POST application/json`: `{rcpt[], from, user, symbols[{name, options[]}], qid, ip, message_id, header_subject[], header_from[]}` | `200`. El servidor extrae de `symbols[RATELIMITED].options` el texto `nombre(hash)` y hace `LPUSH RL_LOG` con `{time, rcpt, from, user, rl_info, rl_name, rl_hash, qid, ip, message_id, header_subject, header_from}` |

### Controller de Rspamd (11334), llamado por mail-security

En sentido contrario, `mail-security` llama al controller (`RSPAMD_CONTROLLER_URL`) con la contrasena
de cada permiso en la cabecera `Password` (docs/adr/0009): `/learnspam` y `/learnham` con la de
escritura; `/stat`, `/history` y `/checkv2` con la de lectura. `/checkv2` puntua sin entregar el MIME de
una plantilla (`POST /internal/mail-security/spam-check`, `docs/Plan_Editor_Correos.md`): Rspamd 4.1.4
lo atiende en el controller con la contrasena de lectura (`rspamd_controller_handle_scan` no pide la de
escritura), asi que no hace falta abrir el worker normal (11333). Se manda sin `Queue-Id` (el
aprendizaje automatico del bayesiano lo exige, `require_queue_id` de `lua_bayes_learn`), con
`Flags: no_log,no_stat` (ni historial `history_redis`, ni registro del motor, ni contadores de `/stat`)
y sin sobre SMTP: la regla `QUARANTINE` de `/pipe` puede dispararse, pero llega con `rcpt` `unknown` y no
guarda nada. Plazo de 8 s, por debajo del `task_timeout` de 30 s de `options.inc`.

`pushover` no se migra.

## Contrato Redis

Redis (`redis-mail`, `requirepass`) es el bus de configuracion en caliente entre
la plataforma y los motores. La plataforma escribe; los motores leen.

**Cifrado en transito.** `redis-mail` no ofrece TLS y sus lectores lo usan en
claro. Cifrarlo hoy obligaria a tocar los motores por dentro, porque cada lector
abre su conexion en claro desde su propio codigo o configuracion: Rspamd
(`local.d/redis.conf` que escribe `docker-entrypoint.sh`; el soporte TLS hacia
Redis es un pedido abierto aguas arriba y no esta verificado en la version
empaquetada), el destino `redis()` de syslog-ng en Postfix y Dovecot (sin opcion
TLS documentada), `netfilter/main.py` y `dockerapi/main.py` (conexion fija sin
TLS) y `watchdog.sh` (`redis-cli` y un `check_tcp` que manda `AUTH` en claro). La
replica (`REDIS_SLAVEOF_*`) tambien va en claro. La garantia es de red:
`redis-mail` no publica puertos, solo esta en el bridge `mail-engines` del host
de la celda, netfilter aisla el 6379 del bridge salvo para `MAIL_REPLICA_IP`, se
exige `requirepass` y `quota_notify` entra con un usuario ACL que solo lee
`QW_*`. Por eso `mail-security`, su unico escritor, corre en ese mismo host unido
a `mail-engines` (como ya exige el alias `mail-policy`) y su trafico hacia Redis
no sale del bridge. `MAIL_REDIS_TLS`, `MAIL_REDIS_TLS_CA_FILE` y
`MAIL_REDIS_TLS_SERVER_NAME` (mismo contrato que `REDIS_TLS*`, `pkg/config`)
quedan apagadas y nunca se exigen: se encienden solo si `redis-mail` abre ademas
un `tls-port` (Redis 7 lo sirve junto al puerto en claro) para un `mail-security`
fuera de ese host. Una replica entre hosts distintos cruza la red en claro: solo
sobre un enlace privado hasta que se cifre.

| Clave | Tipo | Escribe | Lee | Contenido |
|---|---|---|---|---|
| `DOMAIN_MAP` | hash `dominio -> 1` | mail-security (eventos `mail.domain.*`/`mail.alias_domain.*` y reconciliacion) | rspamd multimap (`RCPT_MAILCOW_DOMAIN`, `MAILCOW_DOMAIN_HEADER_FROM`), mail-policy | dominios y alias domains activos de la celda |
| `WHITELISTED_FWD_HOST` | hash `cidr -> origen` | mail-security | rspamd multimap, mail-policy `/forwardinghosts` | hosts de reenvio de confianza |
| `RL_VALUE` | hash `buzon|dominio -> "N / 1h"` | mail-security | rspamd `DYN_RL_CHECK` (lua) | ratelimits por objeto |
| `SMTP_ALLOW_NETS_<usuario>` | hash `ip|red/prefijo -> 1` | mail-security (`mail_security.smtp_access_networks`, `PUT /api/v1/mail-security/smtp-access/{usuario}` y reconciliacion) | rspamd `SMTP_ACCESS` (lua) | redes desde las que puede enviar un usuario con `SMTP_LIMITED_ACCESS`: la IP sola para un host, `red/prefijo` para el resto (IPv4 /8 a /32, IPv6 /32 a /128, lo que compara el Lua) |
| `SMTP_LIMITED_ACCESS` | hash `usuario -> 1` | mail-security (se marca despues de escribir las redes y se desmarca antes de borrarlas) | rspamd multimap | usuarios con acceso SMTP restringido |
| `KEEP_SPAM` | hash `ip|cidr -> 1` | mail-security (host de reenvio con `filter_spam=false`) | rspamd (lua, pre-result accept) | hosts cuyo spam no se filtra |
| `RCPT_WANTS_SUBFOLDER_TAG`, `RCPT_WANTS_SUBJECT_TAG` | hash `buzon -> 1` | mail-security | rspamd `TAG_MOO` | como entregar correo con `+tag` |
| `DKIM_PRIV_KEYS` | hash `selector.dominio -> clave privada PEM` | mail-security (la recibe de domain-service por `PUT /internal/mail-security/dkim/{dominio}`, solo de un dominio activo en el directorio de la celda; no la guarda en su base; en rotacion conviven dos selectores; ciclo de vida debajo de la tabla) | rspamd `dkim_signing`, `arc` | claves DKIM |
| `DKIM_SELECTORS` | hash `dominio -> selector` | mail-security | rspamd | selector por dominio |
| `QW_HTML`, `QW_SENDER`, `QW_SUBJ` | string | mail-directory | `quota_notify.py` (usuario ACL `quota_notify`, solo `GET/HGET ~QW_*`) | plantilla Jinja, remitente y asunto del aviso de cuota |
| `QW_BCC` | hash `dominio -> {"bcc_rcpts":[...],"active":1}` | mail-directory | `quota_notify.py` | copias del aviso de cuota |
| `Q_MAX_AGE` | string (dias) | mail-security | `clean_q_aged.sh` | TOPE de la celda (maximo entre empresas) |
| `Q_MAX_SIZE` (MiB), `Q_EXCLUDE_DOMAINS` (JSON), `Q_RETENTION_SIZE` | string | mail-security | informativo | TOPE de la celda: maximo entre empresas y union de dominios excluidos. Los ajustes son por empresa y los aplica `/pipe` con la fila de la empresa; Redis admite un solo valor. Por eso los cuatro tienen techo por empresa (`domain.MaxQuarantine*`, migraciones 08 y 09): sin el, lo que pide una empresa se lo lleva toda la celda |
| `F2B_OPTIONS` | string JSON | netfilter (defaults) y mail-security (`ban_time`, `max_ban_time`, `ban_time_increment`, `max_attempts`, `retry_window`, `netban_ipv4`, `netban_ipv6` de `mail_security.firewall_options`; conserva `banlist_id` y `manage_external`; sin fila en la base no la toca) | netfilter | opciones de baneo |
| `F2B_REGEX` | string JSON | netfilter (defaults) | netfilter | regex de baneo; no se expone por API: una regex mal escrita deja la celda sin baneos o banea de mas |
| `F2B_WHITELIST`, `F2B_BLACKLIST` | hash `red/prefijo -> 1` | mail-security (`mail_security.firewall_networks`, `/api/v1/mail-security/firewall/networks`, solo superadmin, y reconciliacion) | netfilter | listas del cortafuegos de la celda |
| `F2B_ACTIVE_BANS`, `F2B_PERM_BANS` | hash | netfilter | netfilter, watchdog, mail-security (`GET /api/v1/mail-security/firewall/bans`) | estado de baneos |
| `F2B_QUEUE_UNBAN` | hash `red/prefijo -> 1` | mail-security (`POST /api/v1/mail-security/firewall/bans/unban`, solo baneos temporales vigentes) | netfilter | desbaneos pendientes |
| `F2B_CHANNEL` | pub/sub | syslog-ng de postfix y dovecot | netfilter | lineas de log a evaluar |
| `F2B_LOG` / `NETFILTER_LOG`, `POSTFIX_MAILLOG`, `DOVECOT_MAILLOG`, `ACME_LOG`, `WATCHDOG_LOG`, `RL_LOG` | list (LPUSH, recortadas por `trim_logs.sh` a `LOG_LINES`) | motores | plataforma (UI de logs) | logs JSON |
| `DOVECOT_REPL_HEALTH`, `ACME_FAIL_TIME` | string | dovecot / acme | watchdog | estado |
| `MC_CHANNEL` | pub/sub | plataforma | dockerapi | `{"api_call":"container_post","post_action":"exec|restart|...","container_name":"...","request":{"cmd":..,"task":..}}` |

Claves DKIM (V, 2026-09-13): `DKIM_PRIV_KEYS` y `DKIM_SELECTORS` solo tienen claves de
dominios activos en el directorio de la celda (`mail.v_routing_domains.active`, no un dominio
alias) cuya empresa sigue existiendo para organization. `mail-security` lo aplica en tres
sitios, siempre con el cerrojo del dominio (`pg_advisory_xact_lock` en la base de la celda,
compartido por las replicas) y leyendo el directorio dentro de el:

* Al escribir: `PUT /internal/mail-security/dkim/{dominio}` con `{"keys":[{"selector","private_key_pem"},...]}`
  (una o dos, en orden; la ultima firma) deja exactamente ese juego y retira los demas
  selectores del dominio. Un dominio que la celda no sirve es 409 `DKIM_DOMAIN_NOT_ACTIVE` y no
  se escribe nada; uno de otra empresa, 403. La forma anterior (`selector` y
  `private_key_pem`, una clave por llamada, conserva las demas) sigue aceptada con la misma
  regla mientras quede un domain-service anterior desplegado.
* Al desactivarse o borrarse un dominio en el directorio: el consumidor durable
  `mail-security-redis` de `mail.domain.*` (idempotente, reentrega y DLQ de `pkg/events`) retira
  sus claves con el estado real, no el del evento.
* Repaso periodico con cerrojo de lider cada `MAIL_DKIM_RECONCILE_INTERVAL` (15 min por
  defecto, de 1 s a 15 min: el techo es el intervalo con que cuentan sus alertas): retira las
  de un dominio que no esta activo o cuya empresa organization ya no
  conoce (baja terminada). Sin respuesta de organization no toca esa empresa. Lo que encuentra
  es un camino que fallo y queda en el registro (`repaso DKIM`) y en las metricas
  `mail_security_dkim_reconcile_*`, con sus alertas (`docs/arquitectura/OBSERVABILIDAD.md`).

domain-service solo publica las claves de un dominio corporativo verificado (un dominio solo de
envio no firma en la celda) y retira la clave en gracia en la celda antes de olvidarla mientras
el dominio pueda tenerla alli. En una rotacion programada la clave anterior sigue en los motores
`MAIL_DKIM_ROTATION_GRACE` desde la ultima vez que pudo firmar, y ese plazo tiene que superar
`maximal_queue_lifetime` de `postfix/conf/main.cf.base` (5d) mas un dia de TTL de un TXT: el correo
firmado con ella puede seguir en la cola hasta entonces. Alargar la cola exige subir
`minDKIMRotationGrace` en `services/domain-service/main.go`; `ops/scaffold/check-dkim-grace.sh`
(`make checks`) falla si no. Una clave comprometida no espera a la gracia: la revocacion de
domain-service hace un solo `PUT` con la clave nueva, que queda firmando mientras mail-security
retira al momento los demas selectores del dominio.

Rspamd guarda ademas sus propias estructuras (bayes, fuzzy, history, ratelimit,
reputation) en el mismo Redis.

## Consultas SQL

Postfix: `postfix/postfix.sh` genera en `/opt/postfix/conf/sql/` un fichero por
mapa (`pgsql_relay_ne`, `pgsql_relay_recipient_maps`,
`pgsql_tls_policy_override_maps`, `pgsql_tls_enforce_in_policy`,
`pgsql_sender_dependent_default_transport_maps`, `pgsql_transport_maps`,
`pgsql_virtual_resource_maps`, `pgsql_sasl_passwd_maps_sender_dependent`,
`pgsql_sasl_passwd_maps_transport_maps`, `pgsql_virtual_alias_domain_maps`,
`pgsql_virtual_alias_maps`, `pgsql_recipient_canonical_maps`,
`pgsql_virtual_domains_maps`, `pgsql_virtual_mailbox_maps`,
`pgsql_virtual_relay_domain_maps`, `pgsql_virtual_sender_acl`,
`pgsql_mbr_access_maps`, `pgsql_virtual_spamalias_maps`). Todas usan
`hosts = ${MAIL_DB_HOST}:${MAIL_DB_PORT}` y las tablas `mail.*`; el tri-estado
`active` de buzones y aliases se respeta (`IN (1, 2)` para recibir, `= 1` para
enviar). `pgsql_virtual_sender_acl` no lleva la consulta sino la llamada a
`mail.sender_login_owners('%s')`: la regla de `smtpd_sender_login_maps` vive en la base de la
celda porque el webmail la necesita al reves (`mail.sender_identities`).

Los mapas que miran un ajuste del buzon por su direccion (`pgsql_tls_enforce_in_policy`, la
rama `tls_enforce_out` y la del relayhost propio de
`pgsql_sender_dependent_default_transport_maps`, y `pgsql_sasl_passwd_maps_sender_dependent`)
buscaban el buzon solo como destino de un alias con esa direccion: mailcow creaba el alias
`buzon -> buzon` y mail-directory no, asi que nunca se aplicaban. Ahora tambien casan el
propio buzon y su direccion en un dominio alias. Ademas `pgsql_sasl_passwd_maps_sender_dependent`
busca primero el relayhost del buzon y despues el del dominio, la misma precedencia que el
mapa de transporte: al reves, un buzon con relayhost propio salia por el suyo con la
credencial del del dominio. V con `postmap -q` (2026-09-13, `make e2e-mail`).

Dovecot: `dovecot/docker-entrypoint.sh` genera `sql/dovecot-dict-sql-userdb.conf`
(`user_query`/`iterate_query` contra `mail.mailboxes`), el dict de cuota contra
`mail.quota_usage` (Dovecot hace upsert por `username`) y los dicts de sieve
contra `mail.v_sieve_before`/`mail.v_sieve_after` y, para la respuesta automatica de cada buzon,
`mail.v_sieve_vacation` (`dovecot-dict-sql-sieve_vacation.conf`, en la tercera ranura `sieve_after3` de
`dovecot.conf`: despues del filtro del usuario y de los globales, de modo que un mensaje que estos
descartan o archivan como spam no recibe respuesta). La vista solo trae lo activo y mail-directory genera
el script; `mail_engine` no lee la tabla `mail.vacation_replies`. V con `make e2e-mail` (2026-09-21).

ACME: `SELECT domain FROM mail.domains WHERE NOT backupmx AND active`.

## Maildir de un buzon borrado (V, 2026-09-21)

Borrar un buzon por el API de mail-directory quita su fila de `mail.mailboxes` (y, por
`mail.mailbox.deleted`, su respuesta automatica, sus contactos y calendarios de mail-dav y sus
trabajos de migracion), pero su maildir `/var/vmail/<dominio>/<local>/` sigue en el volumen
`vmail-vol` de Dovecot: ningun servicio Go lo alcanza. Visto en produccion: tras borrar y volver
a crear una direccion, el buzon nuevo NACIO con los mensajes del anterior (quien recibe una
direccion reutilizada hereda el correo de su titular anterior), `doveadm` decia "User doesn't
exist" con el maildir aun en el disco, y 13 directorios huerfanos ocupaban espacio. mailcow lo
resuelve moviendo el maildir a `_garbage` desde su PHP por dockerapi; aqui dockerapi no se
amplia, el API de doveadm solo admite la revocacion y ningun servicio recibe el socket de docker
(seccion "Gestor de la cola", `docs/adr/0004`). Lo que se hace:

* **Marca de baja en la transaccion del borrado.** `DeleteMailbox` inserta en
  `mail.mailbox_deletions` (`migrations/cell/canonical/mail-directory/12_mailbox_deletions.sql`:
  `id`, `tenant_id`, `mailbox_id`, `username`, `local_part`, `domain`, `deleted_at`) en la misma
  transaccion que borra la fila: sin poder dejar la marca el buzon no se borra. `mail_app` solo
  deja y ve las de su empresa; `mail_engine` (Dovecot) las lee y las **borra**, y nada mas: es la
  unica tabla del esquema, ademas de `quota_usage`, en la que el rol de los motores escribe, y
  una marca es una orden para el barrido sin credenciales ni contenido.
* **Barrido en el contenedor de Dovecot.** `dovecot/maildir_reconcile.sh`, lanzado por
  `dovecot/crontab` cada 5 minutos (solo `MASTER=y`) como `vmail`, con la misma conexion de
  solo lectura del userdb (`MAIL_DB_*`, `psql`). Nunca borra: mueve a
  `/var/vmail/_garbage/<epoch>_<dominio>_<local>/` (un dominio entero, a `<epoch>_<dominio>/`),
  la misma convencion que mailcow, y `maildir_gc.sh` lo purga a los `MAILDIR_GC_TIME` minutos
  (por defecto 7200, cinco dias: la ventana para deshacer). Dos pasadas:
  1. **Marcas**: para cada marca, si el maildir existe y es anterior a la baja (algo en el tiene
     mtime no posterior a `deleted_at`) se mueve y la marca se consume. Si el buzon se volvio a
     crear con el mismo nombre (fila con `created_at` posterior a la marca), la referencia es esa
     alta: un maildir en el que todo es posterior lo creo Dovecot para el buzon nuevo y no se
     toca (la marca se consume); uno con algo anterior es del titular anterior y se mueve
     entero, y se anota cuantos ficheros posteriores al alta se fueron con el (correo del buzon
     nuevo, recuperable en `_garbage` durante `MAILDIR_GC_TIME`): se elige mover porque la
     exposicion del correo del anterior no se puede deshacer y la perdida si.
  2. **Huerfanos**: todo `/var/vmail/<dominio>/<local>` sin fila en `mail.mailboxes` (en
     cualquier estado: un buzon apagado, por ejemplo el de una empresa dada de baja, conserva
     su fila y no se toca) y todo `/var/vmail/<dominio>` sin fila en `mail.domains`. Cubre lo
     borrado antes de que existiera la marca, lo que quede de un `_garbage` restaurado a mano y
     la purga futura de una empresa dada de baja: cuando esa purga borre las filas, este barrido
     se lleva sus maildir por el mismo camino, sin codigo nuevo.
* **Cuidados.** El disco se lista ANTES de consultar la base, asi que un maildir que Dovecot
  crea despues de la consulta no esta en la lista y uno que si esta tenia su fila antes de ella;
  ademas un directorio mas joven que `MAILDIR_RECONCILE_GRACE` minutos (por defecto 10, mtime del
  propio directorio) no se mueve. Fail-closed: cualquier fallo o salida inesperada de `psql`
  aborta la pasada sin mover nada, y una consulta de buzones o de dominios vacia tambien (una
  base vacia por error vaciaria el servidor). Tope de `MAILDIR_RECONCILE_MAX_MOVES` movimientos
  por pasada (por defecto 50). Solo directorios regulares, nunca enlaces; solo nombres con forma
  de dominio y de parte local, y nunca `_garbage`, `sieve*` ni `platform.local` (el resto se
  ignora y se anuncia). `flock` sobre `_garbage/.reconcile.lock`, compartido con
  `maildir_gc.sh`: nunca se solapa consigo mismo ni con la purga. Cada movimiento se registra
  con nombre, destino, tamano y motivo, nunca contenido; la salida va al registro del contenedor
  (crond la vuelca a su stdout). Ejecutado a mano como root (`docker exec`) se reejecuta como
  `vmail`. Las sesiones IMAP del buzon borrado ya las cerro mail-security
  (`mail.mailbox.deleted`, "Revocacion en Dovecot"), asi que ningun proceso escribe en el
  maildir que se mueve.
* **Retencion de la direccion en mail-directory.** Crear un buzon con una direccion cuya marca
  de baja sigue viva y es mas joven que `MAIL_DIRECTORY_MAILBOX_RECREATE_HOLD` (por defecto
  `15m`; `0` la desactiva) responde 409 `ADDRESS_RECENTLY_DELETED`: la funcion
  `mail.mailbox_deletion_pending` (SECURITY DEFINER, solo un booleano) responde por toda la
  celda, tambien si el nombre cambio de empresa. En cuanto el barrido consume la marca, la
  direccion se crea al momento. La retencion tiene que ser mayor que la gracia mas la cadencia
  del barrido (10 + 5 minutos).
* **Ventana residual, con honestidad.** En operacion normal el buzon nuevo nunca nace sobre el
  maildir del anterior: la marca lo retiene hasta que el barrido lo movio (a lo sumo 5 minutos
  tras el borrado). La ventana se abre solo si el barrido no corre durante toda la retencion
  (Dovecot parado o `MASTER=n`, cron caido, base inaccesible: cada pasada abortada queda en el
  registro): pasados esos 15 minutos la direccion se puede crear y, hasta la siguiente pasada,
  el buzon nuevo ve el correo del anterior; esa pasada mueve el maildir entero (con lo que el
  buzon nuevo hubiera recibido, recuperable en `_garbage`) y el buzon nuevo sigue limpio desde
  ahi. Lo borrado antes de desplegar esto no tiene marca: la pasada de huerfanos lo mueve en
  las primeras ejecuciones, de 50 en 50.
* **Baja de una empresa.** No mueve nada: la baja desactiva y conserva el directorio durante la
  retencion, y sus buzones conservan la fila (`active = 0`), asi que el barrido no los toca; los
  maildir acaban en `_garbage` cuando la purga de la empresa borre sus filas
  (`docs/Modelo_de_Datos_y_Celdas.md`, 5.4).
* **Probado.** `ops/scaffold/check-maildir-reconcile.sh` (en `make checks`): arbol de maildir y
  `psql` falsos, cada regla y dos mutaciones. Pruebas de integracion de mail-directory contra
  Postgres real (marca en la transaccion, retencion, funcion entre empresas, permisos de
  `mail_engine`). La imagen construida y el guion ejecutado dentro de ella como root con un
  arbol y un `psql` falsos (reejecucion como `vmail`, cerrojo, `flock FILE PROG` de busybox,
  `find -newermt @epoch`, salida de crond). La seccion "Maildir de un buzon borrado" de
  `ops/e2e/mail.sh` (Dovecot real: entrega, borrado, 409, pasada sin base que no mueve nada,
  pasada real, `_garbage/<epoch>_acme.test_dani`, buzon recreado que nace vacio, buzon vivo con
  correo que no se mueve) esta escrita y SIN ejecutar.
* **Despliegue.** Primero la migracion de celda 12 en todas las celdas (sin ella el
  mail-directory nuevo no puede borrar buzones: la marca es parte del borrado), luego
  mail-directory, y por ultimo recrear el motor `dovecot-mail` (`scripts/deploy-mail.sh`:
  imagen nueva con el guion y el crontab). Un Dovecot anterior no consume marcas: hasta
  recrearlo, borrar y recrear una direccion responde 409 durante 15 minutos y despues se crea
  sobre el maildir anterior, como hasta ahora.

## Tareas periodicas (Dovecot)

`dovecot/crontab` lo ejecuta busybox `crond` bajo supervisord (mismo contenedor,
sin scheduler externo):

| Tarea | Frecuencia | Que hace |
|---|---|---|
| `trim_logs.sh` | cada hora (`MASTER=y`) | recorta las listas de log en Redis a `LOG_LINES` |
| `clean_q_aged.sh` | diaria (`MASTER=y`) | busca `mail.quarantine`, que no existe (la cuarentena es `mail_security.quarantine`), y no hace nada. La poda por antiguedad la hace `mail-security` en su reconciliacion, con el `max_age_days` de cada empresa, sin dar a `mail_engine` permiso de borrado |
| `maildir_gc.sh` | cada 30 min | purga `/var/vmail/_garbage` con mas de `MAILDIR_GC_TIME` min (ctime, que `mv` renueva); bajo el mismo cerrojo que el barrido |
| `maildir_reconcile.sh` | cada 5 min (`MASTER=y`) | mueve a `_garbage` el maildir de cada buzon borrado (marcas de `mail.mailbox_deletions`, que consume) y los maildir y dominios sin fila en el directorio; seccion "Maildir de un buzon borrado" |
| `sa-rules.sh` | diaria 03:00 | descarga reglas SpamAssassin de Heinlein y reinicia rspamd via dockerapi si cambian |
| `optimize-fts.sh` | diaria | `doveadm fts optimize -A` si FTS activo |
| `repl_health.sh` | cada 5 min | publica `DOVECOT_REPL_HEALTH` |

## Ficheros generados en tiempo de ejecucion

Los bind mounts de `postfix/conf` y `dovecot/conf` reciben ficheros generados
por los entrypoints, igual que en mailcow: `postfix/conf/sql/*.cf`, `sni.map*`,
`dns_blocklists.cf`, `dnsbl_reply.map`, `extra.cf`, `custom_transport.pcre`,
`custom_postscreen_whitelist.cidr`; `dovecot/conf/sql/*.conf`, `sni.conf`,
`shared_namespace.conf`, `mail_replica.conf`, `dovecot-master.*`,
`mail_plugins*`, `acl_anyone`. `rspamd/custom/` recibe `platform_networks.map`,
`dovecot_trusted.map`, `rspamd_trusted.map`, `sa-rules` y `dqs-rbl.conf`. No
deben versionarse.

## Aviso de cuarentena (V, 2026-09-13)

Lo que hacia `quarantine_notify.py` lo hace `mail-security`, y el correo sale por
`transactional` (SES, clase transaccional), no por los motores de la celda. Probado con
pruebas unitarias e integracion contra Postgres con las migraciones de la celda aplicadas
dos veces; sin prueba de punta a punta con SES.

* Barrido al arrancar y cada `MAIL_QUARANTINE_NOTIFY_INTERVAL` (15m, de 1m a 24h) con cerrojo de lider
  en la base de la celda (`db.TryLeaderLock`): por empresa con `notify_enabled` y ajustes
  validos, las filas con `notified = false` y `score <= notify_max_score`, agrupadas por
  buzon final (las 100 mas recientes; el resto sale en el aviso siguiente). Un buzon que ya
  no existe, no recibe o es un recurso se da por atendido sin enviar (`skipped`).
* Una llamada por buzon a `POST /internal/transactional/messages` de la empresa con
  `purpose = quarantine_notice`, `Idempotency-Key = quarantine-notice:<buzon>:<id mas
  reciente>` (con el sha256 del buzon si la clave pasara de 200 caracteres), remitente
  `notify_sender`, asunto `notify_subject` y el HTML de `notify_html_template`.
  `transactional` aplica sus reglas de siempre (remitente verificado, reputacion, un solo
  destinatario sin copias) y la supresion normal: un buzon suprimido no recibe el aviso.
* 2xx, tambien con el buzon suprimido: `notified = true` y registro en
  `mail_security.quarantine_notices` en UNA transaccion. 4xx de negocio (400, 403, 404,
  409, 422...): registro `rejected` con el codigo y marcado, sin reintento. 429, 401, 408,
  5xx o red: nada cambia y el siguiente barrido repite con la misma clave, asi que
  `transactional` devuelve lo que ya creo si el primer intento llego.
* `notify_html_template` es `html/template` de Go: `{{.Mailbox}}`, `{{.Count}}`,
  `{{.LinksExpireAt}}` y `{{range .Messages}}` con `.Subject`, `.Sender`, `.Date`, `.Score`,
  `.ReleaseURL` y `.DiscardURL`. Asunto y remitente son contenido hostil: se sanean (sin
  caracteres de control ni de direccion de texto, 200 y 254 runas) y salen escapados segun
  el contexto donde los ponga la plantilla. Con el aviso activo, `PUT
  /quarantine-settings` exige remitente con forma de direccion, asunto de una linea y una
  plantilla que se interprete y ejecute; el HTML renderizado tiene un tope de 1 MiB.
* Enlaces sin sesion, declarados en `services/gateway/routes.json` (`public`):
  `GET|POST /api/v1/public/mail-security/quarantine/<celda>/release` y `.../<celda>/discard`
  con `t` (empresa), `q` (qhash), `e` (caducidad, segundos Unix) y `sig` (HMAC-SHA256 con
  `MAIL_LINK_SIGNING_KEY` sobre `quarantine-link/v2`, celda, empresa, id del mensaje, accion
  y caducidad). La celda es `CELL_CODE` de la instancia que emite el aviso (sin un
  `CELL_CODE` valido no hay aviso y todo enlace es invalido). Ejemplo:
  `https://app.example.com/api/v1/public/mail-security/quarantine/pe-01/release?e=1789900000&q=<qhash>&sig=<hmac>&t=<empresa>`.
* Enrutado por celda (V, 2026-09-13): el segmento `<celda>` va sin firmar y el gateway enruta
  por el sin verificar nada (no recibe `MAIL_LINK_SIGNING_KEY`). `mail-security` declara en
  `routes.json` `cell_hosts_env: MAIL_SECURITY_CELL_HOSTS`, variable del gateway con las
  instancias por celda (`pe-02=mail-security-pe-02:8042,eu-west-1=10.0.2.15:8042`; mal
  formada, el gateway no arranca). Una celda listada va a su instancia; cualquier otro
  segmento, conocido o no, va al destino base (`MAIL_SECURITY_HOST`, la celda `GATEWAY_BASE_CELL_CODE`),
  que lo rechaza con la misma pagina 403 que una firma alterada: el gateway no responde nada
  propio y no sirve para enumerar celdas. Cada instancia acepta solo su celda, en la ruta y
  en la firma: un segmento cambiado lleva el enlace a una celda que lo rechaza. Con una sola
  celda la variable queda vacia y todo va al destino base. No hay ruta sin celda: el gateway
  no arranca con una ruta publica de `mail-security` que no lleve `{cell}`.
* Caducan a `MAIL_QUARANTINE_LINK_TTL` (72h, de 1h a 720h). GET muestra una confirmacion sin
  JavaScript y POST ejecuta: liberar es el caso de uso de siempre (reinyeccion por el puerto
  590, borrado y evento por la outbox en una transaccion con la fila bloqueada) y descartar
  borra la fila. El uso se registra en `mail_security.quarantine_link_uses` (una fila por
  mensaje, con la ip de `X-Real-IP` y el user agent) en esa misma transaccion: un solo uso
  por mensaje aunque lleguen dos peticiones a la vez. Firma alterada, caducado, usado o
  mensaje inexistente dan la misma pagina 403. 30 peticiones por minuto e ip. Avisos y
  usos se podan con el `max_age_days` de la empresa.
* Sin `MAIL_LINK_SIGNING_KEY` (32 caracteres o mas) y `PUBLIC_BASE_URL` el servicio arranca
  sin aviso y todo enlace es invalido; sin `TRANSACTIONAL_URL` o `INTERNAL_GATEWAY_TOKEN`,
  sin aviso. Ambos casos quedan en el log como error. Una `TRANSACTIONAL_URL` mal formada, o un
  intervalo o una vigencia fuera de su rango, impiden arrancar.

## Prueba de punta a punta de los motores (`make e2e-mail`)

`ops/e2e/mail.sh` levanta esta pila contra una celda real y comprueba que el correo circula,
con una linea OK o FALLA por comprobacion; termina con error si alguna falla y lo derriba
todo al acabar (`E2E_KEEP=1` lo deja en pie para depurar). Necesita docker con compose v2,
Go, `openssl`, `python3` y salida a internet (imagenes, firmas de ClamAV, DNS publico).

```
make e2e-mail
E2E_KEEP=1 make e2e-mail                     # deja contenedores, red y registros
E2E_MAIL_PORT_BASE=29100 make e2e-mail       # otro rango de puertos (29000-29099 por defecto)
E2E_MAIL_IPV4_NETWORK=172.31.29 make e2e-mail  # otra /24 si 172.30.29.0/24 esta ocupada
E2E_MAIL_PURGE_SIGNATURES=1 make e2e-mail    # borra al final el volumen de firmas de ClamAV
```

Como se monta:

* **Plataforma y celda por el camino real**: Postgres, NATS y Redis desechables con
  credenciales aleatorias (`ops/e2e/lib.sh`, compartido con la prueba de la plataforma),
  migraciones de la celda (incluida `07_sender_identities.sql`), rol propio de la celda
  (`ops/db/cell-service-role.sh`), contrasena aleatoria de `mail_engine`,
  `ops/db/bootstrap-platform.sh`, alta de la empresa por el gateway y login de su
  `tenant_admin`. El plano de control y domain-service corren como binarios del host.
* **Dominio verificado de verdad**: domain-service da de alta `acme.test` y lo verifica contra
  un Unbound autoritativo de la prueba (`MAIL_DNS_RESOLVER`) que publica exactamente los
  registros que domain-service pide; la verificacion activa el dominio en mail-directory y
  entrega la clave DKIM a mail-security. Buzones, aliases, dominio alias, `sender_acl`, alias
  de spam, reescrituras, politica TLS, transportes, relayhosts y un backup MX se crean por el
  API de mail-directory a traves del gateway.
* **Servicios de la celda en contenedores** (`docker-compose.e2e.yml`, con sus Dockerfile):
  mail-directory, mail-auth, mail-security (alias `mail-policy`) y webmail, en la red de los
  motores y con la credencial de la celda, publicados solo en `127.0.0.1` para el gateway y
  domain-service. El directorio se maneja como `tenant_admin`, que `pkg/authz` no consulta en
  access-control (rol del sistema); un usuario con rol de empresa necesitaria que
  access-control sea alcanzable desde la red de la celda.
* **Migracion de buzones**: `mail-migration` corre como binario del host (con `MAIL_MIGRATION_RUNNER_KEY` solo en
  su entorno) y `mail-migration-runner`, con imapsync real, en la red `E2E_MIGRATION_NETWORK` (subred
  `E2E_MAIL_MIGRATION_IPV4_NETWORK`, por defecto `172.30.30`); llega a la API del ejecutor por
  `host.docker.internal`. Dovecot lleva en esa red una IP fija (`.250`) que es tambien un SAN del certificado de
  la prueba. `mail-migration` corre con `MAIL_MIGRATION_JOB_CREDENTIALS=true` y `mail-auth` (en su contenedor) con
  `MAIL_MIGRATION_URL` hacia el host y `MAIL_AUTH_JOB_ALLOWED_NETS` con la red de la migracion: la seccion "Credencial de
  destino por trabajo" de `ops/e2e/mail.sh` detiene el ejecutor, reclama trabajos a mano con su clave y comprueba contra
  Dovecot real que la credencial abre solo el buzon del trabajo desde la red del ejecutor (no otro buzon, no la de otro
  trabajo, no desde la red de los motores, no como maestro), no aparece en ningun registro, y deja de abrir al cancelar, al
  cerrar el trabajo y al borrar el buzon. Escrita y **sin ejecutar** (solo `bash -n`).

Que comprueba: los 18 mapas pgsql con `postmap -q` (valor esperado o sin resultado, y que no
quede un mapa generado sin comprobacion), en particular `smtpd_sender_login_maps` sobre
`mail.sender_login_owners`; `doveadm user`; IMAP con la contrasena del buzon a traves de
`passwd-verify.lua` y mail-auth (y su registro en `mail.sasl_logins`), contrasena mala, el
usuario maestro del webmail y su `allow_nets`; submission autenticado con STARTTLS verificado,
entrega por LMTP leida por IMAP, firma DKIM de Rspamd con el selector de domain-service,
rechazos 553 de `reject_authenticated_sender_login_mismatch` (tambien con la credencial
maestra), envio como alias con permiso, como dominio alias y por `sender_acl`; un adjunto
EICAR rechazado al final de DATA (`CLAM_VIRUS` -> `VIRUS_FOUND` -> reject) y guardado en la
cuarentena del destinatario por `/pipe`; un mensaje de unos 27 MiB con EICAR al final, por
encima de los topes con los que el antivirus se saltaba el analisis en silencio (20 MiB de
`max_size`, 25 MiB de `StreamMaxLength`), analizado entero y rechazado igual; su enlace de liberar, firmado por la prueba con la
forma `quarantine-link/v2` y seguido por el gateway (la celda cambiada a una desconocida y
una firma alterada dan la misma pagina 403 byte a byte sin tocar nada; GET confirma sin
ejecutar; POST libera, registra el uso y la reinyeccion por el 590 lo entrega; usado, el
mismo 403); que Rspamd aplica la regla `watchdog` del mapa
`settings` de mail-policy y ve `DOMAIN_MAP`; Unbound con validacion DNSSEC; el webmail por el
gateway (sesion, carpetas, identidades frente a Postfix, envio idempotente, lectura del
destinatario, Enviados, remitente ajeno, EICAR, cierre de sesion); la revocacion en Dovecot
(el API de doveadm con su clave y sus ordenes permitidas; un buzon que acaba de entrar y se apaga,
cambia de contrasena o cae con la baja de su empresa se rechaza al momento y pierde su sesion
IMAP; un cambio de nombre no la cierra); la migracion de buzones (el ejecutor aislado en su red y sin
privilegios, las dos credenciales maestras acotadas cada una a su red, dos migraciones reales de un buzon a otro
con imapsync, la repeticion sin duplicar, una contrasena de origen equivocada, un mensaje EICAR que ClamAV
detiene sin tocar el origen, y que la contrasena de origen no queda en la base de la empresa ni en los registros);
y registros sin errores ni reinicios.

Diferencias con produccion (solo en `docker-compose.e2e.yml` y el entorno del script; ningun
fichero de configuracion de los motores cambia para la prueba):

| Prueba | Produccion | Motivo |
|---|---|---|
| Certificado de una CA propia de la ejecucion en `/etc/ssl/mail` | Let's Encrypt por `acme-mail` | ACME necesita DNS publico y HTTP-01 hacia la maquina |
| Sin `acme-mail`, `netfilter-mail`, `watchdog-mail` ni `dockerapi-mail` | Los cuatro | netfilter corre privilegiado en la red del host y reescribe su cortafuegos; dockerapi monta el socket de docker del host; watchdog vigila a los anteriores |
| Ningun puerto de los motores en el host; IPv6 apagado; red y bridge propios | 25, 465, 587, 143/993, 110/995, 4190 publicados | La prueba habla desde dentro de la red (cliente IMAP/SMTP en `ops/e2e/mail_client.py`) |
| El ejecutor de migracion con `ENVIRONMENT=test` y `MIGRATION_ALLOW_PRIVATE_SOURCES=true`, y la CA de la prueba como almacen de certificados | `ENVIRONMENT=production`, sin fuentes privadas, CA publicas | La prueba migra entre dos buzones del mismo Dovecot, que esta en una red privada que la guarda de origen rechaza en produccion; la verificacion del certificado sigue activa (contra la CA de la prueba) |
| `SKIP_UNBOUND_HEALTHCHECK=y` | `n` | El chequeo hace ping a resolvers publicos y los runners de CI no dejan salir ICMP; la resolucion con DNSSEC se comprueba aparte |
| Firmas de ClamAV en un volumen que sobrevive entre ejecuciones | Volumen del despliegue | freshclam actualiza por diferencias en vez de bajar la base entera cada vez (la CDN de ClamAV limita las descargas repetidas) |
| DNS de `acme.test` servido por un Unbound de la prueba | DNS del cliente | El dominio de la prueba no existe en internet |
| mail-security sin `TRANSACTIONAL_URL` | Con transactional | El aviso de cuarentena sale por SES; queda desactivado y lo registra como error, que la prueba descuenta. El enlace del aviso lo firma la propia prueba con `MAIL_LINK_SIGNING_KEY` de la ejecucion |

En CI corre en su propio flujo (`.github/workflows/mail-engines.yml`), sin bloquear: cuando
cambia algo de lo que prueba, cada noche y a mano. En local, una ejecucion con las imagenes ya
construidas tarda menos de dos minutos (259 comprobaciones, 211 s, 2026-09-15); construirlas desde
cero, unos seis mas, y la primera descarga de firmas de ClamAV, uno o dos.

## Pendientes

* `/footer`: `vars` lleva `from` y `domain`; faltan los atributos personalizados del
  buzon. `mail.v_routing_mailboxes` ya publica `attributes`, pero ningun API de
  `mail-directory` los escribe (quedan en `{}`): hace falta ese API y que `/footer` los
  mezcle en `vars`.
* MTA-STS: la politica de cada dominio ya vive en `mail.mta_sts_policies` (`10_mta_sts.sql`) y `mail-directory` la sirve
  en `GET /public/mail-directory/mta-sts/{cell}/{dominio}`, pero `acme` sigue sin pedir certificados `mta-sts.<dominio>`
  (`MTA_STS_ACTIVE_DOMAINS` queda vacio a proposito, `acme/acme.sh` no cambia) y ningun motor lee esa tabla: hace falta una
  vista `v_*` de solo los dominios activos con politica para `mail_engine` y llenar el arreglo, y que el borde atienda esos
  nombres. Es decision de quien opera el servidor (`docs/adr/0003-mta-sts-y-tls-rpt-entrantes.md`).
* Contrasena del rol de los motores (`<MAIL_DB_NAME>_engine`): la fija operacion; llega solo
  por `MAIL_DB_PASSWORD`.
* **Smoke test obligatorio del primer despliegue con el rol por celda.** El cambio de
  `MAIL_DB_USER` esta probado contra Postgres (`pkg/db/service_roles_integration_test.go` y
  `make e2e`), pero NO con Postfix y Dovecot reales: `make e2e-mail` sigue usando el rol
  compartido. Un permiso de menos aqui es correo que no se recibe, asi que en el primer
  despliegue de cada celda, con los motores ya arrancados con el rol nuevo y ANTES de
  `--retire-shared`:

  ```bash
  # 1. Los 18 mapas de Postfix resuelven con el rol nuevo (uno por fichero de conf/sql).
  for m in /opt/postfix/conf/sql/*.cf; do
    docker compose exec postfix-mail postmap -q "buzon@<dominio>" "pgsql:$m" || echo "FALLA $m"
  done
  # 2. Dovecot resuelve el buzon (userdb) y su cuota.
  docker compose exec dovecot-mail doveadm user "buzon@<dominio>"
  docker compose exec dovecot-mail doveadm quota get -u "buzon@<dominio>"
  # 3. Entrega y lectura reales: un mensaje de prueba al buzon y un IMAP LOGIN + SELECT INBOX.
  # 4. El rol NO ve credenciales (tiene que responder "permission denied"):
  docker compose exec postfix-mail psql "$MAIL_DB_URL" -c 'SELECT 1 FROM mail.app_passwords LIMIT 1'
  ```

  Si (1) o (2) fallan para algun mapa, el rol no hereda un SELECT: se revisa la membresia
  (`\du <MAIL_DB_NAME>_engine` debe listar `mail_engine`) antes de tocar ningun GRANT suelto.
* `SPAMHAUS_ASN_CHECK_URL`: sin servicio propio, usar `SPAMHAUS_DQS_KEY`.
