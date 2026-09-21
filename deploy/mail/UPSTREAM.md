# Libro de parches de deploy/mail frente a mailcow-dockerized

Estado: V (medido el 2026-09-20 contra el commit base). Lo mantiene `ops/upstream/upstream.sh` y lo vigila
`ops/scaffold/check-upstream-ledger.sh`: un fichero nuevo o cambiado sin explicar aqui rompe `make checks`.

Los motores de `deploy/mail/` se copiaron una vez de mailcow-dockerized (`CLAUDE.md`: no se vuelve a copiar
ningun fichero). Cada version nueva de mailcow se porta a mano. Este libro convierte esa tarea en una lista:
dice que ficheros son nuestros, por que difieren y como se rehace el cambio.

## 1. Base

| Dato | Valor |
|---|---|
| Commit base | `02552ffefdf0` (2026-08-18, merge de la PR 7427) |
| Version de mailcow mas reciente que lo contiene | `2026-07b` |
| Commits de mailcow posteriores a la base, el 2026-09-20 | 0: la base es la punta de su rama principal |
| Manifiesto legible por maquina | `deploy/mail/upstream-manifest.tsv` |
| Mapa entre nuestras carpetas y las de mailcow | `ops/upstream/mapa.tsv` |

## 2. Cuanto divergimos

| Estado | Ficheros | Que significa |
|---|---|---|
| Identico a mailcow | 88 | Sin cambios: portar una version es reaplicar el mismo cambio |
| Modificado | 62 | Tiene equivalente en mailcow y nosotros lo cambiamos (secciones 5 y 6) |
| Nuevo | 1 | Sin equivalente en mailcow (seccion 7) |
| Propio | 6 | Nuestro por definicion: compose, README y este libro |
| Total seguido por git | 157 | |

De los 62 modificados, 31 solo cambian una etiqueta o un nombre (categorias `etiqueta` y `nombres`): son
mecanicos. Los otros 31 cambian comportamiento (PostgreSQL, quitar SOGo y PHP, servicios Go, cron o
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
| `dockerapi/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `dockerapi/docker-entrypoint.sh` | nombres | Organizacion del certificado autofirmado: mailcow pasa a platform. | Reaplicar la linea. |
| `dockerapi/main.py` | nombres | REDISPASS y el host de Redis pasan a MAIL_REDIS_PASSWORD y MAIL_REDIS_HOST. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dockerapi/modules/DockerApi.py` | sogo-php | Quita las tareas de MySQL y de SOGo (mysql_upgrade, sogo rename_user...). | Reaplicar los cambios de mailcow en las demas tareas; no reintroducir las quitadas. |
| `dovecot/Dockerfile` | etiqueta, postgres, sogo-php | Sin grupo sogo, imapsync ni Perl; drivers de PostgreSQL (lua-sql-postgres) en lugar de los de MySQL y MariaDB. | Reaplicar el cambio de mailcow y volver a quitar SOGo, Perl e imapsync y a cambiar los drivers. |
| `dovecot/clean_q_aged.sh` | postgres, nombres | Poda la cuarentena con psql sobre mail.quarantine; hoy no hace nada porque esa tabla no existe: la poda la hace mail-security. | Reaplicar el cambio de mailcow y volver a poner la consulta propia. |
| `dovecot/conf/auth/passwd-verify.lua` | servicios-go | La URL de autenticacion es ${MAIL_AUTH_URL} (mail-auth), que el entrypoint renderiza con envsubst. | Reaplicar el cambio de mailcow y conservar la URL. |
| `dovecot/conf/conf.d/fts.conf` | nombres | Comentarios: mailcow.conf pasa a .env. | Reaplicar la linea. |
| `dovecot/conf/dovecot.conf` | postgres, sogo-php | Los diccionarios de cuota y sieve son pgsql, no mysql; sin ejemplo LDAP ni SOGo; el passwd-verify.lua renderizado vive fuera del bind mount (/etc/dovecot-auth); incluye doveadm-api.conf generado. | Reaplicar el cambio de mailcow y volver a poner las cinco lineas propias. |
| `dovecot/conf/global_sieve_after` | nombres | Comentario: la UI de mailcow pasa a mail.sieve_filters. | Reaplicar la linea. |
| `dovecot/conf/global_sieve_before` | nombres | Comentario: la UI de mailcow pasa a mail.sieve_filters. | Reaplicar la linea. |
| `dovecot/docker-entrypoint.sh` | postgres, sogo-php, servicios-go, nombres | Genera el userdb y los dicts de Dovecot contra PostgreSQL; quita SOGo (IP de confianza, SSO, credenciales de cron); renderiza passwd-verify.lua; conserva el usuario maestro del webmail bajo @platform.local; genera la API HTTP de doveadm con TLS. Es de los ficheros que mas cuesta portar. | Aplicar los cambios de mailcow bloque a bloque, no el fichero entero, y comprobar con make e2e-mail. |
| `dovecot/quota_notify.py` | nombres | El host de Redis sale de MAIL_REDIS_HOST. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/repl_health.sh` | nombres | REDISPASS y MAILCOW_REPLICA_IP pasan a MAIL_REDIS_PASSWORD y MAIL_REPLICA_IP. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/rspamd-pipe-ham` | nombres | El host de Rspamd es rspamd.mail-engines, no <proyecto>_mailcow-network. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/rspamd-pipe-spam` | nombres | El host de Rspamd es rspamd.mail-engines, no <proyecto>_mailcow-network. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/sa-rules.sh` | nombres | Nombres del contenedor y de la red de los motores. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/supervisord.conf` | cron | Anade crond a supervisord: las tareas que lanzaba ofelia desde fuera del contenedor las ejecuta crond dentro (ver dovecot/crontab). El fichero de mailcow trae fin de linea CRLF y el nuestro LF. | Reaplicar el cambio de mailcow ignorando el fin de linea (diff --strip-trailing-cr) y conservar el bloque crond. |
| `dovecot/syslog-ng-redis_slave.conf` | nombres | REDISPASS pasa a MAIL_REDIS_PASSWORD. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/syslog-ng.conf` | nombres | Host y contrasena de Redis por variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `dovecot/trim_logs.sh` | nombres, sogo-php | Variables MAIL_REDIS_* y sin el recorte del registro de SOGo. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `netfilter/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `netfilter/main.py` | sogo-php, nombres | Quita las expresiones de SOGo (8 y 9), renombra la cadena y la red (br-mail) y usa MAIL_REDIS_PASSWORD. El fichero de mailcow trae fin de linea CRLF y el nuestro LF. | Reaplicar el cambio de mailcow ignorando el fin de linea (diff --strip-trailing-cr). Nunca ejecutar netfilter en la maquina de desarrollo. |
| `olefy/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `postfix-tlspol/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `postfix-tlspol/postfix-tlspol.sh` | nombres | Espera de DNS contra letsencrypt.org en lugar de mailcow.email y variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix-tlspol/syslog-ng-redis_slave.conf` | nombres | REDISPASS y nombre del contenedor. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix-tlspol/syslog-ng.conf` | nombres | Host y contrasena de Redis por variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/Dockerfile` | etiqueta, postgres | Etiqueta del mantenedor, postgresql-client y postfix-pgsql en lugar de mariadb-client y postfix-mysql. | Reaplicar las lineas. |
| `postfix/conf/anonymize_headers.pcre` | nombres | El agente de recepcion se llama Core Force Mail, no Postcow. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/conf/main.cf.base` | postgres | Todos los mapas son proxy:pgsql: en lugar de proxy:mysql:. Es la plantilla de main.cf (en mailcow es main.cf); postfix.sh genera el main.cf real en cada arranque. | Reaplicar el cambio de mailcow en main.cf sobre esta plantilla y volver a poner los mapas pgsql. |
| `postfix/conf/master.cf` | endurecimiento, sogo-php | El envio interno de la plataforma no tiene la excepcion de SOGo (allow_mailcow_local): el webmail se autentica como buzon*maestro y Postfix le aplica smtpd_sender_login_maps. | Reaplicar el cambio de mailcow y conservar las restricciones de remitente. |
| `postfix/docker-entrypoint.sh` | arranque | La comprobacion de TLS antiguo lee main.cf.base, no main.cf, porque el main.cf real se genera despues. | Reaplicar la linea. |
| `postfix/postfix.sh` | postgres, nombres, sogo-php | Genera los 18 mapas pgsql_*.cf contra el esquema mail en lugar de los de MySQL, sin BCC dinamicos ni SOGo, y con los nombres propios. Es el fichero que mas cuesta portar. | Aplicar los cambios de mailcow bloque a bloque, no el fichero entero, y comprobar los 18 mapas con postmap -q (make e2e-mail). |
| `postfix/syslog-ng-redis_slave.conf` | nombres | REDISPASS y nombre del contenedor. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/syslog-ng.conf` | nombres | Host y contrasena de Redis por variables MAIL_REDIS_*. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `postfix/whitelist_forwardinghosts.sh` | servicios-go | Consulta a mail-policy (http://mail-policy:8081/forwardinghosts) y no a nginx con un .php. | Reaplicar la linea. |
| `redis/redis-conf.sh` | nombres | REDISPASS y REDISMASTERPASS pasan a MAIL_REDIS_PASSWORD y MAIL_REDIS_MASTER_PASSWORD. | Reaplicar los cambios de mailcow y volver a sustituir las variables por su nombre propio. |
| `rspamd/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `rspamd/docker-entrypoint.sh` | servicios-go, nombres | Espera a mail-policy (8081 y 9081) en lugar de a phpfpm; el mapa de redes internas es platform_networks.map. | Reaplicar el cambio de mailcow y conservar las esperas. |
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
| `unbound/Dockerfile` | etiqueta | Etiqueta del mantenedor. | Reaplicar la linea. |
| `watchdog/Dockerfile` | etiqueta, sogo-php | Sin cliente de MariaDB ni comprobaciones de MySQL. | Reaplicar el cambio de mailcow y volver a quitar MySQL. |
| `watchdog/watchdog.sh` | sogo-php, nombres | Quita las comprobaciones de nginx, MySQL, replicacion MySQL, PHP-FPM, SOGo y external_checks, y cambia el canario de DNS. Es de los ficheros que mas cuesta portar. | Aplicar los cambios de mailcow bloque a bloque y no reintroducir las comprobaciones quitadas. |

## 6. Ficheros identicos a mailcow

88 ficheros, listados con su huella en el manifiesto. Cambiar uno de ellos sin pasarlo a modificado
y explicarlo en la seccion 5 rompe `make checks`: es el aviso de que la divergencia esta creciendo.

## 7. Ficheros nuevos y propios

| Fichero | Categorias | Por que existe | Como se mantiene |
|---|---|---|---|
| `dovecot/crontab` | cron | Las tareas periodicas de Dovecot, que en mailcow lanza ofelia desde fuera del contenedor; aqui las ejecuta crond dentro (ver dovecot/supervisord.conf). | No tiene equivalente: se mantiene a mano y hay que revisar si mailcow cambia las tareas de ofelia. |

Propios: `README.md` (contratos de los motores), `docker-compose.mail.yml`, `docker-compose.mail.images.yml`,
`docker-compose.e2e.yml`, este libro y `upstream-manifest.tsv`.

## 8. Lo que mailcow tiene y no copiamos

Por diseno (`CLAUDE.md`: nada de PHP, MySQL ni SOGo). La lista completa, con el motivo de cada una, esta
en `ops/upstream/mapa.tsv` (lineas `omitido`). Resumen: SOGo, PHP-FPM, nginx, MySQL, LDAP, la copia de
seguridad de mailcow, imapsync, los mapas dinamicos y el exportador de metadatos en PHP, y las plantillas de
restablecimiento de contrasena y de cuarentena. El informe semanal las muestra como informativas, sin
contarlas como pendientes.

## 9. Parches de seguridad de los motores

De donde sale cada motor decide como llega un parche:

| Motor | Origen de la version | Como llega un parche |
|---|---|---|
| Rspamd | Paquete Debian fijado en `rspamd/Dockerfile` (`RSPAMD_VER`, hoy 4.1.4) | Subir `RSPAMD_VER` |
| ClamAV | Se compila desde fuente en `clamav/Dockerfile` (1.4.6) | Subir la version del constructor |
| Postfix | Paquete de Debian trixie, sin fijar | Reconstruir la imagen |
| Dovecot y Pigeonhole | Paquete de Alpine 3.21, sin fijar | Reconstruir la imagen. Cambiar de Alpine puede cambiar la version mayor de Dovecot: no se hace sin probar |
| Unbound | Repositorio `edge` de Alpine, para recibir parches | Reconstruir la imagen |
| postfix-tlspol | Se compila la etiqueta `v1.8.22` de su repositorio | Subir la etiqueta |
| Resto (netfilter, dockerapi, acme, watchdog, olefy) | Paquetes de la base | Reconstruir la imagen |

Objetivos (propuestos en `docs/Plan_Estrategico_Mejoras_Correo.md`, a confirmar con el primer parche real):
parche critico en produccion en 72 horas desde el aviso y parche importante en 14 dias.

Fuentes de aviso que debe leer quien opere el despliegue: anuncios de seguridad de Postfix, Dovecot,
Rspamd, ClamAV y Unbound; los avisos de Debian y Alpine para las bases; y el informe semanal de esta
herramienta, que marca los commits de mailcow con palabras de seguridad.

Para recoger parches del sistema aunque no cambie el codigo, las imagenes se reconstruyen una vez al mes
(`scripts/deploy-mail.sh`, de un motor a la vez). Ese despliegue lo hace quien opera el servidor.

## 10. Revision trimestral

Cada trimestre se anota, en una linea al final de esta seccion, el numero de ficheros modificados, cuantos
son sustantivos, cuantos dias costo el ultimo port y cuantos conflictos hubo. Tras el primer port real se fija el
umbral a partir del cual hay que decidir por escrito entre seguir igual, reducir divergencia o cambiar de
motor. Hasta entonces no hay umbral: no se ha medido ningun port.

| Fecha | Modificados | Sustantivos | Dias del ultimo port | Conflictos |
|---|---|---|---|---|
| 2026-09-20 | 62 | 31 | sin port aun | sin port aun |
