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
  detrás de PgBouncer, ElastiCache, SES, S3, Secrets Manager. Servidor de aplicación
  endurecido con `ops/server-template/bootstrap.sh`. PgBouncer verifica RDS con
  `pgbouncer/rds-global-bundle.pem` (bundle público de AWS, versionado).
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

Ninguna credencial vive en el repositorio ni en el `.env` del servidor: la fuente es AWS
Secrets Manager (`core-force-mail/prod`). `ops/security/secrets/fetch-secrets.sh` los
materializa en `/dev/shm/core-force-mail/secrets.env` (tmpfs, 0600, todo o nada) y
`with-secrets.sh` envuelve cualquier `docker compose` que cree contenedores. La lista
canónica es `ops/security/secrets/secret-keys.txt`; añadir una variable ahí es parte de
introducir el secreto. CI: `make check-secrets` y `make check-secret-sources`.

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
  pasa el pooler. Sus
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
* `docker-compose.images.yml` es generado (`make gen-compose-images`); CI falla si queda
  atrás.
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
* Salida por Amazon SES: `ops/aws/setup-ses.sh <dev|staging|prod>` aplica la pila
  `ops/aws/ses-mail.yaml` (CloudFormation): los configuration sets transaccional y de
  marketing (reputación y TLS por clase; aperturas, clics y bajas solo en marketing, con
  dominio de seguimiento propio y pool de IP dedicadas opcionales), un topic SNS estándar
  cifrado con una clave KMS propia al que solo publican esos dos sets de la cuenta, y la
  suscripción HTTPS a `/api/v1/public/transactional/ses-events`, que `transactional`
  confirma y verifica. `--check` muestra el conjunto de cambios sin aplicarlo. Sus salidas
  son `SES_EVENTS_TOPIC_ARN`, `SES_CONFIG_SET_TRANSACTIONAL` y `SES_CONFIG_SET_MARKETING`.
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
pruebas de `promtool` (`make check-alertas`). Cada servicio expone `/healthz` y `/metrics`
fuera de su cadena de middlewares; las imágenes son `FROM scratch` y el `HEALTHCHECK` usa
el propio binario con `--healthcheck <puerto>`.

## 8. Alta de un servicio

1. `make new-service name=<svc> port=<puerto> module=<modulo>`: molde hexagonal que compila,
   Dockerfile, migración canónica de empresa, puerto en `.env.example`.
2. Ruta en `services/gateway/routes.json` (`prefix`, `service`, `module`).
3. Permisos del módulo en `migrations/registry/NNN_<svc>_permissions.sql` y, si es de
   correo, su módulo de catálogo en `organization.module_catalog.permission_modules`.
4. Bloque en `docker-compose.yml` (el generador imprime el fragmento).
5. `make validate-scaffold` comprueba puertos, permisos, catálogo, acoplamiento, streams e
   imágenes. `make gen-events` y `make gen-observability-targets` regeneran lo derivado.

## 9. Checks antes de dar por terminada una tarea

`make checks` (build, vet, migraciones, acoplamiento, errores mudos, aridad SQL, streams,
contratos de eventos, secretos, scaffold) y `make clean-copy`. Si se toca el perfil autoalojado
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
contra un Postgres y un Redis desechables, con una base por variable `*_TEST_DSN` y los
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

Toda la plataforma en un servidor propio (probado para un VPS Debian 13, 2 vCPU, 3,8 GB de RAM
y 4 GB de swap), sin RDS, ElastiCache ni balanceador. En `ENVIRONMENT=production` los servicios
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
  Los demás guiones de `ops/db` (migraciones y roles) siguen usando `psql` del host y por eso hoy
  solo funcionan desde dentro de la red interna; ver Riesgos.

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
5. Primer despliegue, sin AWS (el transporte cae a `save`):

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

### Riesgos y pendientes

* Los guiones de `ops/db` que aplican migraciones o crean roles (`apply-migration.sh`,
  `apply-all-canonical.sh`, `cell-service-role.sh`, `cell-engine-role.sh`,
  `tenant-service-role.sh`, `bootstrap-platform.sh`) llaman a `psql` del host, que no resuelve
  `postgres-primary`: en este perfil hay que ejecutarlos desde un contenedor en la red interna
  hasta que pasen por `cf_psql` como los del respaldo.
* Los buzones se archivan con Dovecot en marcha: un mensaje que cambie de carpeta durante el
  archivado puede faltar en esa copia (está en la siguiente). Un archivo sin ese riesgo exige
  `doveadm backup` buzón a buzón o parar Dovecot; queda pendiente.
* La cola de Postfix (`postfix-vol`) y el Redis de los motores no se respaldan: lo encolado se
  reintenta desde el emisor y el bayes de Rspamd se reaprende.
* Secretos: el servidor no tiene Secrets Manager. `with-secrets.sh` sigue siendo el único camino,
  pero `fetch-secrets.sh` falla sin el CLI y el rol de AWS y `load.sh` recurre al `.env`, que
  entonces tiene que llevar TODAS las credenciales de `secret-keys.txt` y `secret-keys-db.txt`
  (0600, del usuario de despliegue). En ese modo `load.sh` exporta al entorno solo las claves del
  inventario, citadas, para que los guiones de `ops/db` (el `userlist.txt` de PgBouncer, los roles)
  las vean igual que con el almacen. Es un secreto en disco que en AWS no existe; falta decidir el
  almacén del perfil (por ejemplo `systemd-creds` o un fichero cifrado desbloqueado al arrancar).
  Los secretos del respaldo no siguen ese camino a propósito: con el `.env` entregado entero a cada
  contenedor, la credencial del bucket y la frase de cifrado quedarían a la vista de todo servicio,
  así que viven en `BACKUP_SECRETS_FILE`, que ningún contenedor recibe. Cuando se decida el almacén
  del perfil, esas tres claves entran con las demás (`secret-keys-backup.txt`).
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
* Motores: `scripts/deploy-mail.sh` extrae `deploy/mail` encima de la copia del servidor con el
  usuario de despliegue. Si un motor dejara un fichero versionado de esa copia con otro dueño, `tar`
  fallaría antes de recrear nada (el despliegue se detiene sin tocar los motores). La configuración
  sincronizada llega también a los motores que no se recrean: el despliegue lo avisa, y conviene
  desplegarlos en la misma ventana.
