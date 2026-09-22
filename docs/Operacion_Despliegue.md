# Operación y despliegue

Reglas de operación heredadas del ERP de referencia, ya como documento propio. Las razones
largas de cada guardarraíl están en `ops/scaffold/README.md`, `ops/security/secrets/README.md`,
`ops/backup/README.md` y `docs/arquitectura/OBSERVABILIDAD.md`.

## 1. Entornos

* Desarrollo: `make dev` levanta Postgres (perfil `embedded-db`), PgBouncer, Redis, NATS y
  el plano de control con `docker-compose.yml`. Con `ENVIRONMENT=development` (el valor de
  `.env.example`) PgBouncer va sin TLS al Postgres del compose, que no tiene certificado, y
  si no hay `pgbouncer/userlist.txt` arma uno efímero en `/tmp` del contenedor con el rol de
  plataforma. Fuera de `development` y `test` exige `verify-full` y el `userlist.txt` que
  genera `ops/db/pgbouncer-userlist.sh`, y sin ellos no arranca
  (`ops/scaffold/check-pgbouncer-entrypoint.sh`, sección 9 de `validate.sh`). Lo que sea de
  una sola máquina, como remapear puertos que ya usan otros proyectos, va en un
  `docker-compose.override.yml`, que no se versiona.
* Motores de correo en desarrollo: se levantan aparte con `deploy/mail/docker-compose.mail.yml`
  cuando se trabaje en la fase 2. En un puesto de trabajo nunca `netfilter-mail` (corre
  privilegiado en la red del host y reescribe su cortafuegos), ni `acme-mail`, `watchdog-mail`
  o `dockerapi-mail`: `up -d --no-deps` con la lista de motores, porque `redis-mail` depende de
  `netfilter-mail` y un `up` sin más lo arrancaría. Los puertos de correo, solo en `127.0.0.1`.
  `scripts/deploy-mail.sh` se niega a levantar esos cuatro (etiqueta `core-force-mail.solo-servidor`)
  si el docker del destino es el de la propia máquina.
  `main.cf` de Postfix lo genera `postfix/postfix.sh` en cada arranque desde `main.cf.base`,
  que es el versionado; `webmail` exige `WEBMAIL_MASTER_USER=<DOVECOT_MASTER_USER>@platform.local`.
* Producción (AWS): una cuenta por ambiente (dev, staging, prod). RDS PostgreSQL Multi-AZ
  detrás de PgBouncer, ElastiCache, SES, S3. Servidor de aplicación endurecido con
  `ops/server-template/bootstrap.sh`. PgBouncer verifica RDS con
  `pgbouncer/rds-global-bundle.pem` (bundle público de AWS, versionado). Los secretos, en
  cualquier perfil, nunca en Secrets Manager: `ops/security/secrets` (sección 2).
* Producción autoalojada: un solo servidor propio, sin servicios gestionados, con el perfil
  `DEPLOY_PROFILE=selfhosted` (sección 11). Mismos controles que en AWS, dados por
  contenedores: Postgres y Redis con TLS de una CA interna y un proxy de borde con HTTPS.
* Redis en tránsito (`pkg/config/redis.go`). El Redis de la plataforma (`REDIS_*`: cupos
  del gateway, freno de `mail-auth`, sesiones del webmail, tasa de `reputation`, caché de
  `access-control`) va cifrado: `REDIS_TLS=true`, con verificación del certificado siempre
  activa y TLS 1.2 como mínimo. Solo un `ENVIRONMENT` declarado `development` o `test`
  admite Redis en claro; en cualquier otro (también `staging` o sin declarar) esos servicios
  no arrancan sin TLS (`REDIS_TLS=true is required`). ElastiCache debe crearse con cifrado
  en tránsito (y `AUTH`, la contraseña va en `REDIS_PASSWORD` del almacén); su certificado
  lo firma una CA pública que ya está en las raíces de las imágenes. `REDIS_TLS_CA_FILE`
  (PEM que se suma a las raíces del sistema, montado en el contenedor) es para un Redis
  propio con CA interna, y `REDIS_TLS_SERVER_NAME` para cuando el certificado no lleva el
  nombre de `REDIS_HOST`. Un valor de `REDIS_TLS` que no es booleano, o una CA o un nombre
  con TLS apagado, detienen el arranque. El Redis de los motores (`MAIL_REDIS_*`) tiene
  las mismas variables, opcionales y apagadas: ver `deploy/mail/README.md`, Contrato Redis.
* Variables numéricas y de duración (límites, trabajadores, puertos, intervalos, tasas): se
  leen con `config.EnvInt`, `config.EnvDuration` y `config.EnvFloat` (`pkg/config/env.go`),
  con el rango de cada una junto a ella en `.env.example`. Vacía vale su valor por defecto;
  ilegible o fuera de su rango, el servicio no arranca y el error nombra la variable y el
  rango. `EnvFloat` rechaza además NaN e infinito: una tasa infinita dejaba sin límite el
  envío a SES (`SES_MAX_SEND_RATE`, `SES_MAX_SEND_RATE_MARKETING`, de 1 a 10000 por segundo). Así leen
  `pkg/config` (`POSTGRES_PORT`, `POSTGRES_DIRECT_PORT`, `REDIS_PORT`, `JWT_*_TTL`) y
  scheduler, campaigns, transactional, mail-auth, contacts, suppression, automations,
  webmail, gateway, audit, identity, access-control, templates, analytics, billing,
  reputation, organization, mail-directory, domain-service y mail-security. Los umbrales, la
  ventana y los límites de reputation siguen siendo
  obligatorios y sin valor por defecto: se exige la variable y después se lee con su rango
  (`EnvFloat` para los umbrales). El techo de `DOMAIN_RECHECK_INTERVAL` (6 h) es el que
  cuenta la alerta `BarridoDeDominiosSinCelda`, y el de `MAIL_DKIM_RECONCILE_INTERVAL` de
  mail-security (15 min), el que cuentan `RepasoDKIMDetenido` y `RepasoDKIMSinOrganization`
  (`docs/arquitectura/OBSERVABILIDAD.md`).
* Direcciones internas de otros servicios (`<SERVICIO>_HOST` y `<SERVICIO>_HOST_PORT`): el
  gateway las lee para cada servicio de `services/gateway/routes.json`, y organization para
  identity, access-control y mail-directory cuando falta su `<SERVICIO>_URL`, con
  `config.UpstreamURL` (`pkg/config/upstream.go`). Vacías valen `default_host` y
  `default_port` de la tabla (en organization, los nombres y puertos de compose). El gateway
  las resuelve todas al cargar la tabla, las pida ya una ruta o no. Un puerto ilegible o fuera
  de 1 a 65535 (`config.MaxPort`), un `default_port` que no lo cumpla o un host con espacios,
  con delimitadores de URL o con dos puntos sin ser una IPv6 (que va sin corchetes) impiden
  arrancar, y el error nombra la variable o el servicio: antes salían como 502 en la primera
  petición. Las entradas `celda=host:puerto` de `<SERVICIO>_CELL_HOSTS` siguen la misma
  regla (`tenantcell.ParseInstances`), y también los hosts que mail-security no usa en una URL
  http, `MAIL_REDIS_HOST` y `MAIL_QUARANTINE_REINJECT_HOST` (`config.EnvHost`).
* URLs base internas de otros servicios (`<SERVICIO>_URL`): se leen al arrancar, antes de
  conectar a nada, con `config.ServiceURL` o `config.RequiredServiceURL`
  (`pkg/config/upstream.go`). Regla: `http` o `https` absoluta, `scheme://host[:puerto]`, con el
  host de la regla anterior (una IPv6, entre corchetes), puerto opcional de 1 a 65535 y como
  mucho la barra de la raíz, que se quita; sin usuario, ruta, consulta ni fragmento. Ningún
  cliente la necesita: cada uno le pega su ruta absoluta (`/internal/...`), así que una ruta o
  una consulta desviarían la llamada y unas credenciales viajarían por la red interna. Mal
  formada, el servicio no arranca y el error nombra la variable. Cada una conserva si es
  obligatoria u opcional. Obligatorias: `ORGANIZATION_URL` (servicios de celda y
  domain-service, `tenantcell.OrganizationURLFromEnv`), `SUPPRESSION_URL` de contacts y de
  transactional (sin suppression transactional no encola ningún envío: toda exclusión se
  respeta antes de encolar), `CONTACTS_URL`, `TRANSACTIONAL_URL` y `TEMPLATES_URL` de campaigns
  y automations, `BILLING_URL` de reputation, `MAIL_DIRECTORY_URL` del webmail y
  `MAIL_DIRECTORY_URL` y `MAIL_SECURITY_URL` de domain-service (sus destinos base). Opcionales:
  `TEMPLATES_URL` y `REPUTATION_URL` de transactional, `TRANSACTIONAL_MAIL_URL` de identity y
  `TRANSACTIONAL_URL` de mail-security (vacías, lo que depende de ellas no funciona y se
  registra; en mail-security, el aviso de cuarentena); `ACCESS_CONTROL_URL`, por
  defecto `http://access-control:8002` (`authz.CheckerFromEnv`); y en organization
  `ACCESS_CONTROL_URL`, `IDENTITY_URL` y `MAIL_DIRECTORY_URL`, que sin ellas usa
  `<SERVICIO>_HOST`. No son URLs base internas y conservan su lector: `MAIL_AUTH_URL` (el
  endpoint https que llaman tal cual Dovecot y el webmail), `PUBLIC_BASE_URL`,
  `PASSWORD_BREACH_API_URL`, `MINIO_PUBLIC_URL` y `NATS_URL`. Las dos direcciones de los motores
  que mail-security llama como URL base siguen la misma regla con su valor por defecto:
  `RSPAMD_CONTROLLER_URL` (`http://rspamd:11334`; le pega `/learnspam` y manda la contraseña
  en su cabecera) y `DOVEADM_API_URL` (`https://dovecot:8443`), que además tiene que ser
  `https` (`doveadm.New`): la clave del API viaja en cada petición.
* Entorno declarado (`ENVIRONMENT`). Un servidor declara exactamente
  `ENVIRONMENT=production` o `ENVIRONMENT=staging` en el `.env` de `DEPLOY_PATH`, el que
  Compose pasa a los contenedores; también el servidor de la cuenta de dev. `development` y
  `test` son solo locales (`.env.example` trae `development` para `make dev`; `make e2e` lo
  exporta). `ops/server-template/bootstrap.sh` no aprovisiona sin uno de los dos en
  `server.env` y lo escribe en ese `.env`. `ops/security/secrets/require-server-environment.sh`,
  que `with-secrets.sh` ejecuta antes de materializar ningún secreto, se niega a desplegar si
  la última asignación de `ENVIRONMENT` del `.env` no es exactamente una de las dos (con
  espacios, un comentario o un CR al final tampoco: el servicio recibiría otro texto). Tras
  eso rechaza también un `.env` que conserve el marcador `YOUR_DOMAIN` de `.env.example`: nada
  lo sustituye y acabaría en los registros DNS que se indican a cada empresa (MX, SPF, DMARC),
  en los enlaces y en los orígenes permitidos; y avisa sin bloquear de los `CHANGE_ME`, que
  el resolvedor usaría como credencial si el almacén no respondiera. Solo nombra claves. Todo
  `docker compose` del servidor va por `with-secrets.sh` (`check-secret-sources.sh`), así que
  es el punto único de `release.yml` y de `scripts/deploy-ecr.sh`. Una sola regla decide qué
  se relaja según el valor, `config.DeclaredDevelopmentOrTest` (`pkg/config/config.go`):
  solo `development` o `test` declarados (sin distinguir mayúsculas ni espacios en los
  extremos) relajan algo; sin declarar, `production`, `staging` o cualquier otro valor es
  estricto. Ningún código compara `ENVIRONMENT` por su cuenta. Lo que relaja:

  | Con `development` o `test` | Dónde |
  |---|---|
  | Redis de la plataforma en claro, sin `REDIS_TLS=true` | `pkg/config/redis.go` |
  | Servicios de celda con la credencial de plataforma, sin `CELL_DB_PASSWORD` | `pkg/config/config.go` |
  | identity firma con un par efímero, sin `JWT_SIGNING_KEY` | `pkg/config/config.go`, `pkg/auth` |
  | Sin `INTERNAL_GATEWAY_TOKEN` arrancan el gateway, domain-service, organization, webmail, mail-directory, mail-security y todo servicio que comprueba permisos con `authz.CheckerFromEnv`, y `RequireGatewayToken` deja pasar sin comprobar; en cualquier otro entorno ninguno de ellos arranca y el middleware responde 503 a todo | `middleware.InternalGatewayToken` (`pkg/middleware/middleware.go`), `services/gateway/main.go`, `services/domain-service/main.go`, `services/organization/main.go`, `services/webmail/main.go`, `tenantcell.MembershipFromEnv` (`pkg/tenantcell/membership.go`) en mail-directory y mail-security, y `authz.CheckerFromEnv` (`pkg/authz/authz.go`) |
  | webmail con `WEBMAIL_TLS_INSECURE_SKIP_VERIFY=true` o `WEBMAIL_IMAP_TLS=none`, y adjuntos sin ClamAV con `WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS=true` | `services/webmail/main.go` |

  Además, en un servidor `INTERNAL_GATEWAY_TOKEN` es obligatorio en el almacén
  (`secret-keys.txt`) y sin él `fetch-secrets.sh` detiene el despliegue. `make e2e` comprueba
  que con `staging` y sin `ENVIRONMENT` el gateway, domain-service y organization no arrancan
  sin token.
  `AUTH_COOKIE_SECURE` no depende de `ENVIRONMENT` (`docs/arquitectura/CSP-Y-SESION.md`).

## 2. Secretos

Ninguna credencial vive en el repositorio ni en el `.env` del servidor: la fuente es un
almacén cifrado local (`store.json.gpg`, gpg simétrico, `docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`),
nunca un servicio administrado de AWS. `ops/security/secrets/fetch-secrets.sh` los
materializa en `/dev/shm/core-force-mail/secrets.env` (tmpfs, 0600, todo o nada) y
`with-secrets.sh` envuelve cualquier `docker compose` que cree contenedores. La lista
canónica es `ops/security/secrets/secret-keys.txt` (más `secret-keys-db.txt` para las
credenciales de base); añadir una variable ahí es parte de introducir el secreto. CI:
`make check-secrets`, `make check-secret-sources`, `make check-secret-scope` y
`make check-secrets-store`.

**Reparto por contenedor (mínimo privilegio, `docs/adr/0007-minimo-privilegio-en-secretos.md`).** Ningún
contenedor recibe el fichero de secretos entero: el `environment:` de cada bloque de `docker-compose.yml` declara uno a
uno los suyos como `NOMBRE: ${NOMBRE:-}` (Compose los interpola contra el entorno que deja `with-secrets.sh`) y no hay
`env_file` de `secrets.env`. Quién recibe qué lo dice `ops/security/secrets/reparto.tsv` (contenedor, secreto,
evidencia de dónde lo lee), y `make check-secret-scope`, dentro de `make checks` y de CI, ata la tabla al compose, al
código y a `secret-keys.txt`: falla si un contenedor recibe o lee un secreto que su fila no lista, si una fila concede
algo que no se entrega o que el código no lee, o si un secreto no tiene destinatario. Todo servicio Go recibe
`INTERNAL_GATEWAY_TOKEN`; `identity` es el único con `JWT_SIGNING_KEY`, `audit` con `AUDIT_HASH_KEY*`, `domain-service` y
`mail-migration` con `MAIL_ENCRYPTION_KEY*`, `mail-security` y `transactional` con `MAIL_LINK_SIGNING_KEY`, y `webmail`
con el maestro de Dovecot del webmail (tabla completa en la ADR). Límite: las variables de entorno son legibles con el
socket de Docker o como root del host; esto acota lo que ve un servicio comprometido, no un atacante con el servidor.

* **Secreto nuevo.** Variable en `secret-keys.txt` (con `?` si es opcional), valor en el almacén con `add-secret.sh`,
  una fila en `reparto.tsv` por contenedor que la lea y la línea `NOMBRE: ${NOMBRE:-}` en el `environment:` de cada uno.
  Sin cualquiera de las tres partes, `make check-secret-scope` falla. Servicio nuevo: `make new-service` imprime su
  bloque con solo el token interno y su fila.
* **Rotar uno.** Publicar el valor y recrear **solo** los contenedores de su fila en `reparto.tsv`.
* **Desplegar este reparto (una vez).** El cambio solo toca compose y `ops/`: `deploy-ecr.sh` sin argumentos lo trata como
  "solo ficheros" y no recrea nada, y los contenedores en marcha siguen con el fichero entero hasta que se recrean.
  1. `ops/security/secrets/with-secrets.sh ops/security/secrets/verify-scope.sh entorno`: el valor que Compose interpola
     coincide con el del fichero materializado (un secreto con espacios, comillas, `$` o `#` llegaría cambiado; antes lo
     entregaba `env_file` sin pasar por el shell). Si nombra una variable, cambiar su valor en el almacén.
  2. Recrear los 22 servicios Go (`ops/scaffold/service-paths.sh --class go`) con `scripts/deploy-ecr.sh <servicios>`
     explícitos, que pasa por la guardia de retroceso y `esperar-sanos.sh`. Cada fila está completa por sí sola, así que un
     estado intermedio es válido; `gateway` e `identity` al final. Redis y los motores no se recrean: su entorno no cambia.
  3. `ops/security/secrets/verify-scope.sh contenedores` (solo `docker`, imprime nombres, nunca valores): cada servicio
     debe salir `OK`; `recibe de mas` es un contenedor sin recrear y `le faltan` un obligatorio vacío.
* **Migrar un servidor que hoy no tiene almacén (el `.env` lleva todas las credenciales) sin cortar el
  servicio.** Es el caso real: mientras la cuenta de AWS del proyecto no existió, `fetch-secrets.sh` fallaba
  siempre y `load.sh` seguía con las credenciales del `.env`, que `env_file` reparte enteras a los 22
  servicios (`docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`). Orden, todo desde
  `/opt/core-force-mail/app` en el servidor:
  1. `ops/security/secrets/init-store.sh`. Genera la frase en `/opt/core-force-mail/secrets/passphrase`
     (0600) si no existe y crea el almacén vacío. **Copiar la frase a un gestor de contraseñas del equipo
     antes de seguir** (no dentro de un respaldo cifrado con esa misma frase): sin ella el almacén es
     irrecuperable.
  2. `ops/security/secrets/push-secrets.sh .env` (sin `--apply`): lista qué claves subiría y confirma que
     trae las 41 obligatorias (`secret-keys.txt` + `secret-keys-db.txt`) antes de tocar nada.
  3. `ops/security/secrets/push-secrets.sh .env --apply`: sube, relee el almacén y compara valor por
     valor contra lo enviado, y solo entonces reescribe el `.env` sin esas claves (copia de rescate
     `.env.pre-secrets.<fecha>`, 0600). Los contenedores en marcha no se tocan todavía: `env_file` ya no
     les da nada nuevo hasta que se recreen, pero tampoco pierden lo que ya tienen en su entorno actual.
  4. Verificar antes de recrear nada: `ops/security/secrets/with-secrets.sh ops/security/secrets/verify-scope.sh entorno`
     debe decir que coincide.
  5. Recrear los 22 servicios Go, igual que en "Desplegar este reparto" arriba (`scripts/deploy-ecr.sh` con la
     lista de `ops/scaffold/service-paths.sh --class go`, `gateway` e `identity` al final). Antes de esto,
     `docker inspect` de cualquiera de ellos mostraba el `.env` entero; después, solo lo de su fila.
  6. `ops/security/secrets/verify-scope.sh contenedores`: todos `OK`. Si alguno da `recibe de mas`, no se ha
     recreado; si da `le faltan`, revisar que `push-secrets.sh` subió esa clave (paso 2 la habría avisado
     como obligatoria faltante).
  7. Solo con todos `OK`: `shred -u .env.pre-secrets.*`.

  No hace falta parar la plataforma en ningún paso: cada servicio recreado arranca con lo suyo completo
  (fila íntegra en `reparto.tsv`), así que convivir un rato con unos servicios ya recreados y otros
  todavía con el `.env` viejo en su entorno es un estado válido, igual que en un despliegue normal de
  este reparto.

Credencial de la celda: la contraseña del rol `<CELL_DB_NAME>_svc` de la celda que sirve
el despliegue va al almacén como `CELL_DB_PASSWORD` (obligatoria: sin ella el despliegue se
detiene antes de recrear nada). La reciben solo `mail-directory`, `mail-security` y
`mail-auth`, que en producción no arrancan sin ella; `CELL_DB_USER` es configuración
opcional y por defecto ese nombre. Alta y rotación, seguidas: publicar el valor con
`add-secret.sh` (al menos 32 caracteres ASCII imprimibles), correr
`with-secrets.sh ops/db/cell-service-role.sh --cell <code>`, poner la entrada del rol en
`userlist.txt` de PgBouncer y recrear los tres servicios. Entre el script y la recreación,
las conexiones abiertas siguen vivas pero las nuevas con la contraseña retirada fallan.
Detalle en `docs/Modelo_de_Datos_y_Celdas.md` 5.1.

Secretos del respaldo (credencial del bucket externo y frase de cifrado):
`ops/security/secrets/secret-keys-backup.txt`. Ningún contenedor los recibe; los leen solo los
trabajos de `ops/backup`, del entorno o de `BACKUP_SECRETS_FILE`
(`/opt/core-force-mail/env/backup.env`, 0600 del usuario de despliegue). En el `.env` no van: lo
recibe entero cada servicio, y con esas dos claves un servicio comprometido lee y borra el
histórico de respaldos. `make check-secrets` los vigila igual que los demás.

Credenciales de base de datos, todas: viven en `ops/security/secrets/secret-keys-db.txt`, no
en `secret-keys.txt`. Se materializan en `/dev/shm/core-force-mail/secrets-db.env`, que
**ningún contenedor recibe por `env_file`**: cada una llega solo al servicio al que su bloque
de `docker-compose.yml` se la pasa por `environment:`, y Compose la interpola contra el
entorno que deja `with-secrets.sh`. Quién puede recibir qué lo declara
`ops/db/service-credentials.json` (planos `registry`, `tenant`, `cell`, `none`) y lo comprueba
`make check-db-credentials`, que forma parte de `make checks`. Un servicio nuevo que no declare
su plano hace fallar CI: nacer sin credencial propia es heredar la de plataforma.

`userlist.txt` de PgBouncer lo genera `ops/db/pgbouncer-userlist.sh --write` (y `--check`
falla si se quedó atrás) con los roles de este despliegue. Es el paso que se olvidaba: un rol
que Postgres acepta pero que no está en `userlist.txt` no pasa el pooler, y el error apunta a
la base y no al pooler. Tras regenerarlo, recrear el contenedor `pgbouncer`. El fichero va con
modo 0640 y grupo 70 (el uid del pooler en su imagen): `bootstrap.sh` mete al usuario de
despliegue en ese grupo (lo crea como `pgbouncer-pool` si no existe); sin eso el script no puede
asignarlo y el pooler no arranca ("no se puede leer /etc/pgbouncer/userlist.txt").

Motores de una celda: su rol es `<CELL_DB_NAME>_engine` (`ops/db/cell-engine-role.sh --cell
<code>`), miembro del grupo `mail_engine`, con `MAIL_DB_PASSWORD` del almacén y CONNECT solo a
su base. Orden, en dos fases, porque los motores en marcha siguen conectados como el rol
compartido: (1) el script, `userlist.txt`, `MAIL_DB_USER=<CELL_DB_NAME>_engine` en el `.env` de
los motores y recrearlos; (2) cuando **ninguna** celda del clúster use ya `mail_engine`,
`cell-engine-role.sh --cell <code> --retire-shared`, que le quita LOGIN y CONNECT. Hacer (2)
antes de (1) corta la entrada a la base y la celda deja de recibir correo hasta que los
motores arranquen con el rol nuevo.

Servicios del plano de empresa: dos credenciales, el rol de enrutado (`mail_router`,
compartido, solo lee `organization.v_tenant_routing`) y una por servicio
(`mail_svc_<esquema>`). Orden: la migración canónica de empresa que crea el grupo y sus
permisos (organization la barre sola) **antes** que
`ops/db/tenant-service-role.sh --router | --service <svc>`, que falla si el grupo no existe;
después `userlist.txt` y recrear el servicio. El reparto va servicio a servicio: el que aún no
tenga su contraseña sigue con la de plataforma y lo avisa al arrancar. `docs/Modelo_de_Datos_y_Celdas.md` 5.2.

Clave de firma del token de acceso (`docs/arquitectura/CSP-Y-SESION.md`, Firma del access
token): `JWT_SIGNING_KEY` va al almacén (obligatoria) y la recibe solo identity;
`JWT_SIGNING_KID` y `JWT_PUBLIC_KEYS` son configuración del `.env`. `JWT_SECRET` ya no la usa
nadie: `fetch-secrets.sh` deja de materializarla y se borra del almacén tras el despliegue.
Alta y rotación, en este orden:

1. `ops/security/jwt-keygen.sh`, en el servidor: imprime el `kid` nuevo y su entrada pública
   y deja la privada en `/dev/shm/core-force-mail/jwt-signing-key.<kid>` (0600). Nunca la
   imprime ni la escribe en el repositorio.
2. Añadir la entrada pública a `JWT_PUBLIC_KEYS` del `.env`, junto a la vigente, y recrear el
   gateway: acepta las dos.
3. `add-secret.sh JWT_SIGNING_KEY --desde-env <fichero> --apply`, `shred -u <fichero>`,
   `JWT_SIGNING_KID=<kid>` en el `.env` y recrear identity. Si el `kid` no está publicado con
   esa misma clave, identity no arranca.
4. Pasados `JWT_ACCESS_TTL` y los 5 minutos del reto MFA y del step-up, quitar la entrada
   anterior de `JWT_PUBLIC_KEYS` y recrear el gateway e identity.

El primer paso a EdDSA hace los pasos 1 a 3 antes del despliegue que trae el código (sin
`JWT_SIGNING_KEY` en el almacén, `fetch-secrets.sh` aborta antes de recrear nada) y recrea
todos los servicios en ese mismo despliegue: sus imágenes anteriores exigen `JWT_SECRET` y ya
no la reciben. Los tokens HS256 vivos reciben 401 y el cliente renueva una vez, sin volver a
iniciar sesión. Si la clave se compromete, los pasos 2 y 4 van juntos (se publica la nueva y
se retira la anterior en el mismo paso, recreando gateway e identity a la vez): todos los
access tokens caen de golpe y se renuevan con el refresh.

Llave de la cadena de hash de auditoría (`docs/adr/0006-cadena-de-auditoria-con-hmac-y-anclas.md`):
`AUDIT_HASH_KEY` (64 hex) y `AUDIT_HASH_KEYS_OLD` (retiradas, separadas por coma) son **opcionales** en
`secret-keys.txt`: **sin ellas nada cambia**, `audit` sigue escribiendo el hash sin clave (versión 1), no encadena los
eventos de seguridad y lo avisa en el arranque (`audit: sin AUDIT_HASH_KEY`). Las recibe solo `audit`; los demás
servicios no las reciben (`reparto.tsv`, `make check-secret-scope`). Orden de alta:

1. Migraciones `07`, `08` y `09` de `audit` en cada base de empresa **antes** del código (`bash
   ops/apply-all-canonical.sh`, idempotentes) y `ops/maintenance/pgbouncer-reconnect.sh` después.
2. Desplegar el código sin la llave: ya se anclan las cabezas de las cadenas (`AUDIT_ANCHOR_INTERVAL`, 15 minutos por
   defecto, en el `.env`).
3. Generar la llave en el servidor, sin imprimirla ni dejarla en el historial:
   `VALOR="$(openssl rand -hex 32)" ops/security/secrets/add-secret.sh AUDIT_HASH_KEY --apply`. Debe ser **distinta** de
   `MAIL_ENCRYPTION_KEY`. Recrear solo `audit` con `with-secrets.sh docker compose up -d --no-deps audit`; si hay varias
   réplicas, todas a la vez (una sin llave escribiría versión 1 tras filas de versión 2 y el verificador lo da por
   regresión).
4. Comprobar `GET /api/v1/audit/integrity`: `ok: true` y `versions` con las dos versiones. El arranque registra el
   identificador de la llave (`key_id`), que no es la llave.
5. Copiar la llave al respaldo de secretos. Perderla deja las filas de versión 2 sin verificar (`hash_key_missing`).
   Una vez puesta **no se quita**: el verificador da como rotura toda fila sin clave posterior a una firmada.

Rotación: `ops/security/secrets/rotate-key.sh AUDIT_HASH_KEY AUDIT_HASH_KEYS_OLD --apply` pone la nueva como activa y
pasa la anterior a la lista, y se recrea `audit`. A diferencia de `MAIL_ENCRYPTION_KEYS_OLD`, la lista **no se vacía**
mientras haya filas firmadas con esas llaves: una fila append-only no se re-firma. Causas del verificador y qué hacer
con cada una: ADR 0006, sección 5.

Ancla externa (ADR 0006, sección 8): la cabeza anclada vive en la base, en el stream `AUDIT_CHAIN` y en el log del
mismo servidor, y solo protege contra quien no pueda escribir también `audit.chain_anchors`. La copia fuera es un correo:

1. **`AUDIT_ANCHOR_RUA`** en el `.env`: una o varias direcciones separadas por coma, **fuera de la plataforma** (un buzón
   en otro proveedor, que archive lo que recibe). Vacía (por defecto) no cambia nada. Con ella puesta, `audit` exige
   `TRANSACTIONAL_URL` y `PLATFORM_TENANT_ID` (los mismos que usa `identity` para el correo del sistema) y no arranca sin
   ellos. `AUDIT_ANCHOR_REPORT_INTERVAL` (24h por defecto, de 1h a 7d) fija el ritmo; el envío se alinea al reloj UTC
   (con 24h, a las 00:00), así que un despliegue ni lo repite ni lo salta. Poner la llave (`AUDIT_HASH_KEY`) **antes**:
   sin ella el informe sale con `signature: none` y no prueba nada.
2. Recrear `audit` y comprobar en el log `audit: proximo informe de anclas`. Cada informe lleva, por empresa y cadena,
   id y slug de la empresa, la posición y el hash de la cabeza, la versión del hash y el instante del ancla, firmados con
   HMAC-SHA256 y la llave activa de la cadena; nunca contenido de apuntes ni datos personales. Asunto:
   `[Core Force Mail] Anclas de auditoria <fecha>`, y `: cadena rota` cuando lo dispara una verificación que encontró
   una rotura (sale en el acto, como mucho una vez por hora y empresa).
3. **Conservar los correos fuera del servidor**: son la única referencia que un atacante con la base no puede tocar.
   Vigilar `audit_anchor_reports_total{result}` (`failed`: transactional o SES no responden; `rejected`: falta
   `PLATFORM_FROM_EMAIL` o la dirección no vale; `suppressed`: la dirección rebotó o se quejó y `suppression` la bloquea)
   y la alerta `AnclaDeAuditoriaSinEnviar` (más de dos intervalos sin envío).

**Cotejo ante una sospecha de compromiso.** Desde un equipo que **no** sea el servidor, con la llave del respaldo de
secretos exportada en el entorno como `AUDIT_HASH_KEY` (y `AUDIT_HASH_KEYS_OLD` si el correo es anterior a una
rotación), sin escribirla en ningún fichero ni en la línea de órdenes:

```
ops/security/verificar-ancla.sh ultimo-informe.eml
ops/security/verificar-ancla.sh ultimo-informe.eml \
    --dsn 'postgres://<usuario>:<clave>@<host>:<puerto>/mail_tenant_<slug>' --tenant <slug>
```

La primera forma comprueba solo la firma (acepta el `.eml` completo o el texto pegado). La segunda coteja cada ancla
del correo con la cadena actual de esa empresa: **cualquier `seq` de la cabeza menor que el del correo, o un hash
distinto en ese `seq`, es evidencia de manipulación** (`head_behind_anchor`: se borraron las últimas filas;
`anchor_mismatch`: se reescribió o se borró la fila y se siguió escribiendo), aunque `GET /audit/integrity` diga que todo
está bien, porque el API solo conoce las anclas que quedan en la base. Un `AVISO` de que `chain_anchors` ya no conserva
el ancla es que alguien la borró desde la base. Salida 0: auténtico y contenido en la cadena; 1: evidencia; 2: no se
pudo comprobar (llave ausente o retirada sin `AUDIT_HASH_KEYS_OLD`, base inaccesible). No reiniciar `audit` ni tocar la
base antes de guardar el resultado y el correo con el que se cotejó. Con varias empresas en el informe, `--tenant` elige
cuál; la DSN es la de la base de esa empresa con un rol que lea `audit.*` (la del almacén de secretos).

Verificación en segundo plano y eventos por consumidores durables (`docs/adr/0006-cadena-de-auditoria-con-hmac-y-anclas.md`,
secciones 6 y 7). Orden de despliegue:

1. **Migración `10` (`audit.integrity_runs`) en cada base de empresa antes del código** (`bash ops/apply-all-canonical.sh`,
   idempotente) y `ops/maintenance/pgbouncer-reconnect.sh` después. Sin ella, `POST /audit/integrity/runs` y el `GET
   /audit/integrity` de una cadena grande dan 500; lo demás no cambia.
2. **Recrear `audit`** (nunca con las dos versiones a la vez). En el primer arranque crea un consumidor durable por subject
   de `AUDIT_SUBJECTS` con `DeliverNew` (solo lo publicado desde ese momento: el durable no reproduce lo que los streams ya
   retenían, que la suscripción anterior guardó con otro id y saldría duplicado) y declara los streams (`GATEWAY` y `ACCESS`
   nuevos; `IDENTITY`, `ORGANIZATION`, `SCHEDULER`, `DOMAINS` y `MIGRATION` ganan sus subjects). Lo publicado en el hueco
   entre el `audit` viejo y el nuevo se pierde esa única vez; desde entonces, lo publicado con `audit` caído se aplica al
   volver. Con NATS caído en el arranque el servicio atiende igual y reintenta atarse cada 5 s.
3. Variables nuevas (todas opcionales, en `.env`): `AUDIT_INTEGRITY_INLINE_MAX_ROWS` (200000: hasta que tamaño de cadena
   contesta `GET /audit/integrity` dentro de la petición; más allá responde 202 y lanza una verificación en segundo plano),
   `AUDIT_INTEGRITY_RUN_TIMEOUT` (12h, de 1m a 72h), `AUDIT_INTEGRITY_FULL_EVERY` (7d, de 1h a 90d: cada cuanto el barrido
   hace una completa en vez de incremental) y `AUDIT_INTEGRITY_SWEEP_INTERVAL` (**vacío = sin barrido**; de 1h a 7d; una
   empresa a la vez, hasta 30 min por empresa y pasada, en una sola réplica). Con el barrido encendido la primera pasada
   espera 5 minutos tras el arranque.
4. Vigilar: `audit_integrity_runs_total{origin,outcome}`, `audit_integrity_sweep_broken_tenants`,
   `audit_events_discarded_total` y las alertas del grupo `auditoria` (`CadenaDeAuditoriaRota`,
   `VerificacionDeCadenaSinTerminar`, `EventosDeAuditoriaDescartados`), además de las de `EVENTS_DLQ` (los consumidores se
   llaman `audit-<subject>`, por ejemplo `audit-identity-all`). Ante `CadenaDeAuditoriaRota`: `GET
   /api/v1/audit/integrity/runs` de la empresa da el motivo y la posición; no reiniciar `audit` ni tocar la base.
5. Una verificación en curso cuando `audit` se reinicia no se pierde: queda en curso con su punto y se retoma a los 90 s desde ahí (por
   quien la consulte, quien lance otra o el barrido). Se cancela con `POST /audit/integrity/runs/{id}/cancel`.

## 3. Arranque de una plataforma vacía

`ops/db/bootstrap-platform.sh` crea la celda inicial, la empresa `platform` y su primer
`superadmin` (contraseña solo por `PLATFORM_ADMIN_PASSWORD`, nunca por argumento; hash
bcrypt hecho por Postgres). Idempotente. La base `mail_tenant_platform` no la crea el script:
organization la crea y la migra con las mismas piezas que la saga de alta (marca, cierre a
`PUBLIC` y registro de migraciones), al arrancar y en cada pasada de
`ORGANIZATION_SAGA_SWEEP_INTERVAL` hasta dejarla lista. Es la única empresa que nace fuera de
la saga, y el barrido de migraciones solo migra bases que ya existen: sin esto quedaba activa y
sin base, y los servicios que recorren las empresas la reintentaban en bucle. No crea la base
que le falte a otra empresa: una base borrada por accidente no debe reaparecer vacía. Es lo único que no se puede hacer por API, porque
para llamar a la API hace falta ya un superadmin. Después, todo por API: `POST /cells`,
`POST /organizations`.

## 4. Migraciones

* Registro: las aplica `organization` al arrancar (`RegistryMigrator`, idempotente).
* Empresa: se aplican al crear la empresa y en un barrido de fondo (`RUN_TENANT_MIGRATIONS`)
  por conexión directa con advisory lock; `POST /api/v1/organizations/migrate` para forzar.
* Celda: `ops/db/apply-migration.sh` contra `mail_cell_<code>` (P: runner propio en
  `mail-directory`). Al abrir una celda, en el mismo paso y tras sus migraciones,
  `ops/db/cell-service-role.sh --cell <code>`: crea el rol de sus servicios y cierra a
  PUBLIC las bases del cluster (necesita `mail_service`, de las migraciones 06);
  `ops/db/cell-engine-role.sh --cell <code>`, el rol con el que los motores de esa celda leen
  su directorio; y `ops/db/pgbouncer-userlist.sh --write`, sin cuya entrada ninguno de los dos
  pasa el pooler. Los tres, y `apply-migration.sh`, alcanzan la base por el resolvedor común
  (`ops/db/pg-credentials.sh`), que en el perfil autoalojado los ejecuta en un contenedor de la
  imagen del Postgres en marcha (sección 11); las contraseñas y verificadores viajan por el
  entorno, nunca por argumento. Sus
  `mail-directory` y `mail-security` arrancan con `CELL_CODE=<code>` y `ORGANIZATION_URL` (sin
  las dos no arrancan: cada instancia pregunta a `organization` la celda de cada empresa y
  rechaza con 403 `TENANT_NOT_IN_CELL` a la que no es de la suya) y el gateway recibe sus
  entradas en `MAIL_DIRECTORY_CELL_HOSTS` y `MAIL_SECURITY_CELL_HOSTS` (`<code>=host:puerto`),
  mas `GATEWAY_BASE_CELL_CODE` con la celda de los destinos base (`MAIL_DIRECTORY_HOST`,
  `MAIL_SECURITY_HOST`), obligatoria en cuanto hay una entrada. Con varias celdas el gateway
  pregunta a `organization` (`ORGANIZATION_HOST`) la celda de cada empresa. Sin la entrada de un
  servicio, las empresas de esa celda reciben 503 en el (y el gateway lo avisa al arrancar) y sus
  enlaces de cuarentena llegan a la celda base, que los rechaza (`Modelo_de_Datos_y_Celdas.md`,
  5.3 y 5.4). `GATEWAY_BASE_CELL_CODE` se fija al registrar la segunda celda en organization,
  antes de dar de alta empresas en ella: sin el, el gateway da por hecho una sola celda y las
  mandaria a la base. El webmail de cada celda arranca con su `CELL_CODE` (va en cada token de
  sesion) y sus motores; el gateway recibe el de la celda base en `WEBMAIL_HOST` y los demas en
  `WEBMAIL_CELL_HOSTS`, y lleva cada inicio de sesion a la celda del dominio del buzon (lo pregunta
  a organization) y el resto por la celda del token; una celda sin webmail declarado recibe 503 en
  el inicio de sesion (5.5). `domain-service` lee las mismas variables de mail-directory y
  mail-security, con `MAIL_DIRECTORY_URL` y `MAIL_SECURITY_URL` como destinos base, y necesita
  siempre `ORGANIZATION_URL` (sin ella no arranca): reclama cada dominio en el indice global de
  organization antes de activarlo y, con `GATEWAY_BASE_CELL_CODE`, pregunta la celda de cada
  empresa; activa dominios y entrega las claves DKIM en la instancia de la celda de cada empresa.
  Si esa celda no tiene instancia declarada de un servicio, sus pasos fallan, se reintentan en el
  barrido y se cuentan en `cell_call_failures_total{cell_service, reason}`. Orden de despliegue del
  indice: organization (migracion 029 y sus rutas internas) antes que el `domain-service` nuevo,
  que sin ellas no activa ningun dominio nuevo (los ya activos siguen); el webmail nuevo, con la
  celda en el token, cierra las sesiones abiertas con el anterior. Claves DKIM: `mail-security`
  en todas las celdas antes que el `domain-service` nuevo, que entrega el juego completo de claves
  de cada dominio y un `mail-security` anterior rechaza (las claves ya en los motores no se
  pierden); su primer repaso (`MAIL_DKIM_RECONCILE_INTERVAL`) retira las claves de dominios solo
  de envio o inactivos en el directorio, y cada retirada queda en el registro como `repaso DKIM`
  y en `mail_security_dkim_reconcile_removals_total`. Esa primera pasada corre al arrancar, casi
  siempre antes de que Prometheus vea la serie, y lo que retire no suele avisar; cualquier retirada
  posterior, tambien tras un reinicio, dispara `ClavesDKIMRetiradasPorElRepaso`.
* Reglas: idempotentes y aditivas (`make check-migrations` cubre empresa y celda), cabecera
  `-- Schema | Service`, nunca cambiar el tipo de una columna sin
  `ops/maintenance/pgbouncer-reconnect.sh` después (los planes preparados viven en el pooler).

Antes de desplegar servicios nuevos, la comprobacion de que no falta ninguna migracion no se hace con la lista que
alguien recuerde: se barren TODAS las canonicas, que son idempotentes. Registro y celda, fichero a fichero con
`ops/db/apply-migration.sh` por su numero (`sort -V`), y las de empresa con `bash ops/apply-all-canonical.sh` (48
ficheros, sin errores reales en produccion el 2026-09-21). Motivo: una columna nueva de `mail.mailboxes`
(`11_mailbox_dav_access.sql`) no estaba en la lista recibida y, con el servicio nuevo ya desplegado, crear y listar
buzones dio 500 y `mail-auth` no pudo leer el buzon del remitente de las alertas durante unos minutos. Tras un
`ALTER TABLE` que cambia las columnas de una tabla que los servicios ya consultan hace falta ademas
`ops/maintenance/pgbouncer-reconnect.sh`: PgBouncer conserva sentencias preparadas con el tipo anterior y sigue dando
`cached plan must not change result type` o `prepared statement ... does not exist` hasta renovar sus conexiones.

## 5. Despliegue

* `scripts/deploy-ecr.sh` desde el PC: compila en local, publica a ECR etiquetando por
  commit y el servidor solo hace `pull`. **Nunca `docker compose build` en el servidor** ni
  recrear un contenedor a mano: usaría una imagen local y el servicio correría otro código
  (`scripts/check-image-drift.sh` lo delata). Tampoco se publica desde el servidor: se
  retiró `ops/ecr/ecr-sync.sh`, que subía a ECR las imágenes locales del host.
* GitHub Actions (`release.yml`): construye en paralelo las imágenes de los servicios
  afectados (detectados con `ops/scaffold/service-paths.sh`), con OIDC hacia AWS y sin
  claves estáticas, a los repositorios `core-force-mail/<servicio>` de ECR; el despliegue es
  un `workflow_dispatch` con `deploy=true` que entra por SSM, nunca por SSH. Sin la variable
  de repositorio `AWS_ECR_ROLE_ARN` (la crea `ops/aws/setup-github-oidc.sh`) no se publica:
  un push termina en verde con un aviso y un `deploy=true` falla con el motivo.
* Guarda de entorno (1, «Entorno declarado»): `release.yml` no sincroniza ficheros y
  ejecuta la copia de `with-secrets.sh` que ya está en el servidor; la guarda llega con el
  rsync de `scripts/deploy-ecr.sh`, que en un servidor nuevo es el primer despliegue.
* Confianza del rol OIDC de GitHub (`ops/aws/setup-github-oidc.sh`): `aud`
  `sts.amazonaws.com` y `sub` exactamente `repo:<repo>:ref:refs/heads/main` (build-push de
  un push a main o de un `workflow_dispatch` lanzado desde main) o
  `repo:<repo>:environment:production` (el job deploy), con `StringEquals`. Ni otra rama ni
  un `pull_request` asumen el rol, y `release.yml` corta con el motivo una publicación
  pedida desde otra rama. Formato del claim en
  https://docs.github.com/en/actions/reference/security/oidc: con environment, el `sub` no
  lleva la rama, así que otra ejecución que declare `production` obtiene el mismo claim; lo
  cierra la regla de ramas del environment en GitHub (solo `main`), que se configura aparte.
  Volver a correr el script reescribe la confianza de un rol que ya existía.
* Nombres propios en AWS y en el servidor: prefijo `core-force-mail-` (políticas inline
  `core-force-mail-<capacidad>` y usuario `core-force-mail-deploy-local` de `setup-iam.sh`,
  trail `core-force-mail-auditoria`, unidades `core-force-mail-backup*`, raíz
  `/opt/core-force-mail`, alias `core-force-mail-prod`). Los `core-force-*` venían del ERP;
  nada estaba desplegado, así que no hubo recursos vivos que migrar. En una cuenta donde se
  hubieran aplicado los nombres anteriores, `setup-iam.sh` no los borra: lista las políticas
  del rol que no gestiona, y el usuario anterior se retira a mano. `make clean-copy` falla
  con cualquier `core-force` que no sea `core-force-mail`.
* Todo lo que el servidor ejecuta o monta viaja en el rsync de `stage_head_files` desde
  `git archive` de HEAD, con una sola lista para los tres caminos (`FICHEROS_SERVIDOR` de
  `scripts/deploy-ecr.sh`): compose (incluido `docker-compose.selfhosted.yml`), `selfhosted/`,
  `migrations/`, `ops/db`, `ops/security`, `ops/ecr`, `ops/observability`, `ops/maintenance`,
  `ops/backup`, `pgbouncer`.
* Perfil del servidor: los dos caminos preguntan a `ops/maintenance/perfil-despliegue.sh` (que
  `release.yml` lleva en base64) los argumentos de compose y la infraestructura propia del
  perfil; con `DEPLOY_PROFILE=selfhosted` aplican lo de la sección 11.
* Commits siempre con pathspec (`git commit -- <rutas>`): el índice puede estar compartido
  con otra sesión.
* Los dos overrides de imagen son generados (`make gen-compose-images`, un servicio con `build:`
  no puede quedar fuera de ninguno; CI falla si quedan atrás): `docker-compose.images.yml` para
  ECR y `docker-compose.images.save.yml` para el transporte `save`.
* Transporte `save` (sin AWS, el del perfil autoalojado): también etiqueta por commit. El PC
  construye `core-force-mail/<servicio>:<commit>` con `docker-compose.images.save.yml`
  (`pull_policy: never`: esa imagen no está en ningún registro, llega con `docker save | ssh docker
  load` y se comprueba con `docker image inspect` antes de seguir), el servidor recrea con ese
  `DEPLOY_TAG` y el despliegue verifica que cada contenedor corre esa imagen
  (`verificar_imagen_desplegada` de `scripts/lib/despliegue.sh`, la misma pieza en los dos
  transportes y en los motores). Antes enviaba `app-<servicio>:latest`, y con una etiqueta flotante
  ni la guardia de retroceso ni la verificación podían saber qué commit corría cada servicio: el
  despliegue «salía bien» aunque dejara código viejo dentro, y no había rollback por etiqueta.
  Rollback: desde el commit anterior, `DEPLOY_ALLOW_ROLLBACK=1 ./scripts/deploy-ecr.sh <servicios>`.
* Migración de un servidor anterior al etiquetado (imágenes `app-<servicio>:latest`): sin lista a
  mano. `servicios_sin_commit` (`scripts/lib/despliegue.sh`) añade a la detección automática los
  servicios cuyo contenedor corre una imagen sin etiqueta de commit, así que el primer
  `./scripts/deploy-ecr.sh` sin argumentos los reconstruye y los recrea a todos —un despliegue
  grande, una sola vez— y desde entonces no selecciona nada. Con lista explícita migran solo los de
  la lista; el resto sigue en `latest` hasta que les toque, y `scripts/check-image-drift.sh` los
  cuenta en su línea final. Las `app-<servicio>:latest` quedan sin uso cuando todos corren su
  commit (el prune solo mira `core-force-mail/`): se retiran a mano una vez,
  `docker image rm app-<servicio>:latest`, igual que las `mail-<motor>:latest` de los motores.
* Imágenes viejas: cada despliegue conserva las tres últimas por servicio y retira el resto, en el
  servidor y —desde que el transporte `save` etiqueta por commit y ya no sobrescribe una sola
  imagen— también en el PC que construye (`ops/ecr/prune-local-images.sh --apply`). Sin eso el
  disco se llena, y cuando se llena no falla el despliegue: falla Postgres.
* `scripts/check-image-drift.sh` distingue tres orígenes: `ecr`, `save` (etiquetada por commit y
  llevada con `docker save`) y una imagen sin etiqueta de commit, que es la que solo puede salir de
  un `build` en el servidor. Denuncia el paso de las dos primeras a la tercera, también en el perfil
  autoalojado, donde antes todo era «local» y esa regresión era invisible.
* Motores de correo (`deploy/mail`, proyecto compose `mail`): `scripts/deploy-mail.sh` desde el PC,
  con el mismo destino, candado y guardia de retroceso que `deploy-ecr.sh` (`scripts/lib/despliegue.sh`).
  Construye en local con `deploy/mail/docker-compose.mail.images.yml` (`core-force-mail/<motor>:<commit>`,
  `pull_policy: never`), transporta con `save` (los motores no tienen repositorios en ECR), extrae
  `deploy/mail` de HEAD en `MAIL_DEPLOY_PATH` y recrea los motores uno a uno con `--no-build` por
  `with-secrets.sh`, esperando a que cada uno arranque; registra el commit de cada motor en
  `.deployed-tags`. Detalle en `deploy/mail/README.md` («Despliegue en un servidor»). Nunca
  `docker compose build` ni `up` a mano de los motores en el servidor.
* Red `mail-engines` y volumen de certificados: son del proyecto de los motores y los crea Compose al
  levantar el primero, con sus etiquetas. La plataforma no los crea (crearlos con otra subred o sin
  las etiquetas los dejaría incompatibles con el compose que los declara): `deploy-ecr.sh`, tras leer
  el perfil y antes de recrear nada, comprueba con `ops/maintenance/recursos-externos.sh` las redes y
  volúmenes externos de su compose y se detiene pidiendo desplegar antes los motores. `release.yml`
  no lo comprueba (no sincroniza `ops/`); en un servidor así Compose falla con el nombre del recurso.
* Red `mail-migration` (V, 2026-09-21): la crea el compose de los motores, como `mail-engines`, y une a Dovecot,
  al ejecutor `mail-migration-runner` y al servicio `mail-migration` de la plataforma (que la declara externa). Es
  la primera vez que la plataforma exige una red nueva de los motores: en un servidor que ya corre, desplegar los
  motores (`scripts/deploy-mail.sh`) ANTES de la plataforma, o `deploy-ecr.sh` se detendra en
  `recursos-externos.sh` diciendo que falta `mail-migration`. La migracion de buzones se activa con dos claves
  opcionales del almacen (`MAIL_MIGRATION_RUNNER_KEY`, y el maestro propio `DOVECOT_MIGRATION_MASTER_USER` y
  `DOVECOT_MIGRATION_MASTER_PASS`) y con la contrasena del rol de base `MAIL_MIGRATION_DB_PASSWORD`, que
  crea `ops/db/tenant-service-role.sh --service mail-migration`; sin la clave del ejecutor el servicio arranca y
  la API responde 503 `NOT_CONFIGURED`, sin la del rol arranca con la credencial de plataforma y lo avisa. Credencial de
  destino por trabajo (2026-09-21, `docs/adr/0002`): con `MAIL_MIGRATION_JOB_CREDENTIALS=true` en `mail-migration`, y
  `MAIL_MIGRATION_URL` y `MAIL_AUTH_JOB_ALLOWED_NETS` en `mail-auth`, el ejecutor entra al buzon con una credencial que solo
  abre ese buzon mientras el trabajo esta en curso y el maestro `DOVECOT_MIGRATION_MASTER_*` se puede retirar del almacen. Orden
  (desplegar el codigo, activar `mail-auth`, activar `mail-migration`, probar una migracion, retirar el maestro y recrear
  Dovecot y el ejecutor) y reversion en `deploy/mail/README.md`, "Migracion de buzones". Dovecot se recrea una vez para
  leer la passdb nueva; la migracion de empresa `04_destination_credential.sql` (y `04_reconcile.sql` de `mail-dav`) las
  aplica el runner de empresas al desplegar. La conciliacion de buzones borrados de `mail-dav` y `mail-migration` corre sola
  (`*_RECONCILE_INTERVAL`, `0` la apaga) y necesita `MAIL_DIRECTORY_URL`, que el `.env` ya tiene.
* CardDAV (`mail-dav`, V: codigo y pruebas; P: cliente real, 2026-09-21, `docs/adr/0004-contactos-y-calendario-carddav-caldav.md`):
  servicio de empresa (puerto 8058) en `mail-internal` y en `mail-engines` (para el listener TLS de `mail-auth`,
  `MAIL_AUTH_URL`, la misma variable que usa el webmail; con el perfil autoalojado verifica su certificado con la CA
  interna, `MAIL_DAV_TLS_CA_FILE`). No exige nada nuevo para que el resto arranque: sin `MAIL_DAV_DB_PASSWORD` (rol de
  base creado por `ops/db/tenant-service-role.sh --service mail-dav` tras aplicar las migraciones de empresa, que crean
  su grupo `mail_dav_service`) arranca con la credencial de plataforma y lo avisa, sin RLS efectiva. El gateway lo
  expone en `/api/v1/dav` sin JWT y redirige `/.well-known/carddav` y `/.well-known/caldav` al prefijo. **HTTP Basic solo es admisible con
  TLS**: el proxy de borde ya termina HTTPS y `mail-dav` solo acepta el token del gateway; no se expone el puerto 8058
  fuera de `127.0.0.1`. El borde y Cloudflare deben dejar pasar `PROPFIND`, `REPORT`, `MKCOL` y `MKCALENDAR` (nginx los reenvia; en
  Cloudflare hay que comprobarlo con un cliente real). `MAIL_DAV_PUBLIC_URL` (con el prefijo, https) hace que la ficha
  del buzon muestre los datos de conexion; vacia, no los ofrece. Con varias celdas, `MAIL_AUTH_CELL_URLS` da el
  `mail-auth` de cada una (ademas de `GATEWAY_BASE_CELL_CODE` y `ORGANIZATION_URL`). Metricas y salud como el resto
  (`mail-dav:8058` en `targets.json`); el freno de fuerza bruta es el de `mail-auth` (Redis), y los limites por buzon
  (`MAIL_DAV_MAX_*`, con los de CalDAV: eventos, calendarios y el presupuesto de expansion de recurrencias) son del
  operador hasta que pasen al plan de `billing`. La migracion `03_caldav.sql` es aditiva e idempotente y crea las tablas
  de calendarios; la base de zonas horarias va embebida en el binario (la imagen es `scratch`).
* Salida por Amazon SES: `ops/aws/setup-ses.sh <dev|staging|prod>` aplica la pila
  `ops/aws/ses-mail.yaml` (CloudFormation): los configuration sets transaccional y de
  marketing (reputación y TLS por clase; aperturas, clics y bajas solo en marketing, con
  dominio de seguimiento propio y pool de IP dedicadas opcionales), un topic SNS estándar
  cifrado con una clave KMS propia al que solo publican esos dos sets de la cuenta, y la
  suscripción HTTPS a `/api/v1/public/transactional/ses-events`, que `transactional`
  confirma y verifica. `--check` muestra el conjunto de cambios sin aplicarlo. Sus salidas
  son `SES_EVENTS_TOPIC_ARN`, `SES_CONFIG_SET_TRANSACTIONAL` y `SES_CONFIG_SET_MARKETING`.
  `SES_EVENTS_TOPIC_ARN` es **obligatoria** para las dos rutas de eventos (la global y
  `/ses-events/{empresa}`): una firma valida de SNS solo prueba que firmo AWS, y cualquier cuenta
  de AWS puede crear un topic y suscribir esta URL, asi que sin el ARN fijado se rechaza todo. El
  topic se compara antes de verificar la firma (un topic ajeno no cuesta una descarga de certificado)
  y la descarga no sigue redirecciones.
  No verifica dominios ni saca la cuenta del sandbox: eso es por empresa y con su DNS.
* Antes y después de recrear, en los dos caminos (`ops/scaffold/check-deploy-preflight.sh`,
  sección 10 de `validate.sh`). Antes: `ops/db/pgbouncer-userlist.sh --ensure` genera el
  `userlist.txt` si falta, porque sin él PgBouncer no arranca, y si no coincide solo avisa:
  reescribirlo a ciegas quitaría el rol cuya contraseña no esté en el entorno y dejaría fuera a
  un servicio que hoy entra. `release.yml`, que no sincroniza `ops/`, usa `--write` solo cuando el
  fichero no existe. `ops/maintenance/claves-env.sh` nombra las claves de `.env.example` que el
  `.env` del servidor no tiene, sin leer ni imprimir valores y sin bloquear. Después:
  `ops/maintenance/esperar-sanos.sh` espera a que cada servicio recreado quede sano, o corriendo
  sin reiniciarse si su imagen no declara chequeo, y si no el despliegue falla con su estado y sus
  últimas líneas de registro. Antes solo se comprobaba que corriera la imagen nueva, y un servicio
  en bucle de reinicios por una clave ausente (`SCHEDULER_URL` en analytics) pasaba por bueno.
  `release.yml` lleva los dos guiones dentro del script remoto, en base64.

## 6. Respaldos

Razones largas y procedimientos: `ops/backup/README.md`.

`ops/backup/backup-tenants.sh` vuelca cada base (`mail_%`: registro, celdas y empresas) por
separado con `pg_dump -Fc`, la lee entera para comprobar que `pg_restore` la entiende, deja su
`.sha256` y conserva `BACKUP_KEEP_DAYS` (3) días en local, podando solo tras una corrida sin
fallos. `ops/backup/backup-mail-volumes.sh` archiva los volúmenes de correo de `deploy/mail`
(`crypt-vol`, la clave de `mail_crypt` sin la que los buzones son ilegibles, y `vmail-vol`, los
buzones); el resto de volúmenes se regenera y no entra (tabla en `ops/backup/README.md`).

Cada semana `verify-restore.sh`, sin argumentos, restaura en una base desechable el último volcado
de **una base de cada clase** (registro, la celda mayor y la empresa mayor) y comprueba, sin
suponer filas de negocio, la suma del volcado, que están todos los esquemas y tablas de su índice,
`platform.event_outbox`, el historial `public.schema_migrations` con filas y el esquema que declara
cada migración registrada; en una celda, que no tiene historial, los esquemas de sus migraciones, y
en el registro al menos un usuario.

Copia externa OPCIONAL a un bucket S3 o S3-compatible: `BACKUP_S3_BUCKET` (distinto al de medios;
la misma variable en `ops/aws/setup-iam.sh`, `setup-buckets.sh` y `setup-auditoria.sh`) y, para un
tercero como Cloudflare R2, `BACKUP_S3_ENDPOINT` y `BACKUP_S3_REGION`. Con endpoint, el cifrado en
reposo es obligatorio (`gpg` simétrico AES-256 con `BACKUP_ENCRYPTION_PASSPHRASE`) y `crypt-vol` no
sale del servidor sin cifrar en ningún caso; sin el destino, la copia local funciona igual. Los
tres secretos del respaldo (credencial del bucket y frase) viven en `BACKUP_SECRETS_FILE`
(`/opt/core-force-mail/env/backup.env`, 0600 del usuario de despliegue) y están declarados en
`ops/security/secrets/secret-keys-backup.txt`: **no** en el `.env`, que Compose entrega entero a
cada contenedor. La frase se guarda también fuera del servidor.

Las herramientas de Postgres en el perfil autoalojado corren en un contenedor de la imagen del
Postgres en marcha (sección 11, «Clientes de Postgres»).

Programación: `sudo ops/backup/install-timers.sh` instala las unidades de `ops/backup/systemd/` con
el usuario y la ruta del servidor y activa `core-force-mail-backup.timer` (08:15 UTC),
`core-force-mail-backup-mail.timer` (08:45) y `core-force-mail-backup-verify.timer` (domingos
09:30); es idempotente, lo llama `bootstrap.sh` y se ejecuta igual en un servidor ya aprovisionado.
Alertas del grupo `respaldos` en `ops/observability/prometheus/rules/plataforma.yml`: vigilan la
antigüedad del último éxito, el error de la última corrida y la ausencia de la serie.

## 7. Observabilidad

`docker-compose.observability.yml` (Prometheus, Grafana, Loki, Promtail, node-exporter,
docker-socket-proxy) se une a la red `mail_mail-internal` como externa. Los objetivos se
generan desde el compose (`make gen-observability-targets`); las reglas de alerta tienen
pruebas de `promtool` (`make check-alertas`). Cada servicio expone `/healthz` (el proceso vive), `/readyz`
(la base y el bus responden, 503 si no) y `/metrics` fuera de su cadena de middlewares; las imágenes son `FROM scratch` y el `HEALTHCHECK` usa
el propio binario con `--healthcheck <puerto>`.

## 8. Alta de un servicio

1. `make new-service name=<svc> port=<puerto> module=<modulo>`: molde hexagonal que compila,
   Dockerfile, migración canónica de empresa, puerto en `.env.example`.
2. Ruta en `services/gateway/routes.json` (`prefix`, `service`, `module`).
3. Permisos del módulo en `migrations/registry/NNN_<svc>_permissions.sql` y, si es de
   correo, su módulo de catálogo en `organization.module_catalog.permission_modules`.
4. Bloque en `docker-compose.yml` (el generador imprime el fragmento) y su fila en
   `ops/security/secrets/reparto.tsv`: solo los secretos que su código lee, empezando por `INTERNAL_GATEWAY_TOKEN`.
5. `make validate-scaffold` comprueba puertos, permisos, catálogo, acoplamiento, streams e
   imágenes. `make gen-events` y `make gen-observability-targets` regeneran lo derivado.

## 9. Checks antes de dar por terminada una tarea

`make checks` (build, vet, migraciones, acoplamiento, errores mudos, aridad SQL, streams,
contratos de eventos, secretos y su reparto por contenedor, scaffold) y `make clean-copy`. Si se toca el perfil autoalojado
(`docker-compose.selfhosted.yml`, `selfhosted/`, `pgbouncer/`, `ops/security/internal-tls.sh`),
además `bash ops/scaffold/test-selfhosted-profile.sh` (docker, sección 11); si se toca `ops/backup`
o `ops/db/pg-credentials.sh`, `bash ops/scaffold/test-selfhosted-backup.sh` (docker, sección 6). Con docker: `make test`
(con `-race`) y `make e2e`, que levanta Postgres, NATS y Redis desechables, compila los
servicios integrados y recorre la plataforma de punta a punta con comprobaciones que
fallan: un servicio nuevo que otro consume se añade a `ops/e2e/run.sh` en la misma tarea.
Cada una corre de una en una: la reserva de `ops/e2e/lib.sh` (`e2e_reservar`) hace salir con 3
una segunda `make e2e` (o `make e2e-mail`) sin tocar los contenedores de la primera; una
reserva cuyo proceso ya no existe es un resto y se reemplaza. `make e2e` y `make e2e-mail`
pueden correr a la vez, con prefijos y puertos distintos.
`make test-integration` (también un job de CI) corre las pruebas `//go:build integration`
contra un Postgres, un Redis y un NATS con JetStream desechables (el NATS lo levanta siempre el script, con una credencial de la ejecución, en el puerto base+3), con una base por variable `*_TEST_DSN` y los
paquetes en serie; una prueba que se salta cuenta como fallo. Una prueba de integración
nueva aplica ella misma sus migraciones (dos veces, en su base) y su variable se declara en
`ops/scaffold/test-integration.sh`, o el job falla.

## 10. Eventos abandonados (`EVENTS_DLQ`)

Un consumidor durable (`pkg/events`, `DurableQueueSubscribe`) que no confirma un evento lo recibe
otra vez cada 90 s. Tras 20 entregas sin confirmar (unos 30 minutos), o en la primera si el cuerpo no
es un evento, lo copia en el stream `EVENTS_DLQ` bajo `dlq.<subject original>` y deja de recibirlo.
La copia lleva el cuerpo intacto y las cabeceras `Dlq-Stream` (stream de origen), `Dlq-Consumer`
(quién lo abandonó), `Dlq-Reason` (`max_deliveries` o `undecodable`), `Dlq-Deliveries` y
`Dlq-Stream-Sequence` (su secuencia en el stream de origen). `EVENTS_DLQ` guarda 30 días; los
streams de origen, 7. Nada reproduce un evento abandonado por su cuenta. Avisan
`EventosAbandonadosEnDLQ`, `EventosAbandonadosSinCopia` y `DLQConMensajesSinRevisar`
(`docs/arquitectura/OBSERVABILIDAD.md`, Eventos), y el registro del servicio («evento abandonado y
guardado en EVENTS_DLQ») da stream, consumidor, subject, `stream_seq` y motivo. Las copias llevan
datos de las empresas: se leen en el servidor y no salen de él.

Herramienta: el CLI `nats` de `natsio/nats-box:0.14.5` en la red del despliegue (NATS no se publica
fuera del host), en una sesión interactiva con el `NATS_URL` del `.env`:

```bash
docker run --rm -it --network "${APP_NETWORK:-app_mail-internal}" \
  -e NATS_URL=nats://nats:4222 natsio/nats-box:0.14.5
```

1. Inspeccionar, sin cambiar nada. `<seq>` es la secuencia en `EVENTS_DLQ` (la da `view`):

   ```bash
   nats stream subjects EVENTS_DLQ                # cuántos por subject
   nats stream view EVENTS_DLQ                    # cuerpos y cabeceras, página a página
   nats stream get EVENTS_DLQ <seq> --json > /tmp/m.json
   jq -r .hdrs /tmp/m.json | base64 -d            # Dlq-Stream, Dlq-Consumer, Dlq-Reason...
   jq -r .data /tmp/m.json | base64 -d | jq .     # el evento
   ```

2. Corregir la causa antes de reproducir: el registro del consumidor tiene el error de cada
   entrega, y un evento reproducido con la causa viva vuelve a la cola en media hora.
   `undecodable` es un fallo de quien publica: se corrige el publicador y la copia se descarta
   (paso 5), no se reproduce.
3. Decidir si el efecto sigue siendo válido: llega después de lo que haya pasado desde entonces con
   el mismo objeto (un buzón reactivado, una clave rotada otra vez). Los consumidores que leen el
   estado actual (mail-security, webmail, access-control) no se ven afectados; ante la duda, se
   descarta y se deja constancia en el incidente.
4. Reproducir: el cuerpo tal cual, en el subject original. Lo reciben otra vez TODOS los
   consumidores durables de ese subject (`docs/arquitectura/EVENTS.md`), no solo el que lo
   abandonó; son idempotentes por el `id` del evento o por el estado que leen, así que para los
   demás es un duplicado sin efecto. El cuerpo no se edita nunca: el `id` es la clave de
   idempotencia. `nats pub` no espera la confirmación del stream y trata el cuerpo como plantilla
   de Go si puede (`{{ID}}`, `{{Count}}` o `{{Time}}` se publicarían cambiados): de ahí la guarda
   de antes y la comprobación de después, que exige los mismos bytes en el stream de origen.

   ```bash
   subject=$(jq -r '.subject | sub("^dlq\\."; "")' /tmp/m.json)
   origen=$(jq -r .hdrs /tmp/m.json | base64 -d | tr -d '\r' | sed -n 's/^Dlq-Stream: //p')
   jq -r .data /tmp/m.json | base64 -d > /tmp/body
   grep -q '{{' /tmp/body && echo 'NO reproducir con nats pub: el cuerpo lleva {{'
   nats pub "$subject" --force-stdin < /tmp/body
   [ "$(nats stream get "$origen" -S "$subject" --json | jq -r .data)" = "$(jq -r .data /tmp/m.json)" ] \
     && echo reproducido
   ```

   Sin `reproducido`, otro evento del mismo subject llegó entre medias o el cuerpo cambió:
   `nats stream view "$origen" --subject "$subject"` muestra lo guardado. Después,
   `nats consumer info "$origen" <Dlq-Consumer>` y el registro del consumidor confirman que lo
   aplicó.
5. Retirar de la cola lo reproducido y lo descartado: `nats stream rmm EVENTS_DLQ <seq> -f`.
   `DLQConMensajesSinRevisar` sigue activa mientras quede alguno.

`EventosAbandonadosSinCopia`: el evento no está en `EVENTS_DLQ`, solo en su stream de origen hasta
que se cumplen sus 7 días. Primero se recupera JetStream (registro del servidor NATS, espacio del
volumen `natsdata`); después, con el stream y el `stream_seq` del registro,
`nats stream get <stream> <stream_seq> --json > /tmp/m.json` y los pasos 2 a 4 con
`subject=$(jq -r .subject /tmp/m.json)` y `origen=<stream>`.

No se borra ni se recrea un consumidor para repetir un evento (se recrea con `DeliverAll` y vuelve a
procesar los 7 días de su stream), ni se vacía `EVENTS_DLQ` con `nats stream purge` sin revisar cada
mensaje.

## 11. Producción autoalojada (`DEPLOY_PROFILE=selfhosted`)

Toda la plataforma en un servidor propio (probado en un VPS Debian 13 de 2 vCPU y 3,8 GB de RAM y, en
producción desde 2026-09-17, en uno Ubuntu 24.04 de 4 vCPU y 8 GB de RAM con 4 GB de swap), sin RDS,
ElastiCache ni balanceador. Con 3,8 GB quedaba 1 GB libre solo con los motores de correo levantados
(ClamAV ocupa 1 GB): para volumen real, 8 GB. En `ENVIRONMENT=production` los servicios
exigen lo que en AWS dan esos servicios gestionados, y el perfil lo da sin relajar nada:

| Control | En AWS | En el perfil |
|---|---|---|
| TLS de PgBouncer a Postgres, `verify-full` | Bundle de RDS | CA interna: `DB_UPSTREAM_CA_FILE` (`pgbouncer/entrypoint.sh`) |
| Postgres solo cifrado | `rds.force_ssl` | `ssl=on`, TLS 1.2 mínimo y `selfhosted/postgres/pg_hba.conf` (`hostssl` con `scram-sha-256`, `hostnossl` rechazado) |
| Redis con TLS y contraseña | ElastiCache | `redis` sin puerto en claro (`--port 0`, `tls-port 6379`), contraseña por la entrada estándar (`selfhosted/redis/entrypoint.sh`) |
| Clientes de Redis con TLS verificado | CA pública | `REDIS_TLS=true`, `REDIS_TLS_CA_FILE` y `REDIS_TLS_SERVER_NAME=redis` en todo servicio Go |
| mail-auth (9082) con certificado verificable | `MAIL_AUTH_TLS_*` a mano (vacías: autofirmado en memoria) | CA interna: `MAIL_AUTH_TLS_CERT`/`MAIL_AUTH_TLS_KEY` del directorio montado, SAN `mail-auth`; el webmail lo verifica con `WEBMAIL_TLS_CA_FILE`; mail-auth corre sin root (`user: 65532:65532`) |
| HTTPS delante del gateway | Balanceador | `edge-proxy` (nginx fijado por digest, `selfhosted/edge`) |

Piezas: `docker-compose.selfhosted.yml` (superposición sobre `docker-compose.yml`; activa
`postgres-primary` sin el perfil `embedded-db`), `selfhosted/` (pg_hba, arranque de Redis y
configuración del borde), `ops/security/internal-tls.sh` (CA y certificados),
`ops/security/systemd/core-force-mail-internal-tls.{service,timer}` (renovación),
`ops/maintenance/perfil-despliegue.sh` (lo que decide el despliegue) y dos guardarraíles:
`ops/scaffold/check-selfhosted-profile.sh` (sin docker, sección 11 de `validate.sh`) y
`ops/scaffold/test-selfhosted-profile.sh` (con docker).

### Clientes de Postgres y su modo TLS

Toda conexión TCP a `postgres-primary` negocia TLS o se rechaza. Comprobado con clientes reales
contra el perfil (`test-selfhosted-profile.sh` y a mano el 2026-09-17):

* PgBouncer: `verify-full` contra la CA interna; con la IP en vez de `postgres-primary`, falla
  por el nombre.
* Servicios Go: van por PgBouncer (sin TLS dentro de la red interna, como en AWS). Sus conexiones
  directas (`POSTGRES_DIRECT_HOST=postgres-primary`, que fija el perfil para las migraciones)
  usan `sslmode=prefer` de `pkg/config`: pgx negocia TLS 1.3. `prefer` no verifica el
  certificado; ver Riesgos.
* Motores de correo: `MAIL_DB_HOST=postgres`, que el perfil resuelve a PgBouncer también en la
  red `mail-engines`; Postgres no entra en esa red. Si se apuntaran a `postgres-primary`, su
  libpq (enlazada con OpenSSL en las imágenes de Postfix, Dovecot y acme) negocia TLS 1.3 por
  defecto: un mapa `pgsql:` de Postfix y el `psql` de Dovecot y de acme lo hicieron, y con
  `sslmode=disable` Postgres los rechazó.
* Guiones del host que van por `ops/db/pg-credentials.sh` (el respaldo, la restauración y su
  verificación): NO usan el cliente del host. En este perfil `postgres-primary` no se publica en el
  host y su nombre solo existe en la red interna, así que `cf_psql`, `cf_pg_dump` y `cf_pg_restore`
  corren en un contenedor efímero de la **misma imagen que el Postgres en marcha** (por su
  identificador, resuelto con `docker inspect`), en su red, con `sslmode=verify-full` contra
  `<INTERNAL_TLS_DIR>/publico/ca.crt`, de solo lectura, sin capacidades y con el uid del usuario del
  respaldo; la contraseña entra por `-e PGPASSWORD`, solo el nombre. Se eligió frente a
  `PGHOSTADDR` con el cliente del host porque la versión del cliente tiene que ser la del servidor:
  el `pg_dump` 17 de Debian 13 escribe el formato 1.16 y el `pg_restore` 16 de la imagen no lo lee
  (`unsupported version (1.16) in file header`), y respaldar con una herramienta y restaurar con otra
  convierte la copia en una apuesta. El usuario que los ejecuta necesita el grupo `docker`.
  La entrada estándar se declara: `cf_psql`, `cf_pg_dump` y `cf_pg_restore` **no** leen la del
  guion (se sustituye por `/dev/null`) y `cf_psql_entrada` sí, porque es su SQL o su fichero.
  `docker run -i` se lleva la entrada entera, y por eso `tenant-service-role.sh --all` creó un solo
  rol y salió con 0 en el primer servidor autoalojado: el bucle
  `while read svc; …; done < <(servicios_de_empresa)` perdió su lista en la primera consulta. Lo
  ata `ops/scaffold/check-backups.sh` (estático y ejecutado).
  Por el mismo camino van los demás guiones que abren la base (`ops/db/apply-migration.sh`,
  `ops/apply-all-canonical.sh`, `cell-service-role.sh`, `cell-engine-role.sh`,
  `tenant-service-role.sh`, `bootstrap-platform.sh`): las migraciones entran por la entrada
  estándar, así que el fichero no tiene que existir dentro del contenedor, y los secretos que el
  SQL necesita (el verificador SCRAM y la contraseña del primer superadmin) se pasan por el
  entorno con `cf_pg_pasar_entorno` y se leen con `\getenv`, nunca con `-v`: si no, quedarían en la
  línea de órdenes de `psql`, en la de `docker run` y en `docker inspect` del contenedor efímero.
  El verificador SCRAM lo sigue calculando `python3` en el host, que es donde está la contraseña.
  `ops/maintenance/pgbouncer-reconnect.sh` no pasa por aquí: habla con la consola de
  administración del pooler dentro de su propio contenedor (`docker exec`).

### Proxy de borde

* Sirve el host de `PUBLIC_BASE_URL` (o `EDGE_PUBLIC_HOST`) en 443 con `cert.pem` y `key.pem` del
  volumen de acme de `deploy/mail` (`MAIL_SSL_VOLUME`, por defecto `mail_ssl-vol`): el
  certificado lo emite `acme-mail` por DNS-01 (`ACME_DNS_CHALLENGE=y`, `ACME_DNS_PROVIDER=dns_cf`)
  con ese host en `ADDITIONAL_SAN`. Vigila el certificado cada `EDGE_CERT_CHECK_INTERVAL` y recarga
  nginx cuando acme lo renueva.
* 80 redirige a `https://<host>`; otro nombre en 80 se cierra sin respuesta y en 443 se rechaza
  el saludo TLS sin presentar certificado.
* Con `EDGE_REQUIRE_CLOUDFLARE=true` (por defecto) solo atiende conexiones desde los rangos de
  `selfhosted/edge/cloudflare-ips.txt` (403 al resto), y solo de esas toma la IP del visitante de
  `CF-Connecting-IP`. Reescribe `X-Real-IP` y `X-Forwarded-For` y borra `CF-Connecting-IP`. El
  gateway acepta `X-Real-IP` solo desde `EDGE_NETWORK_SUBNET`, la red `edge`, donde no hay más que
  el borde y el gateway; el borde no entra en la red interna. La lista se contrasta con la de
  Cloudflare con `ops/security/edge-cloudflare-ips.sh --comprobar` (`--escribir` la actualiza).
* Cabeceras: pone HSTS (`EDGE_HSTS_MAX_AGE`, con `includeSubDomains`) y quita la versión de nginx.
  Las de la aplicación (CSP con nonce, `X-Frame-Options`, `nosniff`, `Referrer-Policy`) son del
  gateway y no se repiten (`docs/arquitectura/CSP-Y-SESION.md`).
* Límites: `EDGE_MAX_BODY_SIZE=101m`, el mayor envío del webmail (`postfixMessageSizeLimit` más
  `multipartOverhead`); cubre la importación de `CONTACTS_IMPORT_MAX_ROWS` de `.env.example`
  (`check-selfhosted-profile.sh` ata los tres). El cuerpo se recibe entero antes de pasarlo al
  gateway (lee con un plazo de 15 s) y lo que no cabe en memoria va a un tmpfs
  (`EDGE_BODY_TMPFS_SIZE`), nunca a disco; tiempos de espera alineados con el `WriteTimeout` de
  120 s del gateway. Contenedor de solo lectura, sin capacidades salvo las cuatro que necesita el
  maestro, con `EDGE_MEMORY_LIMIT`.
* Cloudflare admite cuerpos de hasta 100 MB en sus planes Free y Pro: por encima, el límite real
  de un envío es el suyo, no el del borde.

### Procedimiento en el servidor, en orden

Desde el puesto de trabajo, con el repositorio en el commit a desplegar (`<srv>` es el servidor):

1. Aprovisionar. En `ops/server-template/server.env`: `ENVIRONMENT=production`,
   `DEPLOY_PROFILE=selfhosted`, `GRUB_CONSOLA_SERIE=auto` (no aplica la consola serie de EC2) y
   `DEPLOY_PUBKEY` **entre comillas**. Copiar la plantilla junto con lo que genera el TLS y
   ejecutar:

   ```bash
   git archive HEAD docker-compose.yml docker-compose.selfhosted.yml ops/server-template ops/security ops/backup ops/maintenance | \
     ssh root@<srv> 'rm -rf /tmp/cfm && mkdir /tmp/cfm && tar -x -C /tmp/cfm'
   scp ops/server-template/server.env root@<srv>:/tmp/cfm/ops/server-template/
   ssh root@<srv> /tmp/cfm/ops/server-template/bootstrap.sh
   ```

   `bootstrap.sh` escribe `ENVIRONMENT` y `DEPLOY_PROFILE` en `/opt/core-force-mail/app/.env`,
   genera el TLS interno (paso 2), programa `core-force-mail-internal-tls.timer` y, con
   `ops/backup` a mano, instala los temporizadores del respaldo (`ops/backup/install-timers.sh`).
   Si no viajó, se ejecutan a mano tras el primer despliegue:
   `sudo /opt/core-force-mail/app/ops/backup/install-timers.sh`.
2. TLS interno (si no lo hizo el paso 1, o para revisarlo):
   `sudo /tmp/cfm/ops/security/internal-tls.sh` y `sudo /tmp/cfm/ops/security/internal-tls.sh
   --comprobar`. Crea en `INTERNAL_TLS_DIR` (por defecto `/opt/core-force-mail/tls`) la CA
   (`ca/ca.key`, 0600 de root, nunca se monta), `publico/ca.crt` y los certificados de
   `postgres-primary`, `redis` y `mail-auth` con los SAN de su servicio, su contenedor
   (`app-<servicio>-1`), `localhost` y `127.0.0.1`, con la clave 0600 (en un directorio 0700) del
   uid y gid que el guion lee de cada imagen fijada (hoy postgres 999:999 y redis 999:1000) y,
   para mail-auth, cuya imagen es `scratch` y no está en el servidor, del `user:` que le fija
   `docker-compose.selfhosted.yml` (65532:65532; por eso ese fichero viaja con la plantilla).
   Se montan directorios, no ficheros, para que la renovación llegue al contenedor. Nunca imprime
   una clave.
3. `.env` del despliegue: completar `/opt/core-force-mail/app/.env` con la configuración de
   `.env.example` sin copiarlo encima. Del perfil: `PUBLIC_BASE_URL=https://app.<dominio>`,
   `EDGE_REQUIRE_CLOUDFLARE=true`, `EDGE_NETWORK_SUBNET` (fuera de `172.30.0.0/16`, el pool de
   `daemon.json`), `MAIL_SSL_VOLUME` si el proyecto de los motores no se llama `mail`, y
   `INTERNAL_TLS_DIR` si no es el de por defecto. `REDIS_TLS*`, `DB_UPSTREAM_*` y
   `POSTGRES_DIRECT_*`, `MAIL_AUTH_TLS_CERT`/`MAIL_AUTH_TLS_KEY` y `WEBMAIL_TLS_CA_FILE` los fija el perfil. Secretos: ver Pendientes.
4. Motores y certificado público, antes que la plataforma (crean la red `mail-engines` y el volumen
   `<MAIL_PROJECT>_ssl-vol` que ella exige). En el `.env` del paso 3: `MAIL_DB_HOST=postgres`,
   `ACME_DNS_CHALLENGE=y`, `ACME_DNS_PROVIDER=dns_cf` y `ADDITIONAL_SAN=app.<dominio>`. Desde el PC:

   ```bash
   DEPLOY_HOST=<srv> DEPLOY_USER=deploy DEPLOY_SSH_KEY=~/.ssh/<llave> \
     ./scripts/deploy-mail.sh unbound-mail redis-mail clamd-mail olefy-mail dovecot-mail rspamd-mail \
       postfix-tlspol-mail postfix-mail acme-mail netfilter-mail dockerapi-mail
   ```

   `watchdog-mail` solo si se usa (`USE_WATCHDOG=y`). Las credenciales DNS-01 de acme no pasan por
   el despliegue: van en el volumen `<MAIL_PROJECT>_acme-conf-vol` (`/etc/acme/dns-01.conf`, 0600),
   escritas por la entrada estándar. `acme-mail` deja primero el certificado provisional de
   `ssl-example` y espera a la base, que llega con el paso 5; al emitir el definitivo, el borde lo
   recarga solo. En Cloudflare, `app.<dominio>` con proxy y modo SSL «Full (strict)», y
   `mail.<dominio>` sin proxy (DNS only). Despliegues siguientes: `./scripts/deploy-mail.sh` sin
   argumentos recrea solo los motores con cambios.

   Migración de motores levantados a mano (imágenes `mail-<motor>:latest`, red creada a mano con las
   etiquetas de compose, configuración en `/opt/core-force-mail/mail-src/deploy/mail`): el primer
   despliegue va con la lista explícita, porque de `:latest` no se sabe el commit. No cambian el
   proyecto, la ruta, los volúmenes (buzones, cola de Postfix, certificados) ni la red, que Compose
   acepta porque lleva sus etiquetas sin huella de configuración (`test-deploy-mail.sh` lo prueba).
   Sí se recrea cada contenedor, porque cambia su imagen: cada motor cae unos segundos, de uno en uno.
   Durante el de Postfix, los servidores remitentes reintentan (SMTP entrega con reintentos, nada se
   pierde) y la cola sigue en su volumen; durante el de Dovecot, los clientes IMAP reconectan. Para
   acotarlo, en horario de poco tráfico y en tandas: primero los que no cortan correo
   (`unbound-mail olefy-mail clamd-mail postfix-tlspol-mail acme-mail dockerapi-mail`), luego
   `redis-mail rspamd-mail netfilter-mail` y al final `dovecot-mail` y `postfix-mail`, cada tanda
   esperando la anterior. Las imágenes `mail-*:latest` quedan sin uso y se retiran a mano cuando todo
   corre con etiqueta (`docker image rm mail-<motor>:latest`).
5. Primer despliegue, sin AWS (el transporte cae a `save`, que etiqueta por commit igual que ECR):

   ```bash
   DEPLOY_HOST=<srv> DEPLOY_USER=deploy DEPLOY_SSH_KEY=~/.ssh/<llave> \
     ./scripts/deploy-ecr.sh <todos los servicios>
   ```

   Tras el rsync, `perfil-despliegue.sh` confirma `selfhosted`, la CA y los directorios de certificado de `postgres`, `redis` y `mail-auth` (sin ellos se detiene
   aquí); se levantan y esperan sanos `postgres-primary`, `redis`, `pgbouncer` y `nats`; se recrean los
   servicios y se esperan; y al final `edge-proxy`, también esperado. Cada `docker compose` va por
   `with-secrets.sh` con `-f docker-compose.yml -f docker-compose.selfhosted.yml`. En despliegues
   siguientes un cambio en `selfhosted/redis` recrea Redis, uno en `selfhosted/edge` recrea el
   borde y uno en `selfhosted/postgres` recarga `pg_hba` con SIGHUP, sin reiniciar la base.
6. Respaldos (sección 6): comprobar los temporizadores
   (`systemctl list-timers 'core-force-mail-*'`) y, si hay destino externo, crear
   `/opt/core-force-mail/env/backup.env` (0600 del usuario de despliegue) con
   `BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY` y `BACKUP_ENCRYPTION_PASSPHRASE`, y
   declarar `BACKUP_S3_BUCKET`, `BACKUP_S3_ENDPOINT` y `BACKUP_S3_REGION` en el `.env`. Primera
   corrida a mano, como el usuario de despliegue: `ops/backup/backup-tenants.sh`,
   `ops/backup/backup-mail-volumes.sh` y `ops/backup/verify-restore.sh`. La frase de cifrado se
   guarda también fuera del servidor: sin ella, el histórico externo no se descifra.
7. Renovaciones:
   * TLS interno: el timer diario ejecuta `internal-tls.sh --renovar --recargar`, que reemite con la
     misma CA lo que caduca en `INTERNAL_TLS_RENEW_DAYS` (30) y lo aplica sin reiniciar: SIGHUP a
     Postgres y `CONFIG SET tls-cert-file` a Redis; mail-auth mira sus ficheros cada 15 s y sirve
     el par nuevo en cuanto casa (mientras no casa, el anterior). No se reinicia a propósito: con
     mail-auth caído Dovecot responde fallo de contraseña y vacía su caché de ese usuario. Estado: `internal-tls.sh --comprobar`. La CA
     (10 años) no se rota sola: rotarla es crear una nueva, reemitir, y recrear `pgbouncer`,
     `redis`, `postgres-primary` y todos los servicios Go (el webmail carga la CA al arrancar) en
     la misma ventana.
   * Certificado público: lo renueva `acme-mail`; el borde lo detecta y recarga nginx.
   * Rangos de Cloudflare: `ops/security/edge-cloudflare-ips.sh --comprobar` periódicamente.

### Conexiones largas de los motores por PgBouncer

En el perfil autoalojado Dovecot y Postfix leen el directorio de la celda por PgBouncer y mantienen
sus conexiones abiertas mucho mas que los 600 s de `client_idle_timeout`. Pasado ese tiempo
PgBouncer las cerraba y el primer inicio de sesion IMAP (o la primera entrega) tras un rato sin uso
fallaba con `FATAL: client_idle_timeout` en el registro de Dovecot, aunque la contrasena se aceptara
(2026-09-19, probado con un buzon real). `PGBOUNCER_CLIENT_IDLE_TIMEOUT` (segundos, entero, `0` lo
desactiva; por defecto 600, como antes) lo fija `pgbouncer/entrypoint.sh`, y `docker-compose.selfhosted.yml`
lo pone a `0`. `check-pgbouncer-entrypoint.sh` prueba el valor por defecto, el `0` y los no validos, y
`check-selfhosted-profile.sh` exige el `0` del perfil.

### Arranque tras un reinicio del servidor

El demonio de docker corre con `live-restore` (`ops/server-template/config/daemon.json`), y al
arrancar solo levanta los contenedores con la politica `always`; los `unless-stopped` del compose
base se quedan parados. En un reinicio real (2026-09-17) volvieron los motores de `deploy/mail`
(que ya usaban `always`) y no `postgres-primary`, y con la base caida el resto de la plataforma
quedo reintentando. El perfil fija `restart: always` para todos sus servicios (ancla `x-reinicio`),
y `ops/scaffold/check-selfhosted-profile.sh` falla si un servicio del compose base se queda sin
ella. Tras un reinicio, comprobar con `ops/maintenance/esperar-sanos.sh --proyecto app`.

### Red y cortafuegos

No hay Security Group. UFW solo gobierna el host (SSH); los puertos que publica Docker no pasan
por UFW. Públicos quedan 80 y 443 (`edge-proxy`, `EDGE_BIND_ADDRESS`) y los de correo de
`deploy/mail`. Postgres, Redis, NATS, el gateway y los servicios solo en `127.0.0.1`. Si el
proveedor ofrece cortafuegos externo, limitar 80 y 443 a los rangos de Cloudflare cierra el
acceso directo también en la red.

### Consumo de memoria

En reposo, medido en la prueba: Postgres 51 MiB, Redis 5 MiB, PgBouncer 2,4 MiB, `edge-proxy`
11 MiB con 12 trabajadores (en 2 vCPU son 2 y ronda 4 MiB), gateway y access-control 7 MiB cada
uno. El TLS añade poco: la caché de sesiones del borde reserva 5 MiB compartidos y cada conexión
TLS de Postgres o Redis unas decenas de KiB. Lo que cuenta en 3,8 GB son los techos: `shared_buffers`
de 512 MB y `max_connections=200` de Postgres, `maxmemory 512mb` de Redis, el tmpfs de cuerpos del
borde (hasta `EDGE_BODY_TMPFS_SIZE`, 256 MiB, dentro de `EDGE_MEMORY_LIMIT`, 384 MiB) y los motores
de correo (ClamAV sola supera 1 GB al cargar firmas). Sumados rozan la RAM: el swap de 4 GB evita
el OOM, pero con ClamAV cargando la latencia se degrada.

### Presupuesto de recursos

`docker-compose.selfhosted.yml` da a cada servicio Go (22) `mem_limit`, `GOMEMLIMIT` (80 % del techo, para que el
recolector actúe antes que el OOM del cgroup), `pids_limit: 256`, `user: 65532:65532`, `read_only: true`,
`cap_drop: [ALL]` y `stop_grace_period: 40s` (`pkg/server` espera hasta 30 s a las peticiones en vuelo tras SIGTERM; con
los 10 s por defecto de compose el kernel las mataba a medias en cada despliegue). Ninguno escribe en disco (imágenes
`scratch`, sin `CreateTemp` ni `WriteFile`), así que no hay tmpfs. `ops/scaffold/check-selfhosted-profile.sh` falla si
un servicio Go pierde alguno de ellos o si la suma de `mem_limit` del perfil pasa de 6144 MiB. La rotación de logs la da
el demonio (`ops/server-template/config/daemon.json`: `json-file`, 20 MiB x 5, comprimido), no cada servicio. No se fija
límite de CPU: en 4 vCPU compartidas con la base y los motores, un tope por contenedor solo convierte una ráfaga en
latencia (`mail-migration-runner` conserva el suyo).

| Techo | Servicios | Por qué ese valor |
|---|---|---|
| 128 MiB | access-control, analytics, automations, billing, domain-service, identity, mail-directory, organization, reputation, scheduler, suppression, templates | En reposo miden 6 a 15 MiB (medido con `docker stats` sobre la pila local, 13 h de uso); 10x de margen |
| 192 MiB | audit, campaigns, gateway, mail-auth, mail-migration, transactional | Cuerpos de petición y ráfagas de conexiones (gateway, mail-auth); lotes de campañas y de correo transaccional |
| 256 MiB | contacts, mail-dav | Importación de contactos; un `sync-collection` inicial materializa el calendario entero (medido: 20000 eventos de 350 bytes, +26 MiB de heap; la respuesta la acota `MAIL_DAV_MAX_RESPONSE_BYTES`, 16 MiB, y el buzón `MAIL_DAV_MAX_MAILBOX_BYTES`, 64 MiB) |
| 384 MiB | mail-security | Recibe de Rspamd el mensaje entero en `/pipe` (`MAIL_QUARANTINE_MAX_BODY_MB`, 50 por defecto) |
| 512 MiB | webmail | Mensajes de hasta 25 MiB que se leen, se codifican en MIME y se envían |

Resto de la plataforma: `pgbouncer` 128 MiB (medido 2,5 MiB), `redis` 640 MiB (`maxmemory 512mb` más la copia del AOF; medido
4 MiB), `nats` 256 MiB (medido 13 MiB), `web` 64 MiB (medido 11 MiB), `edge-proxy` 384 MiB. Suma de techos de la
plataforma: **5568 MiB**. Pila de observabilidad: Prometheus 768, Grafana 384, Loki 512, Promtail 256, Alertmanager 128,
node-exporter 64 y docker-socket-ro 64 = 2176 MiB.

**Lo que no lleva límite, y por qué.** Postgres (`shared_buffers` 512 MB más hasta 197 procesos de servidor: un OOM de
cgroup sobre la base es peor que la presión de memoria del host), ClamAV (supera 1 GiB al cargar firmas, medido 1007
MiB, y en cada recarga de firmas llega a casi el doble: un límite estrecho lo mata durante la recarga y el correo entrante
se detiene), y el resto de los motores (Rspamd 71 MiB, Dovecot 13, Postfix 16, tlspol 17, Olefy 11, Unbound 13; Postfix
ejecuta además el `queue-agent`). `mail-migration-runner` conserva su tope de 1 GiB y 1 CPU.

**La suma supera la RAM y se decide así.** Techos de la plataforma (5,4 GiB) + observabilidad (2,1) + ejecutor de migración
(1,0) + Postgres (hasta ~1,5) + ClamAV (1,1 a 2,2) + motores (~0,2) son 11 a 12 GiB frente a 7,9 GiB de RAM y 4 GiB de
swap. No es un defecto: un techo no reserva nada, y el uso real es la suma de los usos (medido en producción el 2026-09-21:
37 contenedores con 4,9 GiB libres). Lo que el techo compra es aislamiento: una fuga o una petición enorme en un servicio
lo mata a él (y `restart: always` lo levanta) en vez de arrastrar a Postgres. Lo que hay que vigilar es que ningún
servicio toque su techo en estado estable: `ProcesoMatadoPorFaltaDeMemoria` (`node_vmstat_oom_kill`) avisa de cualquier
muerte por memoria del host, y tras el primer despliegue conviene mirar `docker stats --no-stream` con carga real.
**Estos límites salen de la medición en reposo y del análisis del código, no de carga real en el servidor**: si uno se
queda corto, sube su `mem_limit` y su `GOMEMLIMIT` en el perfil (los candidatos son webmail, mail-security y mail-dav, los
que reciben cuerpos grandes). No impiden el arranque: un servicio Go arranca con 6 a 15 MiB.

### Presupuesto de conexiones a Postgres

`max_connections=200` (3 reservadas para superusuario) y unos 12 huecos para lo que no pasa por el pooler (el migrador de
`organization` por `POSTGRES_DIRECT_*`, `pg_dump` del respaldo, `psql` del operador) dejan **185** para PgBouncer, que corre
en `pool_mode = transaction`. Un pool de PgBouncer es un par (base, usuario), y cada servicio de empresa tiene su usuario:
con `default_pool_size=20` y 13 servicios, UNA base podía pedir 260 conexiones. Medido contra un PgBouncer 1.25 real y un
Postgres de 200 (12 usuarios x 8 bases, 6 clientes concurrentes por par, consultas de 50 ms):

| Configuración | Conexiones de servidor con carga | Al quedar ociosas (`server_idle_timeout`) |
|---|---|---|
| Anterior (`min_pool_size=1`, sin techo por base) | 197 de 200: Postgres al límite | 96 (una por par que alguna vez tuvo tráfico) |
| `min_pool_size=0`, sin techo por base | 197 | 0 |
| `min_pool_size=0`, `max_db_connections=16` | 128 (= 8 bases x 16) | 0 |

Por eso `pgbouncer/pgbouncer.ini.template` fija ahora `min_pool_size=0` (con 1, las conexiones ociosas crecían como
servicios x empresas: 15 empresas agotaban `max_connections` sin hacer nada) y `max_db_connections=40`
(por base, sumados todos los usuarios). Regla: **bases con carga a la vez x `max_db_connections` <= 185**; hoy son 4
(registro, celda, `platform` y una empresa) = 160. Al pasar de 4 bases saturadas a la vez, o se sube `max_connections`
(cada conexión inactiva cuesta 5 a 10 MiB) o se baja el tope; lo habitual es lejos de eso, porque una conexión solo se
ocupa mientras dura una transacción y la carga real de un pool es de unas pocas. Las peticiones de más esperan en la cola
de PgBouncer (`query_wait_timeout` 120 s): no fallan, se enlentecen, y `EsperaDeConexiones` lo avisa. `max_client_conn`
sube a 4000 (era 1000): cada servicio abre hasta 10 conexiones cliente al registro y otras 10 por empresa (`pkg/db`), y
22 servicios superaban 1000 con 6 empresas saturadas; un cliente en espera cuesta unos KiB. `pgbouncer` recibe
`ulimits.nofile` 65536.

**Exige recrear PgBouncer** (`up -d --force-recreate pgbouncer`): el despliegue solo le hace `RECONNECT` y la plantilla se
renderiza al arrancar. Corta las conexiones un instante y los servicios reintentan; hacerlo fuera de hora punta.

### Réplicas: qué escala y qué no

Ningún servicio corre hoy con más de una réplica; esto es lo que se comprobó en el código antes de que haga falta.

| Servicio | ¿Réplicas? | Estado en el proceso y qué pasa con N réplicas |
|---|---|---|
| gateway | Sí | Limitadores compartidos en Redis (`gateway:api`, `gateway:auth`) y el contador de lecturas del detector de extracción masiva (`gateway:exfil`: por usuario, ventana anclada en la primera lectura y TTL en Redis). `EXFIL_READ_THRESHOLD` y `EXFIL_WINDOW_MIN` valen para el conjunto de réplicas y la alerta sale una sola vez por ventana, porque sale del `INCR` atómico. Si Redis cae cada réplica cuenta aparte (`LimitadorSinRedis`, etiqueta `limiter`): los limitadores no dejan de limitar y el detector no deja de contar, pero el total de un usuario que reparte sus lecturas queda dividido por N hasta que Redis vuelva. El detector nunca bloquea, así que un Redis caído no corta ninguna lectura. Cuesta un viaje a Redis por lectura de datos (tope de 250 ms y 5 s de espera tras un fallo, como los limitadores). La caché de permisos vive 60 s por réplica |
| identity, access-control, organization, billing, templates, campaigns, automations, analytics, suppression, reputation, domain-service, contacts, transactional | Sí | Sin estado propio. Los barridos periódicos toman `db.TryLeaderLock` (un `pg_try_advisory_xact_lock` con la transacción abierta: si el líder muere, la conexión se cierra y el cerrojo se suelta solo). Sus limitadores en memoria multiplican el cupo por N, pero detrás está el del gateway |
| audit | Sí, con condición | Cada escritura de cadena toma un candado de transacción por base de empresa (serializa entre réplicas); el anclaje y el barrido de verificación tienen líder. `AUDIT_HASH_KEY` idéntica en todas y recreadas a la vez (una sin llave escribiría versión 1 tras filas de versión 2). Una verificación de cadena por empresa **entre réplicas** (índice único en la base); el tope de 4 verificaciones a la vez es **por proceso**, no global. Los eventos del bus entran por consumidores durables push de JetStream (`audit-<subject>`): la segunda réplica no puede atarse y reintenta con aviso hasta que la primera se vaya (queda en espera, sin daño, y toma el relevo); un evento publicado con audit caído se aplica al volver |
| mail-migration | Sí | El conjunto de empresas con trabajo (`hinted`) es una optimización por réplica: el reclamo es `FOR UPDATE SKIP LOCKED`, sin doble entrega (probado con 8 ejecutores concurrentes sobre 24 trabajos, ninguno repetido) y lo que una réplica no conoce lo encuentra el recorrido completo cada `MAIL_MIGRATION_SWEEP_INTERVAL`. El consumidor de `mail.mailbox.deleted` es un push durable: la segunda réplica no puede atarse y reintenta con aviso (queda en espera, sin daño) |
| mail-dav | Sí | Sin caché. Igual que mail-migration con su consumidor durable. El freno de fuerza bruta de la contraseña es el de mail-auth, en Redis (por IP real) |
| mail-auth, webmail | Sí | Sesiones y throttle en Redis. webmail: consumidor durable como los anteriores |
| mail-directory, mail-security | Con cuidado | Cerrojos de líder para el repaso DKIM, los avisos de cuarentena y el relé de la outbox de celda. El monitor de la cola de Postfix corre en CADA réplica (cada una consulta al `queue-agent`, que atiende de una en una): N réplicas son N consultas por intervalo. No se probó con dos |
| scheduler | Sí | Barrido bajo líder |
| postgres, redis, nats, pgbouncer, edge-proxy | No en este perfil | Instancia única en el host; NATS con `Replicas: 1` |

### Eventos: retención y tamaño

Todo stream de aplicación (`pkg/events.EnsureStream`) retiene 7 días **y** como máximo 1 GiB (`MaxBytes`; al llegar se
descartan los mensajes más viejos: llegar ahí es un incidente, no el funcionamiento normal). Se reconcilia en cada
arranque también sobre los streams que ya existían (comprobado contra un NATS 2.10 real: uno creado sin tope quedó en
1073741824 bytes). `EVENTS_DLQ` (30 días) se crea con 256 MiB. Un consumidor lento o un mensaje venenoso no bloquea el
stream: cada consumidor durable tiene 20 entregas y 90 s de espera de confirmación, las entregas atrasadas no impiden
las siguientes, y al agotarse pasa a `EVENTS_DLQ`. Los eventos críticos salen por outbox (`platform.event_outbox`); si una
fila agota sus 50 intentos ya no se reintenta, y desde ahora eso se cuenta (`outbox_events_exhausted_total`) y avisa
(`EventosDeOutboxAgotados`). El healthcheck de NATS pasó de `nats-server --help` (que sale bien aunque el servidor esté
caído) a `/healthz` del monitor, de modo que `depends_on: service_healthy` espera de verdad.

Un handler que hace `panic` ya no tumba el proceso (nats.go entrega en su propia goroutine): `pkg/events` lo recupera, deja
el mensaje sin confirmar y sigue el camino normal hasta `EVENTS_DLQ`; antes un evento venenoso reiniciaba el servicio en
cada reentrega.

### Pendiente de la revisión de robustez (2026-09-21)

Lo medido o leído en el código que NO se cambió, con su razón:

* **`audit` y los eventos publicados con el servicio caído**: resuelto (2026-09-21, ADR 0006 sección 7): consumidores
  durables por subject, idempotentes por id de evento, con ack tras persistir y DLQ. Queda un hueco de una sola vez en
  el despliegue que lo introduce (`DeliverNew`, ver "Verificación en segundo plano y eventos por consumidores durables"
  en la sección 2) y la retención de 7 días de los streams.
* **Recuentos `count(*)` por página** (bitácora de `audit`, listado de `mail-migration`): eran O(n) por petición, 17 a 21 ms
  con 100000 filas de una empresa. Ahora el total se cuenta solo hasta `db.PageCountCap` (10000 filas,
  `SELECT count(*) FROM (SELECT 1 ... LIMIT 10001)`): bajo el tope es exacto y la petición no cambia; por encima devuelve el
  tope con `meta.total_capped: true` y la web muestra "Mas de 10000 registros". Con 100000 filas, mediana de 15 medidas en
  la prueba de integración: bitácora 8,1 ms exacto contra 0,84 ms acotado; `mail-migration` 21,3 ms contra 3,3 ms. Efectos:
  `total_pages` en un listado acotado es el de las primeras 10000 filas (`ceil(10000 / per_page)`), así que la paginación no
  llega más allá; para lo antiguo se estrecha el filtro (fechas, módulo, usuario). Una petición con `page` mayor sigue
  respondiendo. Si hiciera falta recorrer todo, paginar por clave (cursor), no por desplazamiento. Sin cambiar quedan los
  listados de eventos de seguridad de `audit` y los demás servicios, que cuentan exacto.
* **Verificación de la cadena de auditoría**: resuelto en su modelo (2026-09-21, ADR 0006 sección 6): en segundo plano,
  reanudable, cancelable, incremental y con barrido periódico opcional. Queda **medirla en el servidor con una empresa
  grande** (las pruebas recorren miles de filas; la única medida es 100000 filas a ~227000 filas/s) antes de fijar
  `AUDIT_INTEGRITY_INLINE_MAX_ROWS` y el intervalo del barrido, y el cupo global entre réplicas (hoy 4 por proceso).
* **JetStream sin tope global** en el servidor NATS (solo por stream, 1 GiB) y sin exportador de NATS: el tamaño de los
  streams no está en Prometheus. Añadir `max_file_store` al servidor y el exportador oficial.
* **Sin métricas por contenedor** (cAdvisor): el aviso de un servicio cerca de su techo de memoria es `ProcesoMatadoPorFaltaDeMemoria`,
  después del OOM; antes solo se ve con `docker stats`.
* **`mail-security`**: el monitor de la cola de Postfix hace que el `queue-agent` cuente TODA la cola de `postqueue -j` en
  cada intervalo. Medido (`parseListing`, 1,7 µs y 650 B por mensaje): 100000 mensajes son 169 ms y 65 MiB transitorios
  del agente, además de lo que tarde `postqueue`; el plazo del agente es de 20 s. No es un problema con las colas
  esperadas; con colas de cientos de miles, contar por directorio (`qshape`) en vez de listar.
* **Respaldos**: las claves del almacén (`MAIL_ENCRYPTION_KEY`, `AUDIT_HASH_KEY`, `JWT_SIGNING_KEY`,
  `MAIL_LINK_SIGNING_KEY`) y los roles de Postgres no van en ningún respaldo y su copia externa es manual
  (`ops/backup/README.md`, "Lo que el respaldo no trae"). `verify-restore.sh` avisa de la cadena de auditoría, pero no puede
  verificarla sin la llave.
* **`idle_in_transaction_session_timeout` no debe fijarse en Postgres**: `db.TryLeaderLock` mantiene una transacción
  abierta a propósito durante todo el barrido (el cerrojo de líder es de transacción); un tope bajo soltaría el cerrojo a
  mitad del trabajo y habría dos líderes.
* **`mail-dav` autentica cada petición contra `mail-auth`** (bcrypt de coste 10, decenas de ms de CPU por petición): es lo que
  da la revocación inmediata. Con miles de dispositivos sincronizando cada pocos minutos es una fracción de un núcleo; si
  creciera, una caché de aciertos de segundos, con la revocación como límite de su TTL.

### Estado verificado del servidor de producción (2026-09-20)

Lo que se comprobó de punta a punta con un buzón real, y las trampas que salieron al hacerlo:

* **Nombres.** La plataforma vive en `email.<dominio>` y el correo en `mx.<dominio>` (HELO, PTR y certificado
  son ese mismo nombre); el SPF de la plataforma, en `spf.<dominio>` y el SPF del HELO en `mx.<dominio>`
  (`v=spf1 a -all`). La plataforma no admite dar de alta como dominio de una empresa su propio dominio
  (ni el de un subdominio suyo): las empresas usan dominios distintos.
* **Entrada.** SMTP en el 25 con greylisting (el primer intento de un remitente nuevo recibe 451; los servidores
  reales reintentan), IMAP en el 993 y envío autenticado en el 587. Un remitente en una lista negra o de un dominio
  que declara `nullMX` se rechaza: es el filtro, no un fallo.
* **Salida.** Firmada con DKIM (`d=<dominio de la empresa>`, selector `cfm<aaaamm>`), SPF y DNS inverso en `pass`
  para un verificador externo (Port25) y 10/10 en mail-tester. El puerto 25 de salida hay que comprobarlo en un
  servidor nuevo: hacia Gmail y Outlook conectó; Yahoo no respondió.
* **DNS inverso.** Lo cambia el cliente en el panel del proveedor (Netcup: Server Control Panel, IPv4, Reverse
  DNS) a `mx.<dominio>`; tarda unos minutos en publicarse. No se pone en la fila IPv6 si el servidor no tiene
  IPv6 ni un AAAA para ese nombre.
* **Verificación de dominios.** `MAIL_DNS_RESOLVER` apunta a un resolver público (por defecto `1.1.1.1:53` en el
  perfil): el del proveedor cachea las respuestas negativas y dejaba un dominio recién publicado como fallido.
* **Cortafuegos heredado.** Un servidor reutilizado puede traer reglas de otra plataforma (`cf-firewall` mandaba el 80 y
  el 443 a una lista de Cloudflare y las reponía cada día). Revisar `iptables -S DOCKER-USER` y las unidades de
  systemd antes de desplegar.
* **Reinicio.** Ver "Arranque tras un reinicio del servidor": todo el perfil usa `restart: always`.
* **Gestor de cola de Postfix.** Activo (2026-09-21): `QUEUE_AGENT_API_KEY` (32 bytes aleatorios) en el `.env` del
  servidor, 0600, que es donde `load.sh` toma los secretos mientras no haya almacén, la misma para `mail-security` y
  `postfix-mail`; el 8590 solo existe en la red `mail-engines` y desde fuera no conecta. El superadmin recibe 200 en
  `/api/v1/mail-security/queue` y un administrador de empresa 403. Para rotarla: nueva clave en el `.env`, recrear
  `mail-security` y después `postfix-mail`.
* **Visor de registros y lectura del antispam** (`docs/adr/0009`, 2026-09-21, sin desplegar). Servicio nuevo
  `observability` (imagen nueva, que el despliegue construye como a los demás; entra en `routes.json`, así que
  hay que recrear también el `gateway`) con `LOKI_URL`, que el perfil autoalojado fija a `http://loki:3100`; sin la
  pila de observabilidad levantada la pantalla Registros responde 503 `NOT_CONFIGURED` y nada más cambia. Migraciones
  del registro `036` (permiso `mail_security/rspamd/read`) y `037` (`observability/logs/read`), las dos de plataforma.
  Para la pantalla Antispam hace falta la contraseña del controller de Rspamd en los dos lados: en
  `deploy/mail/rspamd/override.d/worker-controller-password.inc` del servidor (fichero ignorado por git;
  `password = "<hash>";` con el hash de `rspamadm pw`) y `RSPAMD_CONTROLLER_PASSWORD` en `mail-security`; después
  recrear `rspamd-mail` y `mail-security`. Sin ella la pantalla responde 503 `NOT_CONFIGURED` y el entrenamiento desde
  la cuarentena sigue igual de desactivado que hasta ahora. Pendiente (P): mover `RSPAMD_CONTROLLER_PASSWORD` del
  `.env` al almacén de secretos con su fila en `reparto.tsv`.

### Correo del sistema (recuperacion de contrasena)

El correo que envia la propia plataforma sale por `transactional` y por Amazon SES, como el resto del transaccional
(no por el Postfix corporativo: no se mezclan reputaciones). Hace falta, y solo lo primero lo puede dejar listo quien
opera el servidor:

1. Un dominio de remitente de la plataforma, verificado en la empresa de plataforma: el superadmin lo da de alta
   (`POST /api/v1/domains`, proposito `sending`) y publica sus registros. En produccion `avisos.core-force.com`
   (2026-09-21, un subdominio, para no tocar el DNS de `core-force.com` que usa otro sistema);
   `PLATFORM_FROM_EMAIL=no-reply@avisos.core-force.com`.
2. `PLATFORM_TENANT_ID` con el id de la empresa de plataforma en el `.env` de `identity`. Sin el, el correo sale
   como la empresa del usuario y transactional responde 422 `SENDING_DOMAIN_NOT_VERIFIED`: comprobado en produccion
   el 2026-09-21 con la recuperacion de contrasena de `it@mentorenergy.uk`.
3. Credenciales de SES (un usuario IAM solo con `ses:SendEmail` y `ses:SendRawEmail`), la identidad del dominio
   verificada en SES y la cuenta fuera del sandbox. Sin ellas el mensaje se acepta y encola, pero SES lo rechaza.


### Informes DMARC

Todos los dominios de las empresas publican `rua=mailto:<MAIL_DMARC_RUA>`. Para que esos informes lleguen hace
falta, en este orden y una sola vez por plataforma (V en producción, 2026-09-20):

1. Un dominio de la plataforma que reciba correo, distinto del de la web. En producción `dmarc.core-force.com`. El
   superadmin lo da de alta en Dominios (`POST /api/v1/domains`, propósito corporativo): es el único que puede dar
   de alta un dominio de la plataforma, y pasa por la verificación normal (TXT de propiedad, MX, SPF, DKIM).
2. Sus registros DNS, que devuelve el alta: MX a `MAIL_MX_HOSTNAME`, SPF, DKIM y el TXT de propiedad.
3. El TXT de autorización `*._report._dmarc.<dominio>` con `v=DMARC1`. Sin él, Google y Microsoft no envían
   informes a un dominio distinto del que los pide (RFC 7489, 7.1); el comodín vale para todas las empresas.
4. Un buzón real en ese dominio (`reportes@`), con `smtp_access` apagado porque solo recibe. El superadmin lo lee
   por `/webmail`; la contraseña se genera aleatoria y se guarda fuera del repositorio.
5. `MAIL_DMARC_RUA` con esa dirección en el `.env` y recrear `domain-service`. Las empresas con Cloudflare la
   toman al publicar de nuevo su DNS; las de DNS manual la ven en los registros del dominio y la copian ellas.

Con la dirección en un dominio sin MX o sin ese TXT, el correo funciona igual pero los informes se pierden en
silencio: es lo que ocurría con `dmarc@core-force.com`.

### MTA-STS y TLS-RPT de las empresas (código hecho, sin activar)

`mail-directory` guarda la política MTA-STS de cada dominio y la sirve en `GET /public/mail-directory/mta-sts/{cell}/{dominio}`;
`domain-service` pide a los dominios que reciben por la celda los TXT recomendados `_mta-sts` y, si está configurada,
`_smtp._tls` (`rua=mailto:<MAIL_TLSRPT_RUA>`). Dos variables del `.env` intervienen y ninguna cambia lo que se despliega
hoy: `MAIL_MX_HOSTNAME` (ya obligatoria) es ahora también obligatoria para `mail-directory`, que no arranca sin un nombre
válido y la usa como único `mx:` de toda política y como el MX que exige antes de pasar un dominio a `enforce`; y
`MAIL_TLSRPT_RUA` es opcional (vacía no publica `_smtp._tls`). `MAIL_DNS_RESOLVER` lo lee también `mail-directory` (en
el perfil autoalojado, con el mismo `1.1.1.1:53` por defecto que `domain-service`) para comprobar esos MX.

Nada protege a nadie hasta que el borde atienda `mta-sts.<dominio>` (CNAME del cliente hacia la plataforma, certificado por
`acme` con HTTP-01 sin Cloudflare por medio, SNI en `selfhosted/edge`, y la celda en la ruta si hay varias): es la decisión
de quien opera el servidor descrita en `docs/adr/0003-mta-sts-y-tls-rpt-entrantes.md`. Mientras tanto, activar MTA-STS en un
dominio solo publica una política que nadie descarga; no hay que pasarlo a `enforce`. Y como con DMARC, la dirección de
`MAIL_TLSRPT_RUA` debe ser un buzón real de un dominio de la plataforma que reciba correo antes de configurarla.

### Riesgos y pendientes

* Los buzones se archivan con Dovecot en marcha: un mensaje que cambie de carpeta durante el
  archivado puede faltar en esa copia (está en la siguiente). Un archivo sin ese riesgo exige
  `doveadm backup` buzón a buzón o parar Dovecot; queda pendiente.
* La cola de Postfix (`postfix-vol`) y el Redis de los motores no se respaldan: lo encolado se
  reintenta desde el emisor y el bayes de Rspamd se reaprende.
* Secretos: RESUELTO (2026-09-21, `docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`). El
  almacén del perfil ya no es una decisión pendiente: es `/opt/core-force-mail/secrets/store.json.gpg`, un
  fichero cifrado con gpg simétrico (mismo patrón que `ops/backup/destino-externo.sh`) y una frase
  en `/opt/core-force-mail/secrets/passphrase` (0600, fuera del `.env` y fuera del árbol que
  sincroniza el despliegue). `with-secrets.sh` sigue siendo el único camino; `fetch-secrets.sh` ya
  no depende de ningún CLI ni rol de AWS. Falta migrar el `.env` del servidor real al almacén con
  `push-secrets.sh` (sección 2, "Migrar un servidor que hoy no tiene almacén"); hasta entonces,
  `load.sh` sigue con su respaldo documentado al `.env`. Los secretos del respaldo no pasan por
  este almacén a propósito: siguen en `BACKUP_SECRETS_FILE`, que ningún contenedor recibe
  (`secret-keys-backup.txt`).
* `sslmode=prefer` de las conexiones directas de `pkg/config` cifra pero no verifica el
  certificado; en este perfil el camino va por la red interna de Docker del propio servidor.
  Verificarlo pide soportar `sslrootcert` en `pkg/config`.
* La clave de la CA vive en el servidor (la necesita la renovación): quien tenga root puede emitir
  certificados internos, pero también puede leer los datos.
* Dovecot no verifica a mail-auth. `passwd-verify.lua` pasa `insecure = true`, que LuaSec no
  reconoce: lo que rige es su `verify = "none"` por defecto (LuaSec 1.3.2 de Alpine 3.21,
  comprobado en `ssl/https.lua`). Se podría pasar `verify = "peer"` con `cafile` a la CA interna
  montada en Dovecot, sin tocar Dovecot por dentro, pero LuaSec no comprueba el nombre del
  certificado: aceptaría cualquiera de la CA interna (también el de Postgres o Redis), y el cambio
  alcanza a desarrollo y al e2e, que usan otras CA. Queda pendiente con su prueba en `deploy/mail`.
  El tráfico va por la red `mail-engines` del propio servidor; el webmail sí verifica.
* mail-auth sin `MAIL_AUTH_TLS_CERT`/`MAIL_AUTH_TLS_KEY` arranca con un autofirmado y solo lo
  avisa, también en `production`. En este perfil `check-selfhosted-profile.sh` impide perderlos; en
  otro despliegue el síntoma es que el webmail no autentica a nadie.
* `release.yml` aplica el perfil, pero entra por SSM y publica en ECR: en un servidor sin AWS el
  camino es `scripts/deploy-ecr.sh` con `save`.
* Motores: la copia de `deploy/mail` en el servidor es un bind mount de ida y vuelta. rspamd
  reescribe `local.d`, `override.d`, `plugins.d` y `custom/*` con el uid de su contenedor y deja
  esos directorios sin permiso de escritura para el usuario de despliegue; Postfix y Dovecot dejan
  ahí sus ficheros generados (`sql/*.cf` 640 `root:postfix`, `main.cf`, `sni.map`…). El 2026-09-17
  eso detuvo un despliegue de motores en la sincronización, con decenas de `tar: … Cannot open:
  File exists` y `Cannot utime` (antes de recrear nada: producción quedó intacta). Desde entonces
  `scripts/deploy-mail.sh` extrae y borra **dentro de un contenedor efímero como root** —docker es
  lo único que el usuario de despliegue tiene de más, no hace falta `sudo`— y solo toca las rutas
  del archivo: lo generado no está versionado, así que no se reemplaza ni cambia de dueño. Detalle
  y la única excepción a vigilar (los mapas de `rspamd/custom/`, si algún día los escribe un
  servicio) en `deploy/mail/README.md`, «Despliegue en un servidor», paso 5.
* La configuración sincronizada llega también a los motores que no se recrean: el despliegue lo
  avisa, y conviene desplegarlos en la misma ventana.
