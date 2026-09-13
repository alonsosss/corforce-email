# Modelo de datos, multitenencia y celdas

Este documento separa lo **verificado en el codigo** (V) de lo **propuesto** (P). Si
implementas algo marcado P, muevelo a V en la misma tarea.

## 1. Tres planos de datos

| Plano | Base | Quien la lee | Contenido | Migraciones |
|---|---|---|---|---|
| Registro | `mail_registry` (una) | identity, access-control, organization; el gateway indirectamente | empresas, celdas, usuarios, sesiones, roles, permisos, catalogo de modulos, (P) planes | `migrations/registry/` |
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

El registro no tiene RLS: los tres servicios que lo leen filtran por `tenant_id` en cada
consulta y los handlers comprueban que el recurso pedido por id pertenece a la empresa de
la sesion (V en identity, access-control y organization).

## 3. Celda

### 3.1 Esquema `mail` (V: migracion; P: servicios que lo escriben)

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
como dueno porque resuelven identidades sin empresa previa, y lo dicen en un comentario.

`mail_security` (`migrations/cell/canonical/mail-security/01_mail_security.sql`) sigue el
mismo patron: `tenant_isolation` por `tenant_id = mail_security.current_tenant()` para
`mail_app` en sus nueve tablas, sin `FORCE`. Su API de administracion corre en
`TransactRLS` y filtra por `tenant_id`; los endpoints de motores (`/pipe`, `/settings`...)
consultan como dueno y atribuyen cada fila de cuarentena a la empresa del buzon final.
Probado contra Postgres: una empresa no ve umbrales, ajustes ni cuarentena de otra.

`03_mail_app_policies.sql` (mail-directory) anade lo que el primer consumidor necesito:
`app_delete` sobre `quota_usage` (solo del buzon propio, por eso el servicio borra la cuota
antes que el buzon), `WITH CHECK` en `transports` que admite `tenant_id NULL` solo con
`app.is_privileged` (que ademas sea el superadmin lo exige el servicio), y
`mail.name_in_use(text)` (SECURITY DEFINER) para saber si un nombre ya es dominio o dominio
alias de otra empresa sin ver sus filas. `mail-directory` abre TODA lectura y escritura en
`db.TransactRLS`; su ruta interna de activacion (la llama domain-service con `X-Tenant-ID`,
sin usuario) corre sin cambio de rol y ahi solo protege el filtro por `tenant_id`. Probado
con dos empresas en `services/mail-directory/internal/adapters/postgres/integration_test.go`.

## 4. Empresa (V)

Base `mail_tenant_<slug>` creada por `organization` al dar de alta la empresa
(`CREATE DATABASE` + todas las canonicas en orden, con advisory lock sobre conexion
directa sin PgBouncer, `public.schema_migrations`, baseline para bases preexistentes).
Barrido en segundo plano al arrancar (`RUN_TENANT_MIGRATIONS`). Hoy contiene `audit`,
`scheduler`, `domains`, `suppression` (lista de exclusiones de envio: una fila por
direccion y empresa, causa vigente por orden de gravedad, consulta previa a todo envio
por `POST /internal/suppression/check`) y `templates` (plantillas de correo por empresa:
`templates.templates` con nombre unico por empresa y `current_version`, y
`templates.versions` con asunto, HTML, texto opcional y variables declaradas en `jsonb`;
una sola version publicada por plantilla garantizada por el indice parcial
`uq_versions_one_published`, las anteriores quedan `superseded`; renderizado interno por
`POST /internal/templates/{id}/render` y evento `templates.template.published` por la
outbox).

## 5. Enrutado por peticion y por celda (V)

El gateway pone `X-Tenant-ID` desde el JWT; `db.TenantPoolMiddleware` resuelve la empresa
en `organization.tenants` unida a `organization.cells` (`db_name`, `db_host`, `db_port`;
cache en memoria) y abre un pool perezoso por destino (`MaxConns 10`, `MinConns 0`,
reciclado a los 5 minutos ociosos). Una celda cuyo `db_host` coincide con `POSTGRES_HOST`
es el cluster por defecto; cualquier otra abre su pool contra su propio host con la misma
credencial de plataforma (`db.NewTenantRouting` lo cablea en cada `main`). `organization`
crea y migra la base de una empresa nueva en el cluster de su celda (conexion directa a la
base de mantenimiento de la celda). Los servicios de celda abren su base por
`CELL_DB_NAME` (`db.NewNamedPool` + `db.StaticPoolMiddleware`).

P: credencial distinta por celda (hoy una sola de plataforma) y mover una empresa de
celda (`TenantPoolManager.Forget` ya invalida la cache; falta el traslado de datos).

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
| RLS en la celda para los servicios Go | V (politicas y roles; `mail-directory` las usa en toda lectura y escritura) |
| Una celda no lee otra celda (los servicios de celda solo abren `CELL_DB_NAME`); claves de cifrado por celda | V / P (claves) |
| Respaldo por base y restauracion probada semanalmente (`ops/backup`) | V (scripts), P (programados en este entorno) |
