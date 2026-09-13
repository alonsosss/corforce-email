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
  tokens_valid_from, effective_status)` para la politica, `users-with-permission` y la
  retirada de los roles de una cuenta borrada (`023_identity_published_views.sql`, ampliada
  por `027_identity_effective_status.sql`: `effective_status` cuenta como `active` un bloqueo
  por intentos ya vencido, la regla de identity). Ninguna expone correo, nombre, hash ni
  secreto MFA.
  access-control gatea los permisos por modulo contratado leyendo
  `organization.v_module_catalog(module, tier, permission_modules)` y
  `organization.v_tenant_modules(tenant_id, module, enabled)`
  (`024_organization_published_views.sql`), sin `requires`, `label` ni `updated_at`.
  Ningun servicio lee ni escribe tablas de otro esquema del registro:
  `ops/scaffold/coupling-allowlist.txt` y `coupling-writes-allowlist.txt` estan vacias. El
  alta y la baja de empresa son una saga de `organization` (`organization.tenant_sagas`,
  `026_organization_tenant_sagas.sql`) que pide el rol del sistema a access-control y el
  primer usuario a identity por su API interna (`Usuarios_Roles_y_Acceso.md`, 7).
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

Base `mail_tenant_<slug>` creada por la saga de alta de `organization` (`CREATE DATABASE` +
todas las canonicas en orden, con advisory lock sobre conexion directa sin PgBouncer,
`public.schema_migrations`, baseline para bases preexistentes). La base queda marcada con el
id de la empresa en su `COMMENT`: una compensacion o un reintento solo borra o adopta una
base que lleva la marca de esa empresa, nunca una ajena con el mismo nombre.
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
`POST /internal/suppression/check`, que anaden `reasons` con todas las vigentes; la consulta
previa anade ademas `causes: [{reason, created_at}]` (V, 2026-09-13), las mismas causas de
`reasons` en su orden con la hora de alta de su fila, el reloj de la base de la empresa;
el listado
filtra y las estadisticas cuentan por la principal; `suppression.entry.added`, `.removed` y
`.expired` llevan la causa que entro, salio o caduco en `reason` y las vigentes que quedan en
`reasons`; `.expired` lleva ademas `expires_at`, la caducidad anunciada (V, 2026-09-13; ver
"Caducidad y barrido" mas abajo), y `announced_expires_at` guarda en la fila la caducidad ya
anunciada (`suppression/04_expiry_announced.sql`).
Horas de alta y resuscripcion (V, 2026-09-13): `created_at` y `updated_at` tienen DEFAULT
`clock_timestamp()` desde `suppression/03_clock_timestamp.sql` (las filas existentes no
cambian), la hora de la sentencia que escribe la fila y no la del inicio de su transaccion,
que podia quedar antes de un doble opt-in confirmado mientras el alta esperaba el bloqueo
de la direccion. Una baja que se vuelve a pedir estando vigente se vuelve a registrar: la
misma fila toma los datos del nuevo origen y `created_at = clock_timestamp()`, y
`suppression.entry.added` se publica otra vez con un id de evento nuevo, asi que JetStream
no lo deduplica y contacts lo compara con su ultimo reconsentimiento (contacts no deduplica
por id: decide por las causas vigentes y sus horas, y repetir una baja sin reconsentimiento
en medio no le cambia nada); un rebote, una queja o una manual vigentes repetidos no
cambian nada. `contacts.contact.resubscribed` solo retira la baja cuyo `created_at` es
anterior a su `consented_at`: una baja posterior, o la reentrega tardia del evento, no se la
lleva, y en empate la baja sigue, la misma regla con que contacts decide que esa baja revoca
el consentimiento. Sin `consented_at` (productor anterior) la retira como antes; con un
valor que no es una hora RFC 3339 el evento se descarta con un error en el log y la baja
sigue. `updated_at` lo pone el trigger comun con `now()`, asi que en una baja vuelta a
registrar puede quedar por detras de `created_at`) y
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
(V, 2026-09-13) lo decide el consumidor de `suppression.entry.added|removed|expired` con las causas
vigentes de la direccion, que lee de `POST /internal/suppression/check` con la fila del
contacto bloqueada (5 s, un intento; si falla, el evento queda sin confirmar y JetStream lo
reentrega), no con la causa ni la foto de `reasons` del evento, que puede llegar
desordenada: una queja o un rebote vigentes fijan el estado del mas grave; una baja vigente
deja `unsubscribed` a quien estaba en un estado mas grave o la acaba de registrar, y solo
entonces revoca el consentimiento. Una baja que se acaba de registrar no revoca si es
anterior al ultimo reconsentimiento con prueba de que lo pidio la persona (doble opt-in o
formulario con ip, la regla de `CheckGrant`): es la baja atrasada o reentregada que
suppression aun no retiro al recibir `contacts.contact.resubscribed`. Se compara el
`created_at` de la causa en `causes` con el `occurred_at` de `contacts.consents`; en empate
revoca; un consentimiento posterior por API o importacion no levanta la baja; sin hora
(suppression anterior a `causes`) revoca como antes. Las dos horas las pone el mismo
servidor, el de la base de la empresa que guarda los dos esquemas, asi que no hay deriva de
relojes que tolerar (si algun dia se separan, la tolerancia pasa a ser la deriva NTP entre
servidores). El consentimiento que reactiva a quien se dio de baja publica
`contacts.contact.resubscribed` con `{tenant_id, email, consented_at}` (V, 2026-09-13):
`consented_at` es el `occurred_at` de ese consentimiento, en RFC 3339 con fraccion y en
UTC, y con el suppression solo retira las bajas anteriores; publicar sin hora es un error
que revierte la transaccion del consentimiento. Sin ninguna causa que cuente `bounced`,
`complained`, `invalid` y `excluded` vuelven a `active` con el consentimiento que tuvieran y
`unsubscribed` no, ni se reconcede el consentimiento: eso solo lo hace un doble opt-in. Un
evento sin `reasons` (productor anterior) se aplica por su causa, sin degradar un estado mas
grave, con un aviso limitado en el log.

Estados de exclusion (V, 2026-09-13, `domain.ReconcileSuppression`): cada causa de
suppression tiene su estado en contacts y, con varias vigentes, el contacto toma el de la
mas grave, en el orden de `domain.Reasons()` de suppression (un test contrasta las dos
escalas). Hasta esta fecha `manual` e `invalid` no tenian estado: transactional no enviaba
nada a la direccion, pero el contacto seguia `active` y contaba como enviable en segmentos,
audiencias y en la consulta interna de enviables.

| Causa vigente | Estado | Gravedad | Lo levanta (`lifted_by`) | Revoca el consentimiento |
|---|---|---|---|---|
| `complaint` | `complained` | 5 | `operator`: retirar la causa en suppression | no |
| `hard_bounce` | `bounced` | 4 | `operator` | no |
| `unsubscribe` | `unsubscribed` | 3 | `reconsent`: doble opt-in o formulario con ip | si, al registrarse |
| `invalid` | `invalid` | 2 | `operator` | no |
| `manual` | `excluded` | 1 | `operator_or_expiry`: retirarla o que caduque | no |
| ninguna | `active` | 0 | - | - |

La baja pesa mas que `invalid` y `manual` porque es lo unico que no levanta un operador:
`unsubscribed` solo lo sustituye una causa mas grave, nunca una menor, y cuenta como baja en
vigor aunque suppression ya no la devuelva. Una baja vigente solo cuenta si ya estaba en
vigor para el contacto (estado `unsubscribed` o mas grave) o se acaba de registrar: a quien
reconsintio y aun tiene su baja vigente, una exclusion manual lo deja `excluded` (no
`unsubscribed`) y al retirarla vuelve a `active`. Una causa desconocida conserva el estado.
`invalid` y `manual` no los pidio la persona: no revocan el consentimiento, y al levantarlos el
contacto recupera el que tenia, sin escribir evidencia ni publicar
`contacts.contact.resubscribed`. El doble opt-in no se pide a `bounced`, `complained`,
`invalid` ni `excluded` (`CONTACT_NOT_REACHABLE`): la confirmacion no saldria. La regla de
enviable no cambia (`status = 'active' AND marketing_consent = 'granted'`, el indice parcial
`idx_contacts_contacts_sendable`), asi que los dos estados nuevos quedan fuera de la
audiencia y de la consulta interna de enviables; el listado y los segmentos filtran por ellos
(los valores del campo `status` salen de `domain.Statuses()`). `contacts/02_status_exclusions.sql`
amplia el CHECK de `status` (DROP IF EXISTS y ADD con el mismo nombre: idempotente, y ninguna
fila existente lo incumple). `GET /contacts/meta` publica `status_details: [{status, severity,
lifted_by}]` en el orden de `statuses`; la interfaz etiqueta cada estado por su valor y decide
el tono por `lifted_by`, sin lista de estados propia.

Caducidad y barrido (V, 2026-09-13; en suppression `app.AnnounceExpired` y `adapters/sweep`, en
contacts `app.SweepSuppression` y `adapters/sweep`): la caducidad de una exclusion manual la
anuncia su dueno. Cada `SUPPRESSION_EXPIRY_SWEEP_INTERVAL` (1m por defecto, de 1s a 1h), en una
sola replica (cerrojo de lider sobre el registro), suppression busca en cada empresa las causas
con `expires_at` ya pasado y `announced_expires_at` distinto de `expires_at` (indice parcial
`idx_suppression_entries_expiry_pending`, de `suppression/04_expiry_announced.sql`), en paginas
de 200 por `(expires_at, id)`, y anuncia cada una en su propia transaccion: bloqueo de la
direccion, reclamo por `UPDATE ... SET announced_expires_at = expires_at` condicional y
`suppression.entry.expired` por la outbox, con el payload de `added` y `removed` mas
`expires_at` en RFC 3339 y UTC. Una transaccion por causa, con el orden de bloqueos de las altas
y retiradas, para no cruzarse con una carga masiva, que bloquea filas en su propio orden. La
marca guarda la caducidad anunciada y no un booleano: una manual renovada con otra caducidad, o
reactivada sin ella, deja de coincidir y la nueva se anuncia cuando caduca, sin que ninguna
escritura limpie la marca. Es idempotente ante reintentos (sin commit no hay marca ni evento, y
la siguiente pasada lo repite) y ante replicas (el reclamo reevalua la condicion sobre la version
confirmada de la fila: solo una lo gana; el cerrojo solo evita trabajo repetido). La vigencia se
juzga con el reloj del servicio, el mismo con que `POST /internal/suppression/check` decide si la
causa sigue vigente. La marca mueve `updated_at` de la fila. Las filas anteriores a la migracion
quedan sin marca, y la primera pasada anuncia las manuales que ya habian caducado (contacts las
aplica de forma idempotente).

contacts consume `suppression.entry.expired` (durable `contacts-suppression-expired`; declara el
stream `SUPPRESSION` con `EnsureStream`, con la misma definicion que su dueno) exactamente como
`suppression.entry.removed`: con la fila del contacto bloqueada y las causas vigentes que lee de
suppression despues del bloqueo, sin revocar nunca el consentimiento. Sin causa que cuente, el
`excluded` vuelve a `active` con el consentimiento que tenia; una causa mas nueva (la manual
renovada, una queja) manda aunque el evento llegue tarde; la reentrega no cambia nada. Un evento
sin `reasons` levanta como una retirada. El barrido de los `excluded` cada
`CONTACTS_EXPIRY_SWEEP_INTERVAL` se retiro con este cambio: consultaba suppression por HTTP y su
coste crecia con los contactos excluidos de todas las empresas. La variable ya no se lee.

Garantias que quedan: el anuncio se entrega al menos una vez (outbox y JetStream, hasta 20
entregas y despues la DLQ), y lo que no llegue a aplicarse lo corrige el barrido diario de
contacts, `CONTACTS_FULL_SWEEP_AT` despues de la medianoche UTC (4h por defecto), en una sola
replica: recorre todos los contactos en paginas de 500 por id, consulta
`POST /internal/suppression/check` en bloque (tandas de 500, bajo el tope de 1000 de
suppression) y solo al contacto cuyo estado cambiaria lo vuelve a decidir con su fila bloqueada y
sus causas leidas despues del bloqueo, como un evento, publicando `contacts.contact.updated`.
Nunca revoca: sin el evento no sabe si una baja vigente sobre un `active` es nueva o ya la
levanto un reconsentimiento. Un contacto vuelve a `active` en el intervalo de suppression mas el
rele de la outbox (2 s) y la entrega; como mucho, en el barrido diario. Riesgo residual: si el
reloj de la replica de suppression que atiende la consulta de contacts va por detras del de la
que anuncio en mas que esa latencia, contacts aun ve la manual vigente y el contacto sigue
`excluded` hasta el barrido diario. El barrido diario recupera tambien lo que nunca tuvo evento:
los contactos que ya tenian una `manual` o `invalid` vigente antes de estos estados (siguen
`active` hasta la primera pasada tras el despliegue), los creados o importados antes de que el
alta consultara suppression (salvo una baja: ver la carrera residual mas abajo) y los eventos
que agotaron sus entregas.

Alta e importacion (V, 2026-09-13, `app.CreateContact`, `app.Import`,
`domain.AdmitSuppression` y `domain.ImportMayGrant`): antes de escribir, el alta por API y
cada lote de la importacion consultan `POST /internal/suppression/check` fuera de la
transaccion (el alta con su direccion; la importacion una consulta por lote de 500, bajo el
tope de 1000, asi que `CONTACTS_IMPORT_MAX_ROWS` filas son `ceil(filas / 500)` consultas de 5 s
como maximo cada una), y cada contacto nuevo entra con el estado de su causa vigente mas
grave. Es la decision de `ReconcileSuppression` con la baja vigente siempre en vigor: un
contacto sin historial no tiene reconsentimiento que la haya levantado. No se escribe
evidencia por la baja (una revocacion sobre quien nunca consintio no prueba nada; si su evento
llega despues, el consumidor la registra como a cualquier contacto). El consentimiento
declarado se decide sobre ese estado con las reglas de siempre: `CheckGrant` en el alta (la de
`POST /contacts/{id}/consent`) e `ImportMayGrant` en la importacion, que solo concede a quien
queda `active` y ademas nunca sobre una baja vigente, tambien en un contacto existente que aun
no la refleja (su estado no se toca: lo fijan sus eventos).

| Causas vigentes | Estado con que entra | Alta con consentimiento `api`, o `form` sin ip | Alta con `form` e ip | Importacion con base legal |
|---|---|---|---|---|
| ninguna | `active` | se registra; enviable | se registra; enviable | se concede |
| `unsubscribe` (sola o con `invalid`/`manual`) | `unsubscribed` | 409 `RESUBSCRIBE_REQUIRES_OPT_IN`, nada escrito | se registra, vuelve a `active` y publica `contacts.contact.resubscribed` (suppression retira la baja) | no se concede |
| `complaint` / `hard_bounce` (con o sin baja) | `complained` / `bounced` | se registra; no enviable | se registra; no enviable | no se concede |
| `invalid` | `invalid` | se registra; no enviable | se registra; no enviable | no se concede |
| `manual` | `excluded` | se registra; enviable cuando caduque o se retire | igual | no se concede |
| solo desconocidas | `active`, como un active existente | se registra | se registra | se concede |

La importacion responde, y `GET /contacts/imports[/{id}]` devuelve, `suppressed: {estado: n}`
(aditivo, siempre un objeto): de los creados, cuantos entraron ya excluidos, por estado, sin los
`active`. Lo guarda `contacts.imports.suppressed` (`jsonb` con CHECK de objeto,
`contacts/03_import_suppressed.sql`; `{}` en las importaciones anteriores, que no comprobaban
nada). La interfaz lo muestra con las etiquetas de estado de `GET /contacts/meta`.

Con suppression caido (un intento de 5 s) no se escribe ningun contacto sin comprobar: el alta
responde 503 `SUPPRESSION_UNAVAILABLE` sin escribir nada, y la importacion se corta en el lote
que no pudo comprobar con el mismo 503. Si falla desde el primer lote no escribe ningun
contacto; si cae a mitad quedan los lotes anteriores, cada uno comprobado, y el rastro queda
`failed` con lo que confirmo, como ante cualquier fallo a mitad; repetirla es idempotente (los
ya creados cuentan como omitidos o se actualizan sin conceder dos veces). Escribir el contacto
en un estado de espera no es seguro: `active` dejaria enviable a quien se dio de baja, y
`ReconcileSuppression` no cuenta la baja vigente de un `excluded` o un `invalid`, asi que el
barrido lo devolveria a `active`.

Carrera residual (no cubierta): una causa que suppression registra entre la consulta y la
confirmacion del alta o del lote, y cuyo evento se consume antes de esa confirmacion, se ignora
(el consumidor no ve la fila sin confirmar). La ventana es la transaccion del alta o del lote,
frente al intervalo de 2 s del rele de la outbox de suppression. El barrido completo la corrige,
salvo una baja: el barrido no aplica una baja vigente a un `active`, porque sin el evento no la
distingue de una que ya levanto un reconsentimiento. Por la misma razon, los contactos creados
`active` sobre una baja antes de este cambio siguen `active` (transactional no les envia nada).
Corregirlos pide una pasada que distinga las dos con el historial de consentimiento
(`UnsubscribeRevokes`) (P).
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

### 5.3 Enlaces publicos por celda (V, 2026-09-13)

`mail-security` vive por celda y firma los enlaces sin sesion del aviso de cuarentena con
`MAIL_LINK_SIGNING_KEY`. El enlace lleva la celda en la ruta
(`/api/v1/public/mail-security/quarantine/<celda>/release|discard`) y dentro de la firma
(`quarantine-link/v2`: celda, empresa, mensaje, accion, caducidad); la celda es el
`CELL_CODE` de la instancia que lo emite. El gateway enruta por ese segmento con
`MAIL_SECURITY_CELL_HOSTS` (`celda=host:puerto,...`, declarado como `cell_hosts_env` del
servicio en `routes.json`) y no tiene la clave: una celda que no figura va al destino base
(`MAIL_SECURITY_HOST`, la celda `GATEWAY_BASE_CELL_CODE`), y cada instancia solo acepta enlaces de su
propia celda, con la misma pagina 403 que una firma mala. Un segmento manipulado solo
llega a una celda que lo rechaza. Con una sola celda la variable queda vacia. No hay forma
sin celda: el gateway no arranca con una ruta publica de un servicio de celda que no lleve
`{cell}`. Las rutas con sesion de los servicios de celda van por la empresa (5.4); el webmail,
todavia no (5.5).

### 5.4 Rutas con sesion por celda (V, 2026-09-13)

`mail-directory` y `mail-security` declaran `cell_hosts_env` en `routes.json`
(`MAIL_DIRECTORY_CELL_HOSTS`, `MAIL_SECURITY_CELL_HOSTS`): TODAS sus rutas se enrutan por celda.
El gateway no arranca si un servicio de celda es el destino de un prefijo `self_authenticated`
o del `frontend` (no llevan empresa verificada ni celda en la ruta), si no tiene ninguna ruta o
si falta `organization` entre los servicios.

* Celda de la empresa: el token lleva la empresa, no la celda (`v_tenants` excluye `cell_id`).
  El gateway la pregunta a `organization` en `GET /internal/organization/tenants/{id}/cell`
  (token interno y `RequireInternalCaller`: 403 a una peticion con usuario; responde
  `{tenant_id, cell_code}` o 404 `TENANT_NOT_FOUND`, nunca el host de la base; la ruta interna
  queda fuera del limitador por IP de organization, porque todas sus consultas salen del
  gateway). Cache en el gateway: LRU de 10 000 empresas, 5 minutos la respuesta positiva, 30 s
  la negativa, una sola consulta para las peticiones simultaneas de la misma empresa. Si
  organization no responde vale la ultima celda que dio, hasta una hora despues de caducar
  (`cell_resolution_stale_total`); nunca una negativa ni una suposicion.
* Destinos: `GATEWAY_BASE_CELL_CODE` es la celda que sirven los destinos base
  (`<SERVICIO>_HOST`); `<SERVICIO>_CELL_HOSTS`, las demas. El enrutado es el ultimo paso, despues
  de la sesion, el RBAC y el rastro de auditoria: una peticion denegada no pregunta la celda.
* Falla cerrado sin salir hacia ninguna instancia: sesion sin empresa valida o empresa que
  organization no conoce, 403; celda sin respuesta aplicable, 503 `CELL_UNAVAILABLE`; celda
  sin instancia declarada de ese servicio, 503 `CELL_UNAVAILABLE`.
  `cell_routing_failures_total{service, reason}` con `unknown_tenant`, `unresolved` y
  `not_served`.
* Arranque: con alguna instancia declarada, `GATEWAY_BASE_CELL_CODE` es obligatorio, con forma
  de codigo de celda y sin repetirse como instancia. Una celda declarada para un servicio de
  celda y no para otro se avisa en el registro; no impide arrancar (una celda puede abrirse
  servicio a servicio) y sus empresas reciben 503 en el que falta.
* Una celda: sin `GATEWAY_BASE_CELL_CODE` ni instancias todo va al destino base y el gateway no
  pregunta a organization; no hace falta configuracion nueva. En ese modo una empresa de una
  celda registrada despues llegaria a la celda base, que la rechaza (punto siguiente):
  `GATEWAY_BASE_CELL_CODE` se fija en cuanto el registro tiene una segunda celda, aunque aun no
  tenga instancias, y entonces sus empresas reciben 503 en el gateway en vez de 403 en la celda.
* Segunda barrera en la celda (V, 2026-09-13, `pkg/tenantcell`): el enrutado del gateway no es
  la unica defensa. `mail-directory` y `mail-security` montan `tenantcell.Membership` detras de
  `RequireGatewayToken` e `InjectFromGateway` y antes de cualquier ruta. Toda peticion que actua
  por una empresa (trae `X-Tenant-ID`, sea de una persona por el gateway o de domain-service, o
  trae usuario) solo pasa si organization situa esa empresa en el `CELL_CODE` de la instancia,
  por la misma ruta interna y con la misma cache que el gateway (`tenantcell.Resolver`, que el
  gateway tambien usa). Empresa de otra celda, empresa que organization no conoce, persona sin
  empresa o identificador mal formado: 403 `TENANT_NOT_IN_CELL`, sin llegar a ningun handler, sin
  datos y con `Cache-Control: no-store`. Sin respuesta aplicable: 503 `CELL_UNAVAILABLE`. Con
  organization caido solo pasan las empresas ya comprobadas en esa instancia, hasta una hora
  despues de caducar su entrada; la ultima respuesta de otra celda sigue rechazando y una empresa
  sin comprobar nunca pasa (tras reiniciar la instancia con organization caido, ninguna). Sin
  empresa ni usuario no hay nada que comprobar: la consulta de remitentes del webmail y los
  enlaces publicos de cuarentena (atados a la celda por su firma, 5.3) pasan, y los listeners de
  los motores (`mail-auth`, mapas y exportacion de `mail-policy`) no pasan por la barrera. Los dos
  servicios no arrancan sin `CELL_CODE` valido ni `ORGANIZATION_URL`, ni sin token interno fuera
  de desarrollo o prueba. Metrica `cell_membership_refusals_total{reason}` (`foreign_tenant`,
  `unknown_tenant`, `unresolved`, nacen a cero) y alerta `EmpresaEnCeldaAjena` al primer
  `foreign_tenant`, que es siempre un error de despliegue. Descartado: que organization copie en
  la base de la celda la lista de sus empresas al darlas de alta. Seria organization escribiendo
  en el esquema de mail-directory, o necesitaria un mapa de instancias por celda en organization
  (el problema de domain-service, abajo), y las empresas ya existentes pedirian un relleno; a
  cambio solo se ganaria sobrevivir a un reinicio de la instancia con organization caido.
* Probado: unitarias del gateway (cada ruta con sesion de cada servicio de celda, una celda sin
  consultas a organization, validacion de la tabla y del entorno), de `pkg/tenantcell`
  (resolucion, cache y su cota, caducidad, respuesta negativa, margen con organization caido,
  respuestas fuera de contrato, consultas simultaneas; barrera con empresa de la celda, de otra
  celda, desconocida o mal formada, persona sin empresa, peticion sin empresa, organization caido
  con y sin la empresa comprobada y configuracion que falla cerrado) y de organization (contrato
  de la ruta interna y caso de uso); las rutas de los dos servicios tal como las monta `main`
  (cada ruta, como persona y como servicio, rechaza a una empresa de otra celda y a una
  desconocida sin ejecutar ningun handler); `make e2e` con un segundo gateway: acme (pe-01) va al
  destino base, beta (pe-02) a una segunda instancia de `mail-directory` sobre `mail_cell_pe_02`,
  gamma (pe-03, sin instancias) recibe 503 sin llegar a ninguna y la metrica lo cuenta; beta
  recibe 503 en `mail-security`, que no declara pe-02. Por el primer gateway, sin celdas, beta
  llega a las instancias de pe-01, que responden 403 `TENANT_NOT_IN_CELL` a una lectura, a un
  relayhost, a los ajustes de cuarentena y a la activacion interna con su empresa sin dejar
  ninguna fila en `mail_cell_pe_01`; la instancia de pe-02 rechaza a acme y las metricas lo
  cuentan. `make e2e-mail`: los servicios de celda en contenedores preguntan a organization en el
  host.

Pendiente (P):

* El superadmin opera sobre la celda de su empresa (`platform`): el cortafuegos de otra celda
  (`/api/v1/mail-security/firewall/*`) no se alcanza por el gateway. Diseno: una celda de destino
  explicita que el gateway acepte solo con el rol `superadmin` y solo si organization la tiene
  registrada.
* Llamadas entre servicios hacia la celda: `domain-service` activa dominios y entrega DKIM en
  una sola instancia (`MAIL_DIRECTORY_URL`, `MAIL_SECURITY_URL`). Con varias celdas, la instancia
  responde 403 `TENANT_NOT_IN_CELL` a la empresa de otra celda y el paso falla en vez de escribir
  en la celda equivocada (antes, desactivar respondia 404 y domain-service lo daba por hecho).
  Diseno: `tenantcell.Resolver` y un mapa de instancias por celda en `domain-service`, fallando
  cerrado igual que el gateway.
* Traslado de una empresa de celda: el gateway y las instancias de celda lo ven al caducar su
  entrada (5 minutos); falta invalidarla con el evento del traslado.

### 5.5 Webmail por celda (P)

`webmail` sigue yendo al destino base: con varias celdas solo los buzones de la celda base usan
el webmail por el gateway. Un buzon de otra celda recibe el mismo 401 que una contrasena mala
(`mail-auth` de la celda base no lo conoce): no hay datos cruzados ni se revela si el buzon
existe. La tabla no admite declarar el webmail como servicio de celda hasta que esto exista.

El webmail no monta la segunda barrera de 5.4 y no la necesita (V, decision del 2026-09-13): no
recibe empresa del gateway (prefijo `self_authenticated`, sin `X-Tenant-ID` ni usuario de la
plataforma), su identidad es un buzon que `mail-auth` verifica contra el directorio de su propia
celda, y su sesion (claves `webmail:<CELL_CODE>:`) y el usuario maestro de Dovecot son de esa
celda. Un buzon de otra celda no existe ahi y recibe 401; no hay una empresa que el cliente
controle ni forma de escribir en otra celda. Lo que falta aqui es enrutar, no aislar. Su consulta
de remitentes a `mail-directory` va sin empresa y pasa la barrera, y `MAIL_DIRECTORY_URL` debe
ser la instancia de su misma celda (un error ahi deja al buzon sin remitentes, no con los de
otro).

Diseno:

1. Indice global dominio -> celda en el registro: tabla de organization con el dominio (unico),
   la empresa y la celda, que `domain-service` escribe por la API interna de organization al
   activar o retirar un dominio, y `GET /internal/organization/mail-domains/{dominio}/cell`. Da
   ademas la unicidad de los dominios activos entre celdas, que hoy solo se garantiza dentro de
   una (`mail.name_in_use`).
2. Inicio de sesion (`POST /api/v1/webmail/session`, sin cookie): el gateway lee `username` del
   cuerpo (tope de 8 KiB, el del servicio), resuelve su dominio con cache y reenvia el cuerpo a
   la instancia de esa celda. Un dominio desconocido va a la celda base, que responde el mismo
   401 que a una contrasena mala: el gateway no responde nada propio. Los limitadores (el
   estricto del gateway y el de `mail-auth` por buzon e IP real) no cambian.
3. Despues del inicio: el token de `cf_wm` lleva la celda como prefijo (`<celda>.<256 bits>`),
   con los mismos atributos de cookie. El gateway enruta por el prefijo y el webmail rechaza un
   token cuyo prefijo no es su `CELL_CODE` antes de buscarlo; un prefijo cambiado solo llega a
   otra celda, donde la sesion no existe (claves `webmail:<celda>:`), o a la base si la celda es
   desconocida: 401 y la cookie se borra. `DELETE /session` igual.
4. La autenticacion y la CSP del servicio no cambian: sin JWT ni RBAC en el prefijo y con la
   politica del servicio (`keepUpstreamCSP`).

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
| Un enlace publico de cuarentena solo actua en la celda que lo firmo: la celda va en la firma, el gateway enruta sin la clave y una celda desconocida responde igual que una firma mala (5.3) | V (2026-09-13) |
| Una peticion con sesion a un servicio de celda solo llega a la instancia de la celda de su empresa; sin celda resoluble o sin instancia declarada no sale hacia ninguna (5.4) | V (2026-09-13; con `GATEWAY_BASE_CELL_CODE`) |
| Una instancia de celda solo atiende a las empresas de su celda aunque le lleguen de otra (gateway mal configurado, llamada de servicio a la instancia equivocada): pregunta a organization y rechaza con 403 antes de cualquier ruta, sin escribir nada; con organization caido solo las ya comprobadas (5.4) | V (2026-09-13, `mail-directory` y `mail-security`; el webmail no la necesita, 5.5) |
| El webmail de un buzon se sirve en la celda de su dominio (5.5) | P |
| Un servicio de empresa solo abre su esquema y no el registro (credencial por servicio) | P (5.2) |
| Respaldo por base y restauracion probada semanalmente (`ops/backup`) | V (scripts), P (programados en este entorno) |
