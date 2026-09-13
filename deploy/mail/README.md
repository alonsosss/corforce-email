# Motores de correo (deploy/mail)

Motores de correo de Core Force Mail, tomados de mailcow-dockerized (commit
`02552ffefdf0` de agosto de 2026) y adaptados para leer el directorio de correo
desde PostgreSQL (esquema `mail`, rol `mail_engine`) y para delegar en dos
servicios Go de la plataforma, `mail-auth` y `mail-policy`, todo lo que mailcow
resolvia con su capa PHP. Aqui no hay interfaz web, ni MariaDB, ni SOGo, ni
nginx, ni memcached, ni ofelia.

Los ficheros de configuracion copiados conservan sus comentarios originales en
ingles. Los comentarios nuevos estan en espanol.

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
| `redis/redis-conf.sh` | Arranque de Redis con `requirepass` y el usuario ACL `quota_notify` | `data/conf/redis` |
| `ssl-example/` | Certificado snake-oil inicial y `dhparams.pem` | `data/assets/ssl-example` |
| `templates/quota.tpl` | Plantilla Jinja del aviso de cuota | `data/assets/templates` |
| `docker-compose.mail.yml` | Compose solo de los motores | nuevo |

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
* **Eliminado tambien:** imapsync (scripts, cron, dependencias Perl), la tabla
  `versions`/GUID, `quarantine_notify.py`, la regla `PUSHOVERMAIL` y el selector
  `mailcow_rcpt` de `metadata_exporter.conf`, los checks de nginx, mysql,
  mysql_repl, phpfpm, sogo y `external_checks` del watchdog, los handlers
  `mysql_upgrade`, `mysql_tzinfo_to_sql` y `reload nginx` del dockerapi, y los
  mapas BCC de Postfix (mailcow ya los aplicaba solo desde Rspamd).
* **Cron.** Los jobs de Dovecot que lanzaba ofelia ahora los ejecuta busybox
  `crond` dentro del contenedor, gestionado por supervisord (ver mas abajo).
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
4. Si `mail-policy` escribe los mapas de `rspamd/custom/` (listas globales), debe
   correr con uid/gid 82, que es el propietario que fija el entrypoint de Rspamd.

## Variables de entorno

Comunes a casi todos: `TZ`, `LOG_LINES`, `IPV4_NETWORK` (por defecto `172.22.1`),
`IPV6_NETWORK`, `REDIS_SLAVEOF_IP`/`REDIS_SLAVEOF_PORT` (replica), `MAIL_REDIS_HOST`
(por defecto `redis`), `MAIL_REDIS_PASSWORD`.

| Contenedor | Variables propias |
|---|---|
| `postfix-mail` | `MAIL_HOSTNAME`, `MAIL_DB_HOST`, `MAIL_DB_PORT`, `MAIL_DB_NAME`, `MAIL_DB_USER`, `MAIL_DB_PASSWORD`, `MAIL_POLICY_HOST`, `SKIP_LETS_ENCRYPT`, `SPAMHAUS_DQS_KEY`, `SPAMHAUS_ASN_CHECK_URL` |
| `dovecot-mail` | `MAIL_HOSTNAME`, `MAIL_DB_*`, `MAIL_AUTH_URL`, `DOVECOT_MASTER_USER`, `DOVECOT_MASTER_PASS`, `DOVECOT_MASTER_ALLOWED_NETS`, `MAIL_REPLICA_IP`, `DOVEADM_REPLICA_PORT`, `MAILDIR_GC_TIME`, `ACL_ANYONE`, `SKIP_FTS`, `FTS_HEAP`, `FTS_PROCS`, `MAILDIR_SUB`, `MASTER`, `COMPOSE_PROJECT_NAME` |
| `rspamd-mail` | `MAIL_POLICY_HOST`, `SPAMHAUS_DQS_KEY`, `SKIP_OLEFY` |
| `acme-mail` | `MAIL_HOSTNAME`, `MAIL_DB_*`, `ADDITIONAL_SAN`, `AUTODISCOVER_SAN`, `SKIP_LETS_ENCRYPT`, `DIRECTORY_URL`, `ENABLE_SSL_SNI`, `SKIP_IP_CHECK`, `SKIP_HTTP_VERIFICATION`, `ONLY_MAIL_HOSTNAME`, `LE_STAGING`, `SNAT_TO_SOURCE`, `SNAT6_TO_SOURCE`, `ACME_DNS_CHALLENGE`, `ACME_DNS_PROVIDER`, `ACME_ACCOUNT_EMAIL`, `COMPOSE_PROJECT_NAME` |
| `watchdog-mail` | `MAIL_HOSTNAME`, `USE_WATCHDOG`, `WATCHDOG_NOTIFY_EMAIL`, `WATCHDOG_NOTIFY_BAN`, `WATCHDOG_NOTIFY_START`, `WATCHDOG_SUBJECT`, `WATCHDOG_NOTIFY_WEBHOOK`, `WATCHDOG_NOTIFY_WEBHOOK_BODY`, `WATCHDOG_VERBOSE`, `IP_BY_DOCKER_API`, `CHECK_UNBOUND`, `SKIP_CLAMD`, `SKIP_OLEFY`, `SKIP_LETS_ENCRYPT`, `*_THRESHOLD`, `MAILQ_CRIT`, `DEV_MODE`, `COMPOSE_PROJECT_NAME` |
| `netfilter-mail` | `SNAT_TO_SOURCE`, `SNAT6_TO_SOURCE`, `MAIL_REPLICA_IP`, `DISABLE_NETFILTER_ISOLATION_RULE` (usa `IPV4_NETWORK.249` para Redis porque va en `network_mode: host`) |
| `postfix-tlspol-mail` | `DEV_MODE` |
| `redis-mail` | `MAIL_REDIS_PASSWORD`, `MAIL_REDIS_MASTER_PASSWORD` |
| `clamd-mail` | `SKIP_CLAMD` |
| `olefy-mail` | `OLEFY_*`, `SKIP_OLEFY` |
| `unbound-mail` | `SKIP_UNBOUND_HEALTHCHECK` |
| `dockerapi-mail` | solo las comunes de Redis |

Obligatorias sin valor por defecto: `MAIL_HOSTNAME`, `MAIL_DB_HOST`,
`MAIL_DB_NAME`, `MAIL_DB_PASSWORD`, `MAIL_REDIS_PASSWORD`. Ninguna va en el
compose: llegan por `.env` o por el orquestador.

## Red, IPs fijas y puertos

Red `mail-engines` (`${IPV4_NETWORK}.0/24`, bridge `br-mail`): unbound `.254`,
redis `.249`, dovecot `.250`, postfix `.253`; rspamd con `hostname: rspamd`.
`netfilter-mail` corre en `network_mode: host` y aisla los puertos 3306, 6379,
8983 y 12345 del bridge salvo para `MAIL_REPLICA_IP`.

Publicados: 25 (SMTP), 465 (SMTPS), 587 (submission), 143/993 (IMAP), 110/995
(POP3), 4190 (ManageSieve). Internos: postfix 588 (submission interna sin TLS
obligatorio, `submission_host` de Dovecot y avisos de cuota), 590 (reinyeccion
de cuarentena), 591 (copias BCC), 589 (watchdog), 10025/10465/10587 (HAProxy);
dovecot 24 (LMTP), 10001 (SASL para Postfix), 12345 (doveadm); rspamd 9900
(milter), 11332-11334 y `/var/lib/rspamd/rspamd.sock`; postfix-tlspol 8642;
clamd 3310; olefy 10055; dockerapi 443.

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

Cualquier otro codigo o un JSON invalido se trata como fallo de contrasena
(`PASSDB_RESULT_PASSWORD_MISMATCH`), lo que invalida la cache de auth de ese
usuario. El servicio debe validar contrasena principal y `mail.app_passwords`
(acotadas por `service`), respetar `mailboxes.active = 1` y los flags
`imap_access`/`pop3_access`/`smtp_access`/`sieve_access`, y registrar el login en
`mail.sasl_logins`. Dovecot cachea resultados 300 s (negativos 60 s).

Lo implementa `services/mail-auth`: listener TLS en `MAIL_AUTH_TLS_PORT` (9082)
con `MAIL_AUTH_TLS_CERT`/`MAIL_AUTH_TLS_KEY` (sin ellos, certificado autofirmado
en memoria avisado en log); `POST /` y `POST /auth`; `service` se traduce a flag
(`imap`, `pop3`, `smtp`/`submission`/`lmtp` -> `smtp_access`,
`sieve`/`managesieve` -> `sieve_access`; `webmail` exige `imap_access` Y
`smtp_access`, porque el webmail lee y envia con la credencial maestra, que no vuelve a
pasar por aqui; cualquier otro se deniega); bcrypt sobre la contrasena principal y
despues sobre las de aplicacion activas con ese flag (actualiza `last_used_at`; el
webmail solo acepta la principal); `active = 2` y `active = 0` no entran;
`force_pw_update` no bloquea. Freno de fuerza bruta en el Redis de la plataforma
por `(username, real_rip)` y por `real_rip` (`MAIL_AUTH_MAX_FAILURES` 10,
`MAIL_AUTH_MAX_FAILURES_PER_IP` 50, `MAIL_AUTH_FAILURE_WINDOW` 15m,
`MAIL_AUTH_LOCK_TTL` 30m); sin Redis arranca sin freno y lo avisa. Metrica
`mail_auth_attempts_total{service,result}`. El listener HTTP de `MAIL_AUTH_PORT`
(8041) solo sirve `/healthz`, `/metrics` y
`GET /internal/mail-auth/logins?username=&limit=` (tras `X-Gateway-Token` +
`X-Tenant-ID`). El contenedor debe unirse a `mail-engines` con el alias
`mail-auth`.

## Webmail (usuario maestro y envio)

`services/webmail` lee por IMAP y envia por submission en nombre del buzon sin guardar su
contrasena (la comprueba `mail-auth` con service `webmail` al abrir la sesion):

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
* **ClamAV**: el webmail analiza cada adjunto con `clamd:3310` (INSTREAM, `StreamMaxLength`
  25M) antes de enviarlo o guardarlo en Borradores o Enviados: el APPEND por IMAP no pasa
  por Rspamd.
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
| `DKIM_PRIV_KEYS` | hash `selector.dominio -> clave privada PEM` | mail-security (la recibe de domain-service por `PUT /internal/mail-security/dkim/{dominio}`; no la guarda en su base; en rotacion conviven dos selectores) | rspamd `dkim_signing`, `arc` | claves DKIM |
| `DKIM_SELECTORS` | hash `dominio -> selector` | mail-security | rspamd | selector por dominio |
| `QW_HTML`, `QW_SENDER`, `QW_SUBJ` | string | mail-directory | `quota_notify.py` (usuario ACL `quota_notify`, solo `GET/HGET ~QW_*`) | plantilla Jinja, remitente y asunto del aviso de cuota |
| `QW_BCC` | hash `dominio -> {"bcc_rcpts":[...],"active":1}` | mail-directory | `quota_notify.py` | copias del aviso de cuota |
| `Q_MAX_AGE` | string (dias) | mail-security | `clean_q_aged.sh` | TOPE de la celda (maximo entre empresas) |
| `Q_MAX_SIZE` (MiB), `Q_EXCLUDE_DOMAINS` (JSON), `Q_RETENTION_SIZE` | string | mail-security | informativo | TOPE de la celda: maximo entre empresas y union de dominios excluidos. Los ajustes son por empresa y los aplica `/pipe` con la fila de la empresa; Redis admite un solo valor |
| `F2B_OPTIONS` | string JSON | netfilter (defaults) y mail-security (`ban_time`, `max_ban_time`, `ban_time_increment`, `max_attempts`, `retry_window`, `netban_ipv4`, `netban_ipv6` de `mail_security.firewall_options`; conserva `banlist_id` y `manage_external`; sin fila en la base no la toca) | netfilter | opciones de baneo |
| `F2B_REGEX` | string JSON | netfilter (defaults) | netfilter | regex de baneo; no se expone por API: una regex mal escrita deja la celda sin baneos o banea de mas |
| `F2B_WHITELIST`, `F2B_BLACKLIST` | hash `red/prefijo -> 1` | mail-security (`mail_security.firewall_networks`, `/api/v1/mail-security/firewall/networks`, solo superadmin, y reconciliacion) | netfilter | listas del cortafuegos de la celda |
| `F2B_ACTIVE_BANS`, `F2B_PERM_BANS` | hash | netfilter | netfilter, watchdog, mail-security (`GET /api/v1/mail-security/firewall/bans`) | estado de baneos |
| `F2B_QUEUE_UNBAN` | hash `red/prefijo -> 1` | mail-security (`POST /api/v1/mail-security/firewall/bans/unban`, solo baneos temporales vigentes) | netfilter | desbaneos pendientes |
| `F2B_CHANNEL` | pub/sub | syslog-ng de postfix y dovecot | netfilter | lineas de log a evaluar |
| `F2B_LOG` / `NETFILTER_LOG`, `POSTFIX_MAILLOG`, `DOVECOT_MAILLOG`, `ACME_LOG`, `WATCHDOG_LOG`, `RL_LOG` | list (LPUSH, recortadas por `trim_logs.sh` a `LOG_LINES`) | motores | plataforma (UI de logs) | logs JSON |
| `DOVECOT_REPL_HEALTH`, `ACME_FAIL_TIME` | string | dovecot / acme | watchdog | estado |
| `MC_CHANNEL` | pub/sub | plataforma | dockerapi | `{"api_call":"container_post","post_action":"exec|restart|...","container_name":"...","request":{"cmd":..,"task":..}}` |

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
contra `mail.v_sieve_before`/`mail.v_sieve_after`.

ACME: `SELECT domain FROM mail.domains WHERE NOT backupmx AND active`.

## Tareas periodicas (Dovecot)

`dovecot/crontab` lo ejecuta busybox `crond` bajo supervisord (mismo contenedor,
sin scheduler externo):

| Tarea | Frecuencia | Que hace |
|---|---|---|
| `trim_logs.sh` | cada hora (`MASTER=y`) | recorta las listas de log en Redis a `LOG_LINES` |
| `clean_q_aged.sh` | diaria (`MASTER=y`) | busca `mail.quarantine`, que no existe (la cuarentena es `mail_security.quarantine`), y no hace nada. La poda por antiguedad la hace `mail-security` en su reconciliacion, con el `max_age_days` de cada empresa, sin dar a `mail_engine` permiso de borrado |
| `maildir_gc.sh` | cada 30 min | purga `/var/vmail/_garbage` con mas de `MAILDIR_GC_TIME` min |
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

* Barrido al arrancar y cada `MAIL_QUARANTINE_NOTIFY_INTERVAL` (15m) con cerrojo de lider
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
  segmento, conocido o no, va al destino base (`MAIL_SECURITY_HOST`, la celda por defecto),
  que lo rechaza con la misma pagina 403 que una firma alterada: el gateway no responde nada
  propio y no sirve para enumerar celdas. Cada instancia acepta solo su celda, en la ruta y
  en la firma: un segmento cambiado lleva el enlace a una celda que lo rechaza. Con una sola
  celda la variable queda vacia y todo va al destino base. No hay ruta sin celda: el gateway
  no arranca con una ruta publica de `mail-security` que no lleve `{cell}`.
* Caducan a `MAIL_QUARANTINE_LINK_TTL` (72h). GET muestra una confirmacion sin
  JavaScript y POST ejecuta: liberar es el caso de uso de siempre (reinyeccion por el puerto
  590, borrado y evento por la outbox en una transaccion con la fila bloqueada) y descartar
  borra la fila. El uso se registra en `mail_security.quarantine_link_uses` (una fila por
  mensaje, con la ip de `X-Real-IP` y el user agent) en esa misma transaccion: un solo uso
  por mensaje aunque lleguen dos peticiones a la vez. Firma alterada, caducado, usado o
  mensaje inexistente dan la misma pagina 403. 30 peticiones por minuto e ip. Avisos y
  usos se podan con el `max_age_days` de la empresa.
* Sin `MAIL_LINK_SIGNING_KEY` (32 caracteres o mas) y `PUBLIC_BASE_URL` el servicio arranca
  sin aviso y todo enlace es invalido; sin `TRANSACTIONAL_URL` o `INTERNAL_GATEWAY_TOKEN`,
  sin aviso. Ambos casos quedan en el log como error.

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

Que comprueba: los 18 mapas pgsql con `postmap -q` (valor esperado o sin resultado, y que no
quede un mapa generado sin comprobacion), en particular `smtpd_sender_login_maps` sobre
`mail.sender_login_owners`; `doveadm user`; IMAP con la contrasena del buzon a traves de
`passwd-verify.lua` y mail-auth (y su registro en `mail.sasl_logins`), contrasena mala, el
usuario maestro del webmail y su `allow_nets`; submission autenticado con STARTTLS verificado,
entrega por LMTP leida por IMAP, firma DKIM de Rspamd con el selector de domain-service,
rechazos 553 de `reject_authenticated_sender_login_mismatch` (tambien con la credencial
maestra), envio como alias con permiso, como dominio alias y por `sender_acl`; un adjunto
EICAR rechazado al final de DATA (`CLAM_VIRUS` -> `VIRUS_FOUND` -> reject) y guardado en la
cuarentena del destinatario por `/pipe`; su enlace de liberar, firmado por la prueba con la
forma `quarantine-link/v2` y seguido por el gateway (la celda cambiada a una desconocida y
una firma alterada dan la misma pagina 403 byte a byte sin tocar nada; GET confirma sin
ejecutar; POST libera, registra el uso y la reinyeccion por el 590 lo entrega; usado, el
mismo 403); que Rspamd aplica la regla `watchdog` del mapa
`settings` de mail-policy y ve `DOMAIN_MAP`; Unbound con validacion DNSSEC; el webmail por el
gateway (sesion, carpetas, identidades frente a Postfix, envio idempotente, lectura del
destinatario, Enviados, remitente ajeno, EICAR, cierre de sesion); y registros sin errores ni
reinicios.

Diferencias con produccion (solo en `docker-compose.e2e.yml` y el entorno del script; ningun
fichero de configuracion de los motores cambia para la prueba):

| Prueba | Produccion | Motivo |
|---|---|---|
| Certificado de una CA propia de la ejecucion en `/etc/ssl/mail` | Let's Encrypt por `acme-mail` | ACME necesita DNS publico y HTTP-01 hacia la maquina |
| Sin `acme-mail`, `netfilter-mail`, `watchdog-mail` ni `dockerapi-mail` | Los cuatro | netfilter corre privilegiado en la red del host y reescribe su cortafuegos; dockerapi monta el socket de docker del host; watchdog vigila a los anteriores |
| Ningun puerto de los motores en el host; IPv6 apagado; red y bridge propios | 25, 465, 587, 143/993, 110/995, 4190 publicados | La prueba habla desde dentro de la red (cliente IMAP/SMTP en `ops/e2e/mail_client.py`) |
| `SKIP_UNBOUND_HEALTHCHECK=y` | `n` | El chequeo hace ping a resolvers publicos y los runners de CI no dejan salir ICMP; la resolucion con DNSSEC se comprueba aparte |
| Firmas de ClamAV en un volumen que sobrevive entre ejecuciones | Volumen del despliegue | freshclam actualiza por diferencias en vez de bajar la base entera cada vez (la CDN de ClamAV limita las descargas repetidas) |
| DNS de `acme.test` servido por un Unbound de la prueba | DNS del cliente | El dominio de la prueba no existe en internet |
| mail-security sin `TRANSACTIONAL_URL` | Con transactional | El aviso de cuarentena sale por SES; queda desactivado y lo registra como error, que la prueba descuenta. El enlace del aviso lo firma la propia prueba con `MAIL_LINK_SIGNING_KEY` de la ejecucion |

En CI corre en su propio flujo (`.github/workflows/mail-engines.yml`), sin bloquear: cuando
cambia algo de lo que prueba, cada noche y a mano. En local, una ejecucion con las imagenes ya
construidas tarda menos de dos minutos (168 comprobaciones, 2026-09-13); construirlas desde
cero, unos seis mas, y la primera descarga de firmas de ClamAV, uno o dos.

## Pendientes

* `/footer`: `vars` lleva `from` y `domain`; faltan los atributos personalizados del
  buzon. `mail.v_routing_mailboxes` ya publica `attributes`, pero ningun API de
  `mail-directory` los escribe (quedan en `{}`): hace falta ese API y que `/footer` los
  mezcle en `vars`.
* MTA-STS: sin tabla `mta_sts`, ACME no pide certificados `mta-sts.<dominio>`.
* Contrasena de `mail_engine`: la fija operacion; llega solo por `MAIL_DB_PASSWORD`.
* `SPAMHAUS_ASN_CHECK_URL`: sin servicio propio, usar `SPAMHAUS_DQS_KEY`.
