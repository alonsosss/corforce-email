# Modelo de datos, multitenencia y celdas

Este documento separa lo **verificado en el codigo** (V) de lo **propuesto** (P). Si
implementas algo marcado P, muevelo a V en la misma tarea.

## 1. Tres planos de datos

| Plano | Base | Quien la lee | Contenido | Migraciones |
|---|---|---|---|---|
| Registro | `mail_registry` (una) | identity, access-control, organization, billing; el gateway indirectamente | empresas, celdas, usuarios, sesiones, roles, permisos, catalogo de modulos, planes, suscripciones y contadores de consumo | `migrations/registry/` |
| Celda | `mail_cell_<code>` (una por celda) | Postfix, Dovecot, Rspamd via `mail-auth`/`mail-policy`; mail-directory, mail-security | directorio de correo (`mail`), politicas antispam y cuarentena (`mail_security`) | `migrations/cell/canonical/<svc>/` |
| Empresa | `mail_tenant_<slug>` (una por empresa) | el resto de servicios | auditoria, scheduler, dominios y claves DKIM (`domains`), contactos, campanas, plantillas, envios, supresion | `migrations/tenant/canonical/<svc>/` |

Las tres bases llevan además el esquema `platform` con `event_outbox` (`pkg/outbox`).

Convenciones (V): `id uuid DEFAULT gen_random_uuid()`, `tenant_id uuid NOT NULL` donde
aplica, `timestamptz`, trigger `update_updated_at()`, un esquema por servicio, sin claves
foraneas entre esquemas, cabecera `-- Schema: x | Service: y`, idempotentes y aditivas
(`make check-migrations` lo comprueba en empresa y celda).

## 2. Registro (V)

* `organization.cells(id, code, region, status, db_host, db_port)`: directorio de celdas.
* `organization.tenants(id, cell_id, slug, name, db_name, status, settings)`.
* `organization.module_catalog(module, tier, requires, label, permission_modules jsonb)` y
  `organization.tenant_modules(tenant_id, module, enabled)`. Modulos: `corporate_mail`,
  `transactional`, `marketing`. `permission_modules` agrupa los modulos de permiso que cada
  uno habilita; access-control y el gateway lo leen de ahi, no del codigo.
* `identity.users` (con `tokens_valid_from`), `sessions`, `token_blocklist`,
  `password_policies`, `password_history`, `password_reset_tokens`, `session_policies`,
  `audit_log`.
* `access_control.roles` (`is_system`), `permissions(module, resource, action)`,
  `role_permissions`, `user_roles`, `access_denials`.
* Semilla de permisos del plano de control en `005_seed_permissions.sql`; cada servicio
  nuevo trae la suya.
* Vistas publicadas del registro: los tres servicios del plano de control comparten base
  pero cada uno posee solo su esquema, y `check-coupling` lo exige. identity lee
  `access_control.v_user_roles(user_id, tenant_id, role_name)`, solo roles activos, para
  sellar los roles en el token (`022_access_control_published_views.sql`).
  access-control lee `identity.v_user_status(user_id, tenant_id, status,
  tokens_valid_from)` para la politica y `users-with-permission`
  (`023_identity_published_views.sql`). Ninguna expone correo, nombre, hash ni secreto MFA.
  access-control gatea los permisos por modulo contratado leyendo
  `organization.v_module_catalog(module, tier, permission_modules)` y
  `organization.v_tenant_modules(tenant_id, module, enabled)`
  (`024_organization_published_views.sql`), sin `requires`, `label` ni `updated_at`.
  Lo que aun lee o escribe tablas de `organization` o desde ella figura con su motivo en
  `ops/scaffold/coupling-allowlist.txt` y `coupling-writes-allowlist.txt`.
* `billing` (`013_billing.sql`, permisos en `014`): `plans` (codigo unico, moneda ISO 4217,
  `base_price numeric(15,2)`, `monthly|yearly`, `active|retired`), `plan_limits` (un limite
  por recurso: `included` con -1 ilimitado, `hard_limit`, `overage_unit_price numeric(15,6)`
  solo en limite blando), `subscriptions` (una por empresa, periodo en dias UTC con
  `anchor_day` para que 31 ene -> 28/29 feb -> 31 mar no derive), `usage_counters` (stock en
  `period_start = 1970-01-01`, flujo por periodo de la suscripcion), `processed_events`
  (deduplicacion del consumo por id de evento, podada a 30 dias) y `stock_items` (un dominio
  lo informan domain-service y mail-directory: cuenta una vez). Lo lee solo `billing`, que
  filtra por `tenant_id` en cada consulta; los demas servicios preguntan por HTTP.

El registro no tiene RLS: los tres servicios que lo leen filtran por `tenant_id` en cada
consulta y los handlers comprueban que el recurso pedido por id pertenece a la empresa de
la sesion (V en identity, access-control y organization).

## 3. Celda

### 3.1 Esquema `mail` (V: migracion y `mail-directory`, que lo escribe)

`migrations/cell/canonical/mail-directory/01_mail.sql`. Conserva la semantica del
directorio de mailcow, que es la que Postfix y Dovecot entienden, traducida a PostgreSQL:

* `domains`, `alias_domains`, `mailboxes` (tri-estado `active` 0/1/2, `mailbox_format`,
  `path_prefix`, `quota_bytes`, `kind`, `tls_enforce_*`, `*_access`), `aliases`,
  `spam_aliases`, `sender_acl`, `app_passwords`, `relayhosts`, `transports`,
  `tls_policy_overrides`, `recipient_maps`, `bcc_maps`, `quota_usage` (la escribe Dovecot),
  `sieve_filters` con vistas `v_sieve_before`/`v_sieve_after`, `sasl_logins`.
* Rol `mail_engine`: `SELECT` sobre lo que consultan los motores, escritura solo en
  `quota_usage`, nada sobre `app_passwords` ni `sasl_logins`. Los motores nunca ven un hash
  de contrasena: la verificacion pasa por `mail-auth`.
* `relayhosts.password` y `transports.password` van en claro por necesidad de Postfix (un
  mapa `pgsql:` no descifra); por eso solo los ve `mail_engine` y su servicio propietario,
  y la columna nunca sale por el API.

Como leen los motores (V en `deploy/mail`, ver su README): Postfix por mapas
`proxy:pgsql:` con las consultas traducidas; Dovecot `userdb sql` con `driver = pgsql`,
dict de cuota sobre `mail.quota_usage`, dicts de sieve sobre las vistas; contrasenas por
`passwd-verify.lua` -> `MAIL_AUTH_URL`.

### 3.2 RLS en la celda (V: politicas; P: que todos los servicios de celda las usen)

`02_mail_rls.sql`: rol `mail_app` (NOLOGIN, concedido al usuario de la aplicacion) con
politica `tenant_isolation` por `tenant_id = mail.current_tenant()` en todas las tablas con
empresa, fail-closed sin GUC; rol `mail_engine` con `engine_read` `USING (true)` (Postfix y
Dovecot no saben de empresas). `quota_usage` se lee por la aplicacion solo unida a sus
buzones. Vistas publicadas `mail.v_routing_*` para `mail-security`. Probado: la
aplicacion ve solo su empresa, nada sin GUC y no puede escribir en otra; el motor ve todo.
Los servicios Go de celda acceden SIEMPRE dentro de `db.TransactRLS` y ademas filtran por
`tenant_id` en el SQL; `mail-auth` y los endpoints de motores de `mail-security` consultan
sin cambio de rol porque resuelven identidades sin empresa previa, y lo dicen en un
comentario.

Rol `mail_service` (V, 2026-09-13, `mail-directory/06_service_role.sql` y
`mail-security/06_service_role.sql`): lo que antes corria como dueno de las tablas (esos
caminos sin empresa, la ruta interna de activacion, las tareas de fondo y el rele de la
outbox) corre con la credencial de la celda (5.1), que no es duena de nada. `mail_service`
(NOLOGIN) tiene DML sobre `mail` y `mail_security`, DML sobre `platform.event_outbox` y la
politica `service_all` `USING (true) WITH CHECK (true)` en cada tabla con RLS de los dos
esquemas (incluidas `engine_documents` y `firewall_*`); ni DDL, ni `TRUNCATE`, ni tablas
temporales. Una tabla nueva con RLS lleva su `service_all` en la migracion que la crea (la
prueba de integracion de 5.1 falla si falta). Las peticiones con usuario siguen bajando a
`mail_app`, que no hereda nada de `mail_service`.

`mail_security` (`migrations/cell/canonical/mail-security/01_mail_security.sql`) sigue el
mismo patron: `tenant_isolation` por `tenant_id = mail_security.current_tenant()` para
`mail_app` en sus nueve tablas, sin `FORCE`. Su API de administracion corre en
`TransactRLS` y filtra por `tenant_id`; los endpoints de motores (`/pipe`, `/settings`...)
consultan como `mail_service` y atribuyen cada fila de cuarentena a la empresa del buzon final.
Probado contra Postgres: una empresa no ve umbrales, ajustes ni cuarentena de otra.

Anadidos de `mail-security` (V, probado contra Postgres con las migraciones aplicadas dos
veces, 2026-09-13): `04_smtp_access.sql`, redes SMTP por buzon (`cidr`; un CHECK exige
prefijo IPv4 de /8 a /32 e IPv6 de /32 a /128, lo que compara Rspamd) con
`tenant_isolation`; `03_engine_documents.sql`, la marca `Last-Modified` de `/settings` por
celda, y `05_firewall.sql`, listas y opciones del cortafuegos de la celda. Estas dos
ultimas no tienen `tenant_id`: son de la celda o de la plataforma, `mail_app` no tiene
permisos sobre ellas (RLS activo sin politica para ella) y el servicio las usa como
`mail_service`, el cortafuegos solo tras exigir al superadmin.

`07_quarantine_notices.sql` (V, probado contra Postgres con las migraciones aplicadas dos
veces, 2026-09-13): `quarantine_notices`, un registro por aviso de cuarentena (`sent`,
`suppressed`, `rejected` con codigo por CHECK, `skipped`) con los mensajes que lista y
`UNIQUE (tenant_id, idempotency_key)`, escrito en la misma transaccion que marca
`notified`; `quarantine_link_uses`, la constancia de cada enlace sin sesion usado, con
`UNIQUE (tenant_id, quarantine_id)` como garantia de un solo uso y sin clave foranea a
`quarantine` (la fila se borra al liberar o descartar y la constancia queda); y el indice
parcial de lo pendiente de aviso `(tenant_id, rcpt, created_at DESC, id DESC) WHERE notified
= false`. Las dos tablas tienen `tenant_isolation` para `mail_app` y `service_all` para
`mail_service`, y se podan con el `max_age_days` de su empresa.

`03_mail_app_policies.sql` (mail-directory) anade lo que el primer consumidor necesito:
`app_delete` sobre `quota_usage` (solo del buzon propio, por eso el servicio borra la cuota
antes que el buzon), `WITH CHECK` en `transports` que admite `tenant_id NULL` solo con
`app.is_privileged` (que ademas sea el superadmin lo exige el servicio), y
`mail.name_in_use(text)` (SECURITY DEFINER) para saber si un nombre ya es dominio o dominio
alias de otra empresa sin ver sus filas. `mail-directory` abre TODA lectura y escritura en
`db.TransactRLS`; su ruta interna de activacion (la llama domain-service con `X-Tenant-ID`,
sin usuario) corre sin cambio de rol y ahi solo protege el filtro por `tenant_id`. Probado
con dos empresas en `services/mail-directory/internal/adapters/postgres/integration_test.go`.

### 3.3 Eventos de la celda (V, 2026-09-13)

`mail-directory` (`mail.*`) y `mail-security` (`mail_security.quarantine.*`) encolan con
`pkg/outbox.Enqueue` en la MISMA transaccion que el cambio (bajo `TransactRLS` en las
peticiones con usuario): el evento existe si y solo si existe el cambio, y un fallo al
encolar revierte la escritura. `mail-directory/05_outbox_grants.sql` y
`mail-security/02_outbox_grants.sql` conceden a `mail_app` `USAGE` en `platform` e
`INSERT`, y solo `INSERT`, sobre `platform.event_outbox`: la aplicacion encola pero no lee
una tabla con eventos de todas las empresas. Un solo rele por celda vacia la outbox hacia
JetStream (`Relay.RunExclusive` con `db.TryLeaderLock` y `outbox.CellRelayLockKey`,
compartida por los dos servicios y sus replicas) y poda lo publicado a los 7 dias. El id
del evento es el de la fila y no cambia entre reintentos: es la clave de deduplicacion de
JetStream y de los consumidores. Probado en
`services/mail-directory/internal/adapters/postgres/outbox_integration_test.go`: el alta
de buzon deja fila y evento, sin permiso de `INSERT` no deja ninguna, y el rele entrega el
evento con el id de su fila.

## 4. Empresa (V)

Base `mail_tenant_<slug>` creada por `organization` al dar de alta la empresa
(`CREATE DATABASE` + todas las canonicas en orden, con advisory lock sobre conexion
directa sin PgBouncer, `public.schema_migrations`, baseline para bases preexistentes).
Barrido en segundo plano al arrancar (`RUN_TENANT_MIGRATIONS`). Hoy contiene `audit`,
`scheduler` (`scheduler.job_definitions`, con el `handler` validado contra la lista blanca
`services/scheduler/handlers.json`; `scheduler.job_schedules`, cuyo bloqueo por trabajo es
el `FOR UPDATE SKIP LOCKED` de la transaccion que lo despacha, sin escribir ya `is_locked`;
`scheduler.job_executions` con estado `pending|running|completed|failed|cancelled` por
CHECK, `deadline_at` para el vencimiento, `next_attempt_at` para el reintento en espera,
`retry_of` con el indice unico parcial `uq_job_executions_retry_of`, un solo reintento por
ejecucion, y `failure_reason` `executor|timeout|handler_not_allowed` por CHECK; cada cambio
de estado encola `scheduler.job.started|completed|failed` en la outbox en la misma
transaccion; `scheduler.scheduled_tasks`), `domains`, `suppression` (lista de exclusiones de envio,
V 2026-09-13: una fila por causa, `UNIQUE (tenant_id, email, reason)` desde
`suppression/02_causes.sql`, que conservo tal cual las filas del modelo anterior de una
fila por direccion; cada causa (`hard_bounce`, `complaint`, `unsubscribe`, `invalid`,
`manual`) guarda su origen, detalle y caducidad y se registra y se retira por separado,
con un bloqueo consultivo por direccion en la transaccion; la causa principal, la vigente
mas grave por el orden de `domain.Reasons()`, se calcula al leer y es el `reason` del
listado, de la consulta de una exclusion y de la consulta previa a todo envio por
`POST /internal/suppression/check`, que anaden `reasons` con todas las vigentes; el listado
filtra y las estadisticas cuentan por la principal; `suppression.entry.added` y `.removed`
llevan la causa que entro o salio en `reason` y las vigentes que quedan en `reasons`) y
`templates` (plantillas de correo por empresa:
`templates.templates` con nombre unico por empresa y `current_version`, y
`templates.versions` con asunto, HTML, texto opcional y variables declaradas en `jsonb`;
una sola version publicada por plantilla garantizada por el indice parcial
`uq_versions_one_published`, las anteriores quedan `superseded`; renderizado interno por
`POST /internal/templates/{id}/render` y evento `templates.template.published` por la
outbox), `transactional` (mensajes de las dos clases en `transactional.messages.class`,
`transactional` o `marketing`; los de marketing llevan `campaign_id` y `contact_id` y la
restriccion `messages_marketing_check` exige campana, contacto, enlace de baja y un solo
destinatario; `is_test` marca los envios de prueba de campaigns (solo el lote interno con
la etiqueta `test=true` la fija, `messages_test_marketing_check` la limita a marketing, y
viaja como `test` en todos los `transactional.email.*`, que analytics no cuenta);
peticiones idempotentes en `transactional.submissions` con su clase y los
suprimidos de la respuesta, para que una repeticion devuelva lo mismo y una clave no cruce
de clase; eventos de SES, proyeccion de dominios de envio, bajas),
`campaigns` (`campaigns.campaigns` con nombre unico por empresa sin distinguir mayusculas,
estado con CHECK, version de plantilla obligatoria fuera de borrador, audiencia en `jsonb`
y contadores con CHECK de no negativos; `campaigns.batches`, un lote por pagina de la
audiencia con su cursor de entrada y salida, la pagina fijada antes del primer envio
(se vacia al cerrarse) y la reserva del trabajador que lo envia, con a lo sumo un lote
pendiente por campana por el indice parcial `uq_campaigns_batches_one_pending`;
`processed_events` para la deduplicacion por id de evento, podada a los 30 dias;
`message_engagement` con la primera apertura y el primer clic de cada mensaje),
`analytics` (agregados diarios por dia UTC en `analytics.daily_class_stats`,
`daily_campaign_stats` y `daily_domain_stats`, cada uno con sus dimensiones sin NULL
ambiguos y contadores con CHECK de no negativos; `analytics.message_facts`, una fila por
mensaje con la primera ocurrencia de cada hito y solo el dominio del destinatario, nunca
la direccion, podada por `ANALYTICS_MESSAGE_RETENTION_DAYS`; `processed_events` para la
deduplicacion por id de evento, podada a los 30 dias; `campaigns_seen` con el estado de
cada campana segun sus eventos) y `reputation` (contadores por dia UTC y clase en `reputation.daily_stats`, con los envios
contados por destinatario igual que rebotes permanentes y quejas; estado vigente por clase
en `reputation.states` con la marca `manual` de una decision del superadmin que la
evaluacion no pisa; `reputation.state_history` de solo insercion, protegido por trigger;
limites de tasa propios en `reputation.limit_overrides`; ids de evento ya contados en
`reputation.processed_events` para que la reentrega no sume dos veces; cada cambio de
estado sale por la outbox como `reputation.tenant.state_changed`).
Tambien `contacts` (audiencia de marketing, 2026-09-13): `contacts.contacts` con
`UNIQUE (tenant_id, email)`, atributos en `jsonb` validados contra
`contacts.attribute_definitions`, etiquetas `text[]`, `status` y `marketing_consent`, que es
la proyeccion del consentimiento vigente mantenida por un trigger de `contacts.consents`;
`contacts.consents` es evidencia de solo insercion (un trigger rechaza UPDATE, DELETE y
TRUNCATE salvo la seudonimizacion del borrado del titular, con `SET LOCAL app.erasure = 'on'`
en su transaccion); `contacts.confirmation_tokens` guarda solo el sha256 del token del doble
opt-in; `contacts.lists`, `contacts.list_members`, `contacts.segments` (el DSL validado,
nunca SQL) y `contacts.imports`. El indice parcial `idx_contacts_contacts_sendable
(tenant_id, id) WHERE status = 'active' AND marketing_consent = 'granted'` sirve la audiencia
por keyset y la consulta interna de enviables por id. El `status` que implica la supresion
(V, 2026-09-13) lo decide el consumidor de `suppression.entry.added|removed` con las causas
vigentes de la direccion, que lee de `POST /internal/suppression/check` con la fila del
contacto bloqueada (5 s, un intento; si falla, el evento queda sin confirmar y JetStream lo
reentrega), no con la causa ni la foto de `reasons` del evento, que puede llegar
desordenada: una queja o un rebote vigentes fijan el estado del mas grave; una baja vigente
deja `unsubscribed` a quien estaba en un estado mas grave o la acaba de registrar, y solo
entonces revoca el consentimiento; sin ninguna causa vigente `bounced` y `complained`
vuelven a `active` (una `manual` o `invalid` vigente lo impide) y `unsubscribed` no, ni se
reconcede el consentimiento: eso solo lo hace un doble opt-in. Un evento sin `reasons`
(productor anterior) se aplica por su causa, con un aviso limitado en el log.
Tambien `automations` (2026-09-13): `automations.doi_settings` (una fila por empresa,
`UNIQUE (tenant_id)`; activado exige plantilla y remitente por CHECK);
`automations.doi_deliveries`, un intento por evento `contacts.consent.requested` con
`UNIQUE (event_id)`, estado `pending|sent|skipped|failed`, motivo, `message_id`, intentos y
la foto de plantilla y remitente con que se reclamo (un pending la exige por CHECK), sin el
enlace de confirmacion, que es una credencial y nunca se guarda; el indice parcial
`(tenant_id, contact_id, created_at) WHERE status IN ('pending','sent')` sirve el limite por
contacto; `automations.workflows` con nombre unico por empresa sin distinguir mayusculas,
estado y disparador con CHECK (lista blanca), campana solo para `email.clicked`, pasos en
`jsonb` (1..20 por CHECK) e indice parcial de los activos por disparador;
`automations.runs`, una ejecucion por contacto y flujo con `UNIQUE (workflow_id, contact_id,
trigger_event_id)` y `UNIQUE (workflow_id, contact_id, entry_key)` (`entry_key` = id del
evento con reentrada, `once` sin ella), la reserva `lease_token`/`lease_until` que solo existe
en `running` y `finished_at` que solo existe en los terminales (CHECK), indice parcial de las
debidas y de las fallidas por flujo; `automations.processed_events` para la deduplicacion por
id de evento, podada a los 30 dias.

## 5. Enrutado por peticion y por celda (V)

El gateway pone `X-Tenant-ID` desde el JWT; `db.TenantPoolMiddleware` resuelve la empresa
en `organization.tenants` unida a `organization.cells` (`db_name`, `db_host`, `db_port`;
cache en memoria) y abre un pool perezoso por destino (`MaxConns 10`, `MinConns 0`,
reciclado a los 5 minutos ociosos). Una celda cuyo `db_host` coincide con `POSTGRES_HOST`
es el cluster por defecto; cualquier otra abre su pool contra su propio host con la misma
credencial de plataforma (`db.NewTenantRouting` lo cablea en cada `main`). `organization`
crea y migra la base de una empresa nueva en el cluster de su celda (conexion directa a la
base de mantenimiento de la celda) y la cierra a PUBLIC (`REVOKE CONNECT, TEMPORARY`):
solo entran su dueno (la credencial de plataforma) y los superusuarios; si no puede
cerrarla, la retira. Los servicios de celda abren su base por `CELL_DB_NAME`
(`db.NewCellPool` + `db.StaticPoolMiddleware`).

P: mover una empresa de celda (`TenantPoolManager.Forget` ya invalida la cache; falta el
traslado de datos).

### 5.1 Credencial de la celda (V, 2026-09-13)

`mail-directory`, `mail-security` y `mail-auth` abren la base de su celda con el rol de
login `<CELL_DB_NAME>_svc` (`mail_cell_pe_01_svc`, `config.CellServiceRole`); fuera de
desarrollo, nunca con la de plataforma.

* Lo crea `ops/db/cell-service-role.sh --cell <code>` con la credencial de plataforma:
  crear roles no es cosa del runtime, y la contrasena no la genera ningun servicio, vive en
  el almacen como `CELL_DB_PASSWORD`. Idempotente: `CREATE ROLE ... LOGIN` si falta, la
  contrasena fijada en cada ejecucion como verificador SCRAM-SHA-256 calculado fuera de
  Postgres (el texto de la sentencia nunca lleva la contrasena), `GRANT mail_app,
  mail_service`, `GRANT CONNECT` sobre su base al rol y a `mail_engine`, y `REVOKE CONNECT,
  TEMPORARY ... FROM PUBLIC` en todas las bases del cluster salvo `postgres` (sin datos).
  Termina comprobando que el rol no tiene atributos de administracion, no crea objetos ni
  temporales y no alcanza otra base, y falla si no.
* El servicio la recibe por `CELL_DB_PASSWORD` (secreto) y, opcional, `CELL_DB_USER` (por
  defecto el nombre por convencion). `config.PostgresConfig.CellConnection` falla cerrado:
  sin `CELL_DB_PASSWORD` solo admite la credencial de plataforma si `ENVIRONMENT` dice
  expresamente `development` o `test` (el valor por defecto no cuenta) y lo avisa al
  arrancar; en cualquier otro entorno el servicio no arranca. Con credencial de celda no
  necesita `POSTGRES_PASSWORD`.
* Probado: unitarias de `pkg/config` y `pkg/db`; integracion contra Postgres 16
  (`services/organization/internal/adapters/postgres/cell_role_integration_test.go`: el rol
  entra en su celda por TCP con contrasena y no en el registro, en otra celda ni en una
  base de empresa que organization crea despues; sin DDL, `TRUNCATE` ni temporales;
  `mail_app` sigue acotado por RLS y sin lectura de la outbox; rotacion de contrasena); y
  `make e2e`, que corre los tres servicios de celda con su credencial y sin la de
  plataforma.

Pendiente de la credencial de celda (P):

* Reparto de secretos por servicio en compose: hoy cada servicio recibe el fichero de
  secretos entero, asi que un servicio de celda sigue viendo `POSTGRES_PASSWORD` y uno de
  empresa `CELL_DB_PASSWORD`. A cada uno se le deja vacia la que no le toca.
* PgBouncer autentica con `userlist.txt` (`auth_type = plain`): el rol de la celda necesita
  su entrada ahi. Mejor: `auth_query` con un usuario que solo consulte verificadores.
* `mail_engine` sigue siendo un rol de login compartido por las celdas de un cluster, con
  CONNECT en todas sus bases de celda: pasa a un rol por celda con el mismo patron.
* Una base de celda recien creada queda abierta a PUBLIC hasta que corre el script para
  ella: se corre en el mismo paso en que se abre la celda.

### 5.2 Credencial de las bases de empresa (P)

Evaluado y descartado con el enrutado actual: un servicio de empresa atiende desde un solo
despliegue a TODAS las empresas de TODAS las celdas y resuelve cada una en el registro.
Con una credencial por celda tendria la de todas las celdas y seguiria necesitando la de
plataforma para leer `organization.tenants`: no aislaria nada. Ademas `DBTarget` no lleva
la celda (la del cluster por defecto se normaliza a host vacio) y `secret-keys.txt` es una
lista fija de nombres. El diseno que si aisla:

1. Vista publicada `organization.v_tenant_routing` (id, slug, status, db_name, db_host,
   db_port, codigo de celda) y un rol de login de enrutado con `SELECT` solo sobre ella y
   CONNECT solo al registro: `db.NewTenantRouting` resuelve con el, no con la credencial de
   plataforma.
2. Rol NOLOGIN por servicio con `USAGE` y DML solo sobre su esquema, concedido por su
   migracion canonica de empresa, y rol de login por servicio miembro de el; organization
   le da CONNECT al crear cada base de empresa. Sin DDL (las migraciones siguen corriendo
   como dueno). Un servicio comprometido ve su esquema, no los de los demas ni el registro.
3. Si los servicios de empresa llegan a desplegarse por celda: el mismo rol por servicio y
   celda, con CONNECT solo a las bases de empresa de su celda; `DBTarget` gana la celda y
   `TenantPoolManager` elige la credencial por celda.
4. `organization` sigue siendo el unico con la credencial de plataforma (crea y migra).

## 6. Limites que condicionan el dimensionado (V, heredados y vigentes)

* PgBouncer en `pool_mode = transaction`, `default_pool_size 20`, `max_client_conn 1000`.
  Cada servicio abre hasta 10 conexiones por base de empresa activa: con S servicios que
  toquen bases de empresa y T empresas activas a la vez, el techo es 10 x S x T conexiones
  de cliente hacia PgBouncer. `MinConns 0` es lo que permite tener muchas empresas
  registradas con pocas activas.
* Advisory locks solo con `TryLeaderLock` (transaccional) o por conexion directa:
  `pg_try_advisory_lock` de sesion no es fiable tras PgBouncer.
* Un cambio de TIPO de columna deja `cached plan must not change result type` en las
  conexiones del pooler: `ops/maintenance/pgbouncer-reconnect.sh` tras desplegar.

## 7. Garantias de aislamiento

| Garantia | Estado |
|---|---|
| Una empresa nunca lee la base de otra (bases separadas) | V |
| El gateway inyecta el tenant; los servicios no aceptan `X-Tenant-ID` sin `X-Gateway-Token` | V |
| Rutas por id comprueban la empresa de la sesion en el plano de control | V |
| Directorio de correo con `tenant_id` en cada fila y rol de motores sin acceso a credenciales | V |
| RLS en la celda para los servicios Go | V (politicas y roles; `mail-directory` y el API de administracion de `mail-security` las usan en toda lectura y escritura) |
| Una celda no lee otra celda: sus servicios abren solo `CELL_DB_NAME` con el rol `<CELL_DB_NAME>_svc`, sin CONNECT al registro, a otra celda ni a una empresa (5.1); claves de cifrado por celda | V / P (claves; reparto de secretos por servicio) |
| Un servicio de empresa solo abre su esquema y no el registro (credencial por servicio) | P (5.2) |
| Respaldo por base y restauracion probada semanalmente (`ops/backup`) | V (scripts), P (programados en este entorno) |
