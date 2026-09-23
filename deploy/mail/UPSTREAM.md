# Libro de parches de deploy/mail frente a mailcow-dockerized

Estado: V (medido el 2026-09-21 contra el commit base, tras el primer port real). Lo mantiene `ops/upstream/upstream.sh` y lo vigila
`ops/scaffold/check-upstream-ledger.sh`: un fichero nuevo o cambiado sin explicar aqui rompe `make checks`.

Los motores de `deploy/mail/` se copiaron una vez de mailcow-dockerized (`CLAUDE.md`: no se vuelve a copiar
ningun fichero). Cada version nueva de mailcow se porta a mano. Este libro convierte esa tarea en una lista:
dice que ficheros son nuestros, por que difieren y como se rehace el cambio.

## 1. Base

| Dato | Valor |
|---|---|
| Commit base | `ca07d8d33318` (2026-09-21, merge de la PR 7477) |
| Version de mailcow mas reciente que lo contiene | `2026-09` |
| Commits de mailcow posteriores a la base, el 2026-09-21 | 0: la base es la punta de su rama principal |
| Base anterior | `02552ffefdf0` (2026-08-18, `2026-07b`); el port entre ambas (7 commits, incidencia #13) se hizo el 2026-09-21 y se anota en la seccion 10 |
| Manifiesto legible por maquina | `deploy/mail/upstream-manifest.tsv` |
| Mapa entre nuestras carpetas y las de mailcow | `ops/upstream/mapa.tsv` |

## 2. Cuanto divergimos

| Estado | Ficheros | Que significa |
|---|---|---|
| Identico a mailcow | 86 | Sin cambios: portar una version es reaplicar el mismo cambio |
| Modificado | 64 | Tiene equivalente en mailcow y nosotros lo cambiamos (secciones 5 y 6) |
| Nuevo | 47 | Sin equivalente en mailcow (seccion 7) |
| Propio | 6 | Nuestro por definicion: compose, README y este libro |
| Total seguido por git | 203 | |

De los 64 modificados, 28 solo cambian una etiqueta o un nombre (categorias `etiqueta` y `nombres`): son
mecanicos. Los otros 36 cambian comportamiento (PostgreSQL, quitar SOGo y PHP, servicios Go, cron o
endurecimiento).

El coste real se concentra en cuatro ficheros, los que reescriben logica de mailcow y no solo un nombre:

| Fichero | Lineas anadidas y quitadas respecto a mailcow (2026-09-20) |
|---|---|
| `postfix/postfix.sh` | 244 y 326 |
| `watchdog/watchdog.sh` | 70 y 328 |
| `dovecot/docker-entrypoint.sh` | 98 y 100 |
| `acme/acme.sh` | 40 y 76 |

Regla para reducir divergencia: si un cambio se puede resolver con configuracion (`extra.cf`, `custom/`,
`local.d/`, variables de entorno o los compose), no se edita un fichero copiado.

## 3. Como portar una version de mailcow

1. `ops/upstream/upstream.sh informe` (necesita red: clona solo `data/Dockerfiles`, `data/conf` y `data/assets`).
   Lista lo que cambio en mailcow desde la base, en lo que copiamos, y lo cruza con este libro:
   cambio limpio (nuestro fichero es identico al suyo), a revisar (lo modificamos nosotros), sin
   equivalente local, y lo que no copiamos por diseno. Marca los commits con palabras de seguridad y los
   cambios de version de imagenes base.
2. Para cada cambio, reaplicarlo a mano, sin copiar el fichero entero: en un fichero identico es el mismo
   cambio; en uno modificado, leer su entrada de la seccion 5 y aplicarlo sobre nuestra version. Los ficheros
   con fin de linea CRLF en mailcow (`netfilter/main.py`, `dovecot/supervisord.conf`) se comparan con
   `diff --strip-trailing-cr`.
3. `make e2e-mail` en verde antes de subir. Es la unica prueba que ejercita los motores reales.
4. `ops/upstream/upstream.sh regenerar <checkout> --base <sha> <fecha> <etiqueta>` reescribe el manifiesto con
   la base nueva y conserva las categorias; los ficheros que pasen a modificados quedan `sin-clasificar`.
5. Clasificar y explicar en este libro lo que quede sin clasificar, y `make checks`.
6. Desplegar de un motor a la vez con `scripts/deploy-mail.sh`, como ya se hace.

La herramienta solo lee y avisa: nunca escribe en `deploy/mail/`. Cada semana un flujo de GitHub Actions
(`.github/workflows/upstream-mailcow.yml`) corre el informe y abre o actualiza una incidencia cuando hay
cambios relevantes.

## 4. Categorias

| Categoria | Significado |
|---|---|
| `etiqueta` | Cambio cosmetico: la etiqueta `maintainer` del Dockerfile. |
| `nombres` | Variables, hosts o nombres renombrados (`MAILCOW_*` a `MAIL_*`, `REDISPASS` a `MAIL_REDIS_PASSWORD`, la red `mail-engines`). |
| `postgres` | MySQL y MariaDB sustituidos por PostgreSQL (esquema `mail`, rol `mail_engine`). |
| `sogo-php` | Se quita SOGo, PHP, nginx o MySQL, que no se copiaron. |
| `servicios-go` | Una llamada a un `.php` de nginx pasa a un servicio Go: `mail-policy` o `mail-auth`. |
| `cron` | Las tareas que lanzaba ofelia las ejecuta crond dentro del contenedor. |
| `tls` | Cambios de TLS propios. |
| `endurecimiento` | Mejora propia de seguridad o robustez que no existe en mailcow. |
| `arranque` | Adaptacion del arranque o del despliegue. |
| `cola` | El agente de la cola de Postfix (`postfix/queue-agent/`), propio, y lo que hace falta para compilarlo y arrancarlo dentro de la imagen. |
| `migracion` | El ejecutor de la migracion de buzones (`migration-runner/`, con imapsync), propio, y el usuario maestro de Dovecot que usa. |

## 5. Ficheros modificados

Ordenados por carpeta. "Como se rehace" es lo que hay que hacer cuando mailcow cambie ese fichero.

| Fichero | Categorias | Por que difiere | Como se rehace |
|---|---|---|---|
| `acme/Dockerfile` | etiqueta, postgres | Etiqueta del mantenedor y postgresql-client en lugar de mariadb-client. | Reaplicar las dos lineas. |
| `acme/acme.sh` | postgres, nombres, sogo-php | Lee los dominios activos de mail.domains con psql en lugar de MariaDB, espera con pg_isready y no a nginx, y no pide certificados de mta-sts.<dominio> porque el esquema mail aun no tiene tabla de politicas. | Reaplicar el cambio de mailcow en la logica de certificados y volver a poner las esperas y las consultas SQL propias. |
| `acme/functions.sh` | nombres | MAILCOW_HOSTNAME pasa a MAIL_HOSTNAME. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `acme/obtain-certificate-dns.sh` | nombres | REDISPASS y el host de Redis pasan a MAIL_REDIS_PASSWORD y MAIL_REDIS_HOST. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `acme/obtain-certificate.sh` | nombres | REDISPASS y el host de Redis pasan a MAIL_REDIS_PASSWORD y MAIL_REDIS_HOST. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `acme/reload-configurations.sh` | sogo-php, nombres | Sin nginx: quita su recarga; usa la red mail-engines en lugar de <proyecto>_mailcow-network. | Reaplicar el cambio sobre las funciones de Dovecot y Postfix; no reintroducir nginx. |
| `clamav/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `clamav/clamd.conf` | endurecimiento | StreamMaxLength, MaxFileSize, MaxScanSize y AlertExceedsMax atados al tamano maximo de Postfix: un mensaje mayor no se analizaba y se entregaba como limpio. | Conservar los cuatro valores; ops/scaffold/check-mail-size-limits.sh falla si dejan de casar con Postfix, Rspamd y mail-security. |
| `clamav/clamd.sh` | nombres | clamd-mailcow pasa a clamd-mail. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dockerapi/Dockerfile` | etiqueta, endurecimiento | Etiqueta del mantenedor. Sin `pip` en la imagen final: `pip` trae `msgpack` 1.1.2 embebido, con una vulnerabilidad alta que no se puede corregir desde aqui hasta que salga un `pip` nuevo, y el motor no lo usa al ejecutarse. | Reaplicar el cambio de mailcow y conservar `pip3 uninstall -y pip`. |
| `dockerapi/docker-entrypoint.sh` | nombres | Organizacion del certificado autofirmado: mailcow pasa a platform. | Reaplicar la linea. |
| `dockerapi/main.py` | nombres | REDISPASS y el host de Redis pasan a MAIL_REDIS_PASSWORD y MAIL_REDIS_HOST. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dockerapi/modules/DockerApi.py` | sogo-php | Quita las tareas de MySQL y de SOGo (mysql_upgrade, sogo rename_user...). | Reaplicar los cambios de mailcow en las demas tareas; no reintroducir las quitadas. |
| `dovecot/Dockerfile` | etiqueta, postgres, sogo-php, endurecimiento | Sin grupo sogo, imapsync ni Perl; drivers de PostgreSQL (`lua-sql-postgres`) en lugar de los de MySQL y MariaDB; `gosu` se compila en una etapa propia con el Go actual (el binario publicado 1.19 lleva la libreria estandar de Go 1.24.6, con una critica y veintiuna altas con arreglo que `gosu` no alcanza pero un escaner cuenta); copia `maildir_reconcile.sh` (seccion 7). | Reaplicar el cambio de mailcow, volver a quitar SOGo, Perl e imapsync, cambiar los drivers y conservar la etapa `gosu` (misma version, `GOSU_VERSION`, compilada con Go 1.26) y el `COPY` de `maildir_reconcile.sh`. |
| `dovecot/clean_q_aged.sh` | postgres, nombres | Poda la cuarentena con psql sobre mail.quarantine; hoy no hace nada porque esa tabla no existe: la poda la hace mail-security. | Reaplicar el cambio de mailcow y volver a poner la consulta propia. |
| `dovecot/conf/auth/passwd-verify.lua` | servicios-go | La URL de autenticacion es ${MAIL_AUTH_URL} (mail-auth), que el entrypoint renderiza con envsubst, y el comentario del bloque que trata la caida del backend nombra a mail-auth y no a nginx. Ese bloque es el arreglo de mailcow 7150 (portado el 2026-09-21): si `https.request` no devuelve un codigo numerico (red, DNS, tiempo agotado) responde `INTERNAL_FAILURE`, que conserva la entrada de cache de Dovecot, en lugar de `PASSWORD_MISMATCH`, que la borraba en cada reinicio de mail-auth. Aplica igual a nuestra logica porque llamamos al mismo `ssl.https` con la misma tupla de retorno; `migration-verify.lua` ya trataba ese caso. | Reaplicar el cambio de mailcow y conservar la URL. Probar con stubs de `dovecot` y `ssl.https` en `lua5.3` dentro de la imagen (fallo de red: `INTERNAL_FAILURE`; 500 y 401: `MISMATCH`; 200: `OK`) y con `make e2e-mail`. |
| `dovecot/conf/conf.d/fts.conf` | nombres | Comentarios: mailcow.conf pasa a .env. | Reaplicar la linea. |
| `dovecot/conf/dovecot.conf` | postgres, sogo-php, migracion | Los diccionarios de cuota y sieve son pgsql, no mysql; sin ejemplo LDAP ni SOGo; el passwd-verify.lua renderizado vive fuera del bind mount (/etc/dovecot-auth); incluye doveadm-api.conf generado; ademas declara el diccionario `sieve_vacation` y la ranura `sieve_after3` de la respuesta automatica (`mail.v_sieve_vacation`); y una passdb Lua antes de las demas (`migration-verify.lua`, la credencial de destino por trabajo de migracion) con `cache_key=%s:%u:%w:%{session}` | Reaplicar el cambio de mailcow y volver a poner las cinco lineas propias. Conservar el diccionario, `sieve_after3` y la passdb de migracion, que debe ir la primera. |
| `dovecot/conf/global_sieve_after` | nombres | Comentario: la UI de mailcow pasa a mail.sieve_filters. | Reaplicar la linea. |
| `dovecot/conf/global_sieve_before` | nombres | Comentario: la UI de mailcow pasa a mail.sieve_filters. | Reaplicar la linea. |
| `dovecot/docker-entrypoint.sh` | postgres, sogo-php, servicios-go, nombres, migracion | Genera el userdb y los dicts de Dovecot contra PostgreSQL; quita SOGo (IP de confianza, SSO, credenciales de cron); renderiza passwd-verify.lua y migration-verify.lua; conserva el usuario maestro del webmail bajo @platform.local; genera la API HTTP de doveadm con TLS. Es de los ficheros que mas cuesta portar; ademas genera `dovecot-dict-sql-sieve_vacation.conf` (la respuesta automatica) y, solo con `DOVECOT_MIGRATION_MASTER_USER` y `DOVECOT_MIGRATION_MASTER_PASS`, un segundo usuario maestro propio de la migracion (su linea en `dovecot-master.passwd` con su `allow_nets` y en `dovecot-master.userdb`); y da permiso de ejecucion a `maildir_reconcile.sh` junto al resto de guiones | Aplicar los cambios de mailcow bloque a bloque, no el fichero entero, y comprobar con make e2e-mail. Conservar el bloque `sieve_vacation`, el del maestro de la migracion, el renderizado de `migration-verify.lua` y `maildir_reconcile.sh` en la lista del `chmod +x`. |
| `dovecot/quota_notify.py` | nombres | El host de Redis sale de MAIL_REDIS_HOST. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/repl_health.sh` | nombres | REDISPASS y MAILCOW_REPLICA_IP pasan a MAIL_REDIS_PASSWORD y MAIL_REPLICA_IP. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/rspamd-pipe-ham` | nombres | El host de Rspamd es rspamd.mail-engines, no <proyecto>_mailcow-network. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/rspamd-pipe-spam` | nombres | El host de Rspamd es rspamd.mail-engines, no <proyecto>_mailcow-network. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/sa-rules.sh` | nombres | Nombres del contenedor y de la red de los motores. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/supervisord.conf` | cron | Anade crond a supervisord: las tareas que lanzaba ofelia desde fuera del contenedor las ejecuta crond dentro (ver dovecot/crontab). El fichero de mailcow trae fin de linea CRLF y el nuestro LF. | Reaplicar el cambio de mailcow ignorando el fin de linea (diff --strip-trailing-cr) y conservar el bloque crond. |
| `dovecot/syslog-ng-redis_slave.conf` | nombres | REDISPASS pasa a MAIL_REDIS_PASSWORD. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/syslog-ng.conf` | nombres | Host y contrasena de Redis por variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/trim_logs.sh` | nombres, sogo-php | Variables MAIL_REDIS_* y sin el recorte del registro de SOGo. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `netfilter/Dockerfile` | etiqueta, endurecimiento | Etiqueta del mantenedor. Sin `pip` en la imagen final: `pip` trae `msgpack` 1.1.2 embebido, con una vulnerabilidad alta que no se puede corregir desde aqui hasta que salga un `pip` nuevo, y el motor no lo usa al ejecutarse. | Reaplicar el cambio de mailcow y conservar `pip3 uninstall -y pip`. |
| `netfilter/main.py` | sogo-php, nombres | Quita las expresiones de SOGo (8 y 9), renombra la cadena y la red (br-mail) y usa MAIL_REDIS_PASSWORD. El fichero de mailcow trae fin de linea CRLF y el nuestro LF. | Reaplicar el cambio de mailcow ignorando el fin de linea (diff --strip-trailing-cr). Nunca ejecutar netfilter en la maquina de desarrollo. |
| `olefy/Dockerfile` | etiqueta, endurecimiento | Etiqueta del mantenedor; sube `setuptools` a 78.1.1 o mas (CVE-2025-47273). Sin `pip` en la imagen final: `pip` trae `msgpack` 1.1.2 embebido, con una vulnerabilidad alta que no se puede corregir desde aqui hasta que salga un `pip` nuevo, y el motor no lo usa al ejecutarse. | Reaplicar el cambio de mailcow y conservar la subida de `setuptools` y la desinstalacion de `pip`. Ojo: instala `oletools` desde la rama principal de su repositorio, sin fijar (herencia de mailcow). |
| `postfix-tlspol/Dockerfile` | etiqueta, arranque | La etiqueta del mantenedor y `NOTEST=1` en el `ENV` del builder: `scripts/build.sh` de postfix-tlspol ejecuta antes de compilar unas pruebas que resuelven DNS reales (DANE) y que fallan segun la red desde la que se construye (en GitHub Actions, `dane_test.go: expected exactly 4 concurrent address lookups, got 5`); el propio postfix-tlspol las salta en su CI con esa variable. Sin ella, la imagen no se puede construir de forma reproducible. Hasta el 2026-09-21 ademas subia `golang.org/x/net` a v0.56.0 y regeneraba `vendor/` antes de compilar, porque la v1.8.22 fijaba v0.47.0 (cinco altas) y este servicio descarga politicas MTA-STS de dominios ajenos; la v1.11.0 que fija mailcow ya trae `x/net` v0.56.0 en su `go.mod`, asi que ese parche propio se quito y el fichero vuelve a ser el de mailcow salvo la etiqueta (Trivy: 0 criticas y 0 altas con arreglo). | Reaplicar las dos lineas (etiqueta y `NOTEST=1`). Si mailcow sube `VERSION`, escanear la imagen (`ops/security/escanear-motores.sh escanear <carpeta> postfix-tlspol`) y, solo si `x/net` vuelve a salir con altas, reponer `go get golang.org/x/net@<corregida> && go mod tidy && go mod vendor` antes de `scripts/build.sh build-only`. |
| `postfix-tlspol/postfix-tlspol.sh` | nombres | Espera de DNS contra letsencrypt.org en lugar de mailcow.email y variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix-tlspol/syslog-ng-redis_slave.conf` | nombres | REDISPASS y nombre del contenedor. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix-tlspol/syslog-ng.conf` | nombres | Host y contrasena de Redis por variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/Dockerfile` | etiqueta, postgres, cola | Etiqueta del mantenedor, postgresql-client y postfix-pgsql en lugar de mariadb-client y postfix-mysql; una etapa que compila `queue-agent` con Go, copia el binario a `/usr/local/sbin` y expone el puerto 8590. | Reaplicar las lineas y conservar la etapa `queue-agent` y su `COPY --from`. |
| `postfix/conf/anonymize_headers.pcre` | nombres | El agente de recepcion se llama Core Force Mail, no Postcow. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/conf/main.cf.base` | postgres, nombres, endurecimiento | Todos los mapas son proxy:pgsql: en lugar de proxy:mysql:; `mail_name` es Core Force Mail y el comentario de los BCC apunta a mail-policy. Ademas (commit fa468d7) `lmtp_host_lookup = native` y reintentos de cola de 1 minuto (`minimal_backoff_time`, `maximal_backoff_time`, `queue_run_delay`) para que Postfix no rebote el correo mientras Dovecot esta parado. Es la plantilla de main.cf (en mailcow es main.cf); postfix.sh genera el main.cf real en cada arranque. `smtp_tls_policy_maps` consulta a postfix-tlspol con `QUERYwithTLSRPT` (mailcow 0f06c8e5, portado el 2026-09-21): Postfix 3.10 de trixie lleva libtlsrpt y analiza los atributos `policy_type`, `policy_domain`, `mx_host_pattern` y `policy_string` de la respuesta; tlspol lo acepta desde la v1.8.22, asi que el orden de despliegue entre Postfix y tlspol no importa. | Reaplicar el cambio de mailcow en main.cf sobre esta plantilla y volver a poner los mapas pgsql, `mail_name`, `lmtp_host_lookup` y los tres tiempos de cola. Validar con `postconf -c` sobre la plantilla renderizada dentro de la imagen y con `make e2e-mail`. |
| `postfix/conf/master.cf` | endurecimiento, sogo-php | El envio interno de la plataforma no tiene la excepcion de SOGo (allow_mailcow_local): el webmail se autentica como buzon*maestro y Postfix le aplica smtpd_sender_login_maps. | Reaplicar el cambio de mailcow y conservar las restricciones de remitente. |
| `postfix/docker-entrypoint.sh` | arranque | La comprobacion de TLS antiguo lee main.cf.base, no main.cf, porque el main.cf real se genera despues. | Reaplicar la linea. |
| `postfix/postfix.sh` | postgres, nombres, sogo-php | Genera los 18 mapas pgsql_*.cf contra el esquema mail en lugar de los de MySQL, sin BCC dinamicos ni SOGo, y con los nombres propios. Es el fichero que mas cuesta portar. | Aplicar los cambios de mailcow bloque a bloque, no el fichero entero, y comprobar los 18 mapas con postmap -q (make e2e-mail). |
| `postfix/stop-supervisor.sh` | cola | El oyente de supervisord detiene el contenedor cuando sale cualquier proceso, salvo el agente de la cola (`queue-agent`): es un auxiliar con superficie de red y corre como root, y su caida (falta de memoria, un fallo) no puede llevarse a Postfix. Ademas lee de verdad el evento (encabezado y carga) y responde al protocolo; un evento ilegible cuenta como critico. | Reaplicar el cambio de mailcow y conservar la lista de procesos no criticos. Se prueba con `ops/scaffold/test-stop-supervisor.sh` (en `check-queue-agent.sh`) y con el supervisord real de la imagen. |
| `postfix/supervisord.conf` | cola | Un programa mas, `queue-agent`, que arranca el agente de la cola. | Reaplicar el cambio de mailcow y conservar el bloque `[program:queue-agent]` antes del `eventlistener`. |
| `postfix/syslog-ng-redis_slave.conf` | nombres | REDISPASS y nombre del contenedor. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/syslog-ng.conf` | nombres | Host y contrasena de Redis por variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/whitelist_forwardinghosts.sh` | servicios-go | Consulta a mail-policy (http://mail-policy:8081/forwardinghosts) y no a nginx con un .php. | Reaplicar la linea. |
| `redis/redis-conf.sh` | nombres | REDISPASS y REDISMASTERPASS pasan a MAIL_REDIS_PASSWORD y MAIL_REDIS_MASTER_PASSWORD. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `rspamd/Dockerfile` | etiqueta, arranque | Etiqueta del mantenedor y la copia de `controller-password.sh` a la imagen. | Reaplicar las dos lineas. |
| `rspamd/docker-entrypoint.sh` | servicios-go, nombres, endurecimiento | Espera a mail-policy (8081 y 9081) en lugar de a phpfpm; el mapa de redes internas es platform_networks.map. Las contrasenas del controller no las deja un fichero escrito a mano: las genera `controller-password.sh` en cada arranque desde el almacen de secretos. | Reaplicar el cambio de mailcow y conservar las esperas. |
| `rspamd/local.d/antivirus.conf` | endurecimiento | max_size y timeout del modulo antivirus atados al tamano maximo de Postfix. | Conservar los dos valores; check-mail-size-limits.sh los vigila. |
| `rspamd/local.d/composites.conf` | sogo-php | Sin la regla SOGO_CONTACT_EXCLUDE. | No reintroducir la regla. |
| `rspamd/local.d/force_actions.conf` | endurecimiento | Regla VIRUS_SCAN_FAILED: si el antivirus no pudo analizar un mensaje, soft reject; nunca se entrega sin veredicto. | Conservar la regla; check-mail-size-limits.sh la exige. |
| `rspamd/local.d/greylist.conf` | servicios-go | La lista de hosts de reenvio la sirve mail-policy. | Reaplicar la linea. |
| `rspamd/local.d/groups.conf` | sogo-php, nombres | Sin el simbolo SOGO_CONTACT. | No reintroducir el simbolo. |
| `rspamd/local.d/metadata_exporter.conf` | servicios-go, sogo-php | Los mapas /pipe y /pipe_rl los sirve mail-policy; sin PUSHOVERMAIL ni el selector mailcow_rcpt. | Reaplicar el cambio de mailcow y conservar las dos URLs. |
| `rspamd/local.d/options.inc` | nombres, endurecimiento | local_addrs usa platform_networks.map y max_message fija el mayor mensaje que Rspamd analiza, atado a Postfix. | Conservar las dos lineas; check-mail-size-limits.sh vigila max_message. |
| `rspamd/local.d/ratelimit.conf` | nombres | Comentario: los limites por buzon se dan por Redis (RL_VALUE), no por la UI de mailcow. | Reaplicar la linea. |
| `rspamd/lua/rspamd.local.lua` | servicios-go | Las URL de aliasexp, bcc y footer las sirve mail-policy, sin .php. | Reaplicar el cambio de mailcow y conservar las URL. |
| `rspamd/settings.conf` | servicios-go | El mapa settings lo sirve mail-policy. | Reaplicar la linea. |
| `unbound/Dockerfile` | etiqueta | Etiqueta del mantenedor. La version minima de `unbound` desde `edge` (`>=1.26.1-r0`) es de mailcow (a480a96e, portada el 2026-09-21). | Reaplicar la linea; si mailcow sube la version minima, reaplicarla y comprobar que la imagen construye y arranca con el `unbound.conf` montado (`unbound-control status`, `dnssec-failed.org` debe dar SERVFAIL). |
| `watchdog/Dockerfile` | etiqueta, sogo-php | Sin cliente de MariaDB ni comprobaciones de MySQL. | Reaplicar el cambio de mailcow y volver a quitar MySQL. |
| `watchdog/watchdog.sh` | sogo-php, nombres | Quita las comprobaciones de nginx, MySQL, replicacion MySQL, PHP-FPM, SOGo y external_checks, y cambia el canario de DNS. Es de los ficheros que mas cuesta portar. | Aplicar los cambios de mailcow bloque a bloque y no reintroducir las comprobaciones quitadas. |

## 6. Ficheros identicos a mailcow

86 ficheros, listados con su huella en el manifiesto. Cambiar uno de ellos sin pasarlo a modificado
y explicarlo en la seccion 5 rompe `make checks`: es el aviso de que la divergencia esta creciendo.

## 7. Ficheros nuevos y propios

| Fichero | Categorias | Por que existe | Como se mantiene |
|---|---|---|---|
| `dovecot/crontab` | cron | Las tareas periodicas de Dovecot, que en mailcow lanza ofelia desde fuera del contenedor; aqui las ejecuta crond dentro (ver dovecot/supervisord.conf). Ademas lanza `maildir_reconcile.sh` cada 5 minutos (solo `MASTER=y`) y envuelve `maildir_gc.sh` en el mismo cerrojo (`_garbage/.reconcile.lock`) para que la purga y el traslado a `_garbage` no se crucen. | No tiene equivalente: se mantiene a mano y hay que revisar si mailcow cambia las tareas de ofelia. Conservar la linea de `maildir_reconcile.sh` y el `flock` de `maildir_gc.sh`. |
| `dovecot/maildir_reconcile.sh` | cron, endurecimiento | Retira del volumen el maildir de un buzon borrado (README, "Maildir de un buzon borrado"). mailcow lo hace en el momento del borrado desde su PHP por dockerapi (`maildir cleanup`: mueve `/var/vmail/<dominio>/<local>` a `_garbage/<epoch>_<dominio>_<local>`); aqui ningun servicio Go recibe el socket de docker ni se amplia dockerapi ni el API de doveadm (README, "Gestor de la cola"), asi que lo hace este guion dentro del contenedor, con la conexion de solo lectura del userdb: atiende las marcas de baja de `mail.mailbox_deletions` (que consume) y concilia los maildir y dominios sin fila, con la misma convencion de nombres en `_garbage` para que `maildir_gc.sh` los purgue igual. Lista el disco antes de consultar la base, gracia por mtime, fail-closed ante cualquier fallo o base vacia, tope por pasada, sin seguir enlaces, con cerrojo. | No tiene equivalente. Se prueba con `ops/scaffold/check-maildir-reconcile.sh` (arbol y psql falsos, mutaciones) y con `make e2e-mail` contra Dovecot real. Si mailcow cambia la convencion de `_garbage` en dockerapi, reflejarla aqui y en `maildir_gc.sh`. |
| `rspamd/controller-password.sh` | endurecimiento | Genera `override.d/worker-controller-password.inc` en cada arranque desde `RSPAMD_CONTROLLER_PASSWORD` (lectura) y `RSPAMD_CONTROLLER_ENABLE_PASSWORD` (escritura), solo con hashes de `rspamadm pw`. mailcow tiene una sola contrasena, `enable_password`, que pone su panel PHP por dockerapi; aqui se separan porque Rspamd 4.1.4 deja que la contrasena de lectura autorice tambien las ordenes de escritura si falta `enable_password` (comprobado): sin contrasena de escritura se pone una aleatoria y descartada. La ruta `worker_password` de dockerapi sigue igual que en mailcow, y lo que escriba dura hasta el siguiente arranque. | No tiene equivalente. Se prueba con `ops/scaffold/check-rspamd-controller-password.sh` (rspamadm falso) y con `make e2e-mail` contra Rspamd real (la lectura lee y no aprende). |
| `dovecot/conf/auth/migration-verify.lua` | migracion | Passdb Lua de la credencial de destino por trabajo de migracion (docs/adr/0002): solo actua sobre contrasenas `cfmj1.` por IMAP y pregunta a `mail-auth` con `service = migration`; el entrypoint la renderiza con envsubst junto a `passwd-verify.lua`. | No tiene equivalente en mailcow (mailcow no expone migracion por trabajo). Va antes de `passwd-verify.lua` en `dovecot.conf` con una clave de cache que lleva `%{session}`; no se toca al reaplicar cambios de mailcow. |

| `postfix/queue-agent/go.mod`, `postfix/queue-agent/harden.go`, `postfix/queue-agent/hardening_test.go`, `postfix/queue-agent/main.go`, `postfix/queue-agent/priority_linux.go`, `postfix/queue-agent/priority_other.go`, `postfix/queue-agent/protect_linux.go`, `postfix/queue-agent/protect_other.go`, `postfix/queue-agent/queue.go`, `postfix/queue-agent/server.go`, `postfix/queue-agent/queue_test.go`, `postfix/queue-agent/server_test.go` | cola | El gestor de cola: mailcow lo hace con dockerapi (`postqueue` y `postsuper` por el socket de docker, sin autenticacion en la red de los motores), lo que daria a cualquier servicio que lo alcance una ejecucion de ordenes en cualquier contenedor. Este agente es un binario de Go con biblioteca estandar que corre dentro del contenedor de Postfix, atiende por HTTPS con `QUEUE_AGENT_API_KEY` y solo admite listar, reintentar, retener, liberar y borrar por un identificador de cola validado, y vaciar la cola diferida; nunca devuelve el contenido de un mensaje. | No tiene equivalente. Se prueba con `ops/scaffold/check-queue-agent.sh` (unitarias y mutaciones) y con `make e2e-mail` contra Postfix real. |

| `migration-runner/Dockerfile`, `migration-runner/api.go`, `migration-runner/api_test.go`, `migration-runner/classify.go`, `migration-runner/config.go`, `migration-runner/config_test.go`, `migration-runner/go.mod`, `migration-runner/hardening_test.go`, `migration-runner/imapsync.go`, `migration-runner/job_credential_test.go`, `migration-runner/main.go`, `migration-runner/parse.go`, `migration-runner/parse_test.go`, `migration-runner/proc_linux.go`, `migration-runner/proc_other.go`, `migration-runner/proc_unix.go`, `migration-runner/protect_linux.go`, `migration-runner/protect_other.go`, `migration-runner/runner.go`, `migration-runner/runner_test.go`, `migration-runner/scan.go`, `migration-runner/scan_test.go`, `migration-runner/ssrf.go`, `migration-runner/ssrf_test.go`, `migration-runner/testdata/imapsync-auth.txt`, `migration-runner/testdata/imapsync-cuota.txt`, `migration-runner/testdata/imapsync-inaccesible.txt`, `migration-runner/testdata/imapsync-ok-repaso.txt`, `migration-runner/testdata/imapsync-ok.txt`, `migration-runner/testdata/imapsync-starttls-nombre.txt`, `migration-runner/testdata/imapsync-tls-ca.txt`, `migration-runner/testdata/imapsync-tls-nombre.txt`, `migration-runner/testdata/imapsync-virus.txt` | migracion | El ejecutor de la migracion de buzones (`docs/adr/0002`): mailcow lanza imapsync desde su panel PHP y su contenedor de Dovecot; aqui imapsync corre en un contenedor propio, aislado, con un envoltorio de Go de biblioteca estandar que reclama trabajos a `mail-migration` por HTTP, valida el origen (rechaza direcciones privadas y conecta a la IP ya resuelta y validada, verificando el certificado contra el nombre pedido), pasa las contrasenas por ficheros 0400 en un tmpfs efimero, filtra cada mensaje por ClamAV (`--pipemess`, falla cerrado) y devuelve solo contadores. `testdata/` son salidas reales de imapsync 2.314 contra dos Dovecot desechables. | No tiene equivalente. Se prueba con `ops/scaffold/check-migration-runner.sh` (unitarias, con un imapsync falso) y con `make e2e-mail` (imapsync real contra el Dovecot de la prueba). |

Propios: `README.md` (contratos de los motores), `docker-compose.mail.yml`, `docker-compose.mail.images.yml`,
`docker-compose.e2e.yml`, este libro y `upstream-manifest.tsv`.

## 8. Lo que mailcow tiene y no copiamos

Por diseno (`CLAUDE.md`: nada de PHP, MySQL ni SOGo). La lista completa, con el motivo de cada una, esta
en `ops/upstream/mapa.tsv` (lineas `omitido`). Resumen: SOGo, PHP-FPM, nginx, MySQL, LDAP, la copia de
seguridad de mailcow, imapsync (que vuelve como servicio propio, seccion 7), los mapas dinamicos y el exportador de metadatos en PHP, y las plantillas de
restablecimiento de contrasena y de cuarentena. El informe semanal las muestra como informativas, sin
contarlas como pendientes.

## 9. Parches de seguridad de los motores

De donde sale cada motor decide como llega un parche:

| Motor | Origen de la version | Como llega un parche |
|---|---|---|
| Rspamd | Paquete Debian fijado en `rspamd/Dockerfile` (`RSPAMD_VER`, hoy 4.1.4) | Subir `RSPAMD_VER` |
| ClamAV | Se compila desde fuente en `clamav/Dockerfile` (1.4.6) | Subir la version del constructor |
| Postfix | Paquete de Debian trixie, sin fijar | Reconstruir la imagen |
| Dovecot y Pigeonhole | Paquete de Alpine 3.21, sin fijar (`gosu` 1.19 se compila en el Dockerfile con Go 1.26) | Reconstruir la imagen. Cambiar de Alpine puede cambiar la version mayor de Dovecot: no se hace sin probar |
| Unbound | Repositorio `edge` de Alpine, para recibir parches, con version minima fijada (`unbound>=1.26.1-r0`, hoy 1.26.1) | Reconstruir la imagen; subir la version minima cuando lo haga mailcow o un aviso lo exija |
| postfix-tlspol | Se compila la etiqueta `v1.11.0` de su repositorio con `golang:1.26-trixie` (su `go.mod` fija `golang.org/x/net` v0.56.0) sobre `debian:trixie-slim` (syslog-ng 4.8) | Subir la etiqueta y escanear la imagen; si `x/net` sale con altas, subirla antes de compilar (seccion 5) |
| imapsync (ejecutor de migracion) | Paquete `imapsync` de Alpine 3.23 (community), serie 2.314 fijada en `migration-runner/Dockerfile` (`imapsync~=2.314`; el build falla si resulta otra version). No esta en los repositorios de Debian ni de Ubuntu; no se descarga nada mas de Internet en el build. La licencia es la NO LIMIT PUBLIC LICENSE (`docs/adr/0002`) | Reconstruir la imagen recoge los parches del paquete y de Perl; subir de serie es una decision con `make e2e-mail` en verde (la salida de imapsync es lo que lee el ejecutor) |
| Resto (netfilter, dockerapi, acme, watchdog, olefy) | Paquetes de la base | Reconstruir la imagen |

Plazos comprometidos (confirmados el 2026-09-23; `docs/Operacion_Despliegue.md`, seccion 12): parche critico
en produccion en 72 horas desde el aviso y parche importante en 14 dias. La incidencia mensual de
`imagenes-motores.yml` escribe la fecha limite en su primera linea y se asigna al responsable de
mantenimiento.

Fuentes de aviso que debe leer quien opere el despliegue: anuncios de seguridad de Postfix, Dovecot,
Rspamd, ClamAV y Unbound; los avisos de Debian y Alpine para las bases; y el informe semanal de esta
herramienta, que marca los commits de mailcow con palabras de seguridad.

Para recoger parches del sistema aunque no cambie el codigo, las imagenes se reconstruyen una vez al mes
(`scripts/deploy-mail.sh`, de un motor a la vez). Ese despliegue lo hace quien opera el servidor.

Cada mes `.github/workflows/imagenes-motores.yml` construye las imagenes y las escanea con Trivy
(`ops/security/escanear-motores.sh`): solo gravedad critica o alta y solo con version corregida publicada. Mantiene
una incidencia abierta mientras haya algo que corregir. No se usa Dependabot para las imagenes de los motores: sus
etiquetas son de version mayor de Alpine o Debian, y subir una (por ejemplo la de Dovecot) puede cambiar la version
mayor del motor; ese cambio se decide con el informe de mailcow y `make e2e-mail`, no con una propuesta automatica.

## 10. Revision trimestral

Cada trimestre se anota, en una linea al final de esta seccion, el numero de ficheros modificados, cuantos
son sustantivos, cuantos dias costo el ultimo port y cuantos conflictos hubo. Un conflicto es un fichero en el
que el cambio de mailcow no se puede reaplicar linea a linea porque choca con logica propia y hay que
reescribirlo.

Umbral, fijado el 2026-09-21 con el primer port real (siete commits de mailcow en menos de un dia y sin
conflictos): si un port cuesta mas de 3 dias de trabajo, o deja 3 o mas conflictos, o los ficheros sustantivos
pasan de 45, se decide por escrito (ADR en `docs/adr/`) entre seguir igual, reducir divergencia o cambiar de
motor. El umbral se revisa con cada fila nueva.

| Fecha | Modificados | Sustantivos | Dias del ultimo port | Conflictos |
|---|---|---|---|---|
| 2026-09-20 | 62 | 35 | sin port aun | sin port aun |
| 2026-09-21 | 64 | 36 | menos de uno: una sesion de unas dos horas para los 7 commits de `02552ffefdf0` a `ca07d8d33318` (incidencia #13): 7 ficheros tocados (`postfix-tlspol/Dockerfile` y sus dos `syslog-ng*.conf`, `unbound/Dockerfile`, `postfix/conf/postscreen_access.cidr`, `postfix/conf/main.cf.base`, `dovecot/conf/auth/passwd-verify.lua`), 4 imagenes reconstruidas y escaneadas (postfix-tlspol, unbound, postfix, dovecot), humo de cada motor y `make checks`; la divergencia bajo en un fichero (el parche propio de `x/net` en tlspol ya no hace falta) | 0 |
