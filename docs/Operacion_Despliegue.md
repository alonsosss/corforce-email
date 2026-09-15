# Operación y despliegue

Reglas de operación heredadas del ERP de referencia, ya como documento propio. Las razones
largas de cada guardarraíl están en `ops/scaffold/README.md`, `ops/security/secrets/README.md`,
`ops/backup/README.md` y `docs/arquitectura/OBSERVABILIDAD.md`.

## 1. Entornos

* Desarrollo: `make dev` levanta Postgres (perfil `embedded-db`), PgBouncer, Redis, NATS y
  el plano de control con `docker-compose.yml`. Los motores de correo se levantan aparte
  con `deploy/mail/docker-compose.mail.yml` cuando se trabaje en la fase 2.
* Producción (AWS): una cuenta por ambiente (dev, staging, prod). RDS PostgreSQL Multi-AZ
  detrás de PgBouncer, ElastiCache, SES, S3, Secrets Manager. Servidor de aplicación
  endurecido con `ops/server-template/bootstrap.sh`.
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
  reputation, organization, mail-directory y domain-service. Los umbrales, la ventana y los
  límites de reputation siguen siendo
  obligatorios y sin valor por defecto: se exige la variable y después se lee con su rango
  (`EnvFloat` para los umbrales). El techo de `DOMAIN_RECHECK_INTERVAL` (6 h) es el que
  cuenta la alerta `BarridoDeDominiosSinCelda` (`docs/arquitectura/OBSERVABILIDAD.md`).
  Pendiente de migrar, con lector propio: mail-security.
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
  regla (`tenantcell.ParseInstances`).
* URLs base internas de otros servicios (`<SERVICIO>_URL`): se leen al arrancar, antes de
  conectar a nada, con `config.ServiceURL` o `config.RequiredServiceURL`
  (`pkg/config/upstream.go`). Regla: `http` o `https` absoluta, `scheme://host[:puerto]`, con el
  host de la regla anterior (una IPv6, entre corchetes), puerto opcional de 1 a 65535 y como
  mucho la barra de la raíz, que se quita; sin usuario, ruta, consulta ni fragmento. Ningún
  cliente la necesita: cada uno le pega su ruta absoluta (`/internal/...`), así que una ruta o
  una consulta desviarían la llamada y unas credenciales viajarían por la red interna. Mal
  formada, el servicio no arranca y el error nombra la variable. Cada una conserva si es
  obligatoria u opcional. Obligatorias: `ORGANIZATION_URL` (servicios de celda y
  domain-service, `tenantcell.OrganizationURLFromEnv`), `SUPPRESSION_URL` de contacts,
  `CONTACTS_URL`, `TRANSACTIONAL_URL` y `TEMPLATES_URL` de campaigns y automations,
  `BILLING_URL` de reputation y `MAIL_DIRECTORY_URL` del webmail. Opcionales: `SUPPRESSION_URL`,
  `TEMPLATES_URL` y `REPUTATION_URL` de transactional y `TRANSACTIONAL_MAIL_URL` de identity
  (vacías, lo que depende de ellas no funciona y se registra); `ACCESS_CONTROL_URL`, por
  defecto `http://access-control:8002` (`authz.CheckerFromEnv`); y en organization
  `ACCESS_CONTROL_URL`, `IDENTITY_URL` y `MAIL_DIRECTORY_URL`, que sin ellas usa
  `<SERVICIO>_HOST`. No son URLs base internas y conservan su lector: `MAIL_AUTH_URL` (el
  endpoint https que llaman tal cual Dovecot y el webmail), `PUBLIC_BASE_URL`,
  `PASSWORD_BREACH_API_URL`, `MINIO_PUBLIC_URL` y `NATS_URL`. Pendientes de migrar, con lector
  propio: mail-security (`ACCESS_CONTROL_URL`, `TRANSACTIONAL_URL`, `DOVEADM_API_URL`) y
  domain-service (`ACCESS_CONTROL_URL`, `MAIL_DIRECTORY_URL`, `MAIL_SECURITY_URL`).
* Entorno declarado (`ENVIRONMENT`). Un servidor declara exactamente
  `ENVIRONMENT=production` o `ENVIRONMENT=staging` en el `.env` de `DEPLOY_PATH`, el que
  Compose pasa a los contenedores; también el servidor de la cuenta de dev. `development` y
  `test` son solo locales (`.env.example` trae `development` para `make dev`; `make e2e` lo
  exporta). `ops/server-template/bootstrap.sh` no aprovisiona sin uno de los dos en
  `server.env` y lo escribe en ese `.env`. `ops/security/secrets/require-server-environment.sh`,
  que `with-secrets.sh` ejecuta antes de materializar ningún secreto, se niega a desplegar si
  la última asignación de `ENVIRONMENT` del `.env` no es exactamente una de las dos (con
  espacios, un comentario o un CR al final tampoco: el servicio recibiría otro texto). Todo
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
  | Sin `INTERNAL_GATEWAY_TOKEN` arrancan el gateway, domain-service, organization, webmail, mail-directory y mail-security, y `RequireGatewayToken` deja pasar sin comprobar; en cualquier otro entorno esos seis no arrancan y el middleware responde 503 a todo | `middleware.InternalGatewayToken` (`pkg/middleware/middleware.go`), `services/gateway/main.go`, `services/domain-service/main.go`, `services/organization/main.go`, `services/webmail/main.go`, y `tenantcell.MembershipFromEnv` (`pkg/tenantcell/membership.go`) en mail-directory y mail-security |
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
bcrypt hecho por Postgres). Idempotente. Es lo único que no se puede hacer por API, porque
para llamar a la API hace falta ya un superadmin. Después, todo por API: `POST /cells`,
`POST /organizations`.

## 4. Migraciones

* Registro: las aplica `organization` al arrancar (`RegistryMigrator`, idempotente).
* Empresa: se aplican al crear la empresa y en un barrido de fondo (`RUN_TENANT_MIGRATIONS`)
  por conexión directa con advisory lock; `POST /api/v1/organizations/migrate` para forzar.
* Celda: `ops/db/apply-migration.sh` contra `mail_cell_<code>` (P: runner propio en
  `mail-directory`). Al abrir una celda, en el mismo paso y tras sus migraciones,
  `ops/db/cell-service-role.sh --cell <code>`: crea el rol de sus servicios y cierra a
  PUBLIC las bases del cluster (necesita `mail_service`, de las migraciones 06). Sus
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
  `git archive` de HEAD: compose, `migrations/`, `ops/security`, `ops/ecr`,
  `ops/observability`, `ops/maintenance`, `ops/backup`, `pgbouncer`.
* Commits siempre con pathspec (`git commit -- <rutas>`): el índice puede estar compartido
  con otra sesión.
* `docker-compose.images.yml` es generado (`make gen-compose-images`); CI falla si queda
  atrás.
* Salida por Amazon SES: `ops/aws/setup-ses.sh <dev|staging|prod>` aplica la pila
  `ops/aws/ses-mail.yaml` (CloudFormation): los configuration sets transaccional y de
  marketing (reputación y TLS por clase; aperturas, clics y bajas solo en marketing, con
  dominio de seguimiento propio y pool de IP dedicadas opcionales), un topic SNS estándar
  cifrado con una clave KMS propia al que solo publican esos dos sets de la cuenta, y la
  suscripción HTTPS a `/api/v1/public/transactional/ses-events`, que `transactional`
  confirma y verifica. `--check` muestra el conjunto de cambios sin aplicarlo. Sus salidas
  son `SES_EVENTS_TOPIC_ARN`, `SES_CONFIG_SET_TRANSACTIONAL` y `SES_CONFIG_SET_MARKETING`.
  No verifica dominios ni saca la cuenta del sandbox: eso es por empresa y con su DNS.

## 6. Respaldos

`ops/backup/backup-tenants.sh` vuelca cada base (`mail_%`) por separado con `pg_dump -Fc`,
verifica que `pg_restore --list` lo lee, sube a `BACKUP_S3_BUCKET` (bucket distinto al de
medios, con Object Lock; la misma variable en `ops/aws/setup-iam.sh`, `setup-buckets.sh` y
`setup-auditoria.sh`) y conserva 3 días en local. Cada semana `verify-restore.sh` restaura
el último volcado de la empresa con el respaldo más grande (o la base que se le indique) en
una base desechable y comprueba, sin suponer filas de negocio, que están todos los esquemas y
tablas del índice del volcado, `platform.event_outbox`, el historial
`public.schema_migrations` con filas y el esquema que declara cada migración registrada; en
una celda, que no tiene historial, los esquemas de sus migraciones, y en el registro al menos
un usuario. Las alertas vigilan la antigüedad del último éxito y la ausencia de la serie.
Programación: `ops/backup/systemd/` (`core-force-mail-backup.timer` y
`core-force-mail-backup-verify.timer`, que instala `bootstrap.sh`).

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
contratos de eventos, secretos, scaffold) y `make clean-copy`. Con docker: `make test`
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
