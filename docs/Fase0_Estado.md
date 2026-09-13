# Fase 0: estado de la copia

Criterio de cierre (de `CLAUDE.md`) y estado real. Se actualiza en la misma tarea que
cambie cualquiera de estas líneas.

| Criterio | Estado | Cómo se comprueba |
|---|---|---|
| El repositorio compila y `go vet` está limpio | Cumplido | `go build ./... && go vet ./...` |
| Pasan los tests | Cumplido | `go test ./... -race` (pkg, gateway, identity, access-control, organization, audit) |
| No queda `capitalpillar`, `erp`, `sunat`, `sucursal`, `mysql` en código | Cumplido | `make clean-copy` |
| `make checks` en verde (migraciones, acoplamiento, streams, imágenes, eventos, secretos, scaffold) | Cumplido | `make checks` |
| Migraciones del registro aplican desde cero y se re-ejecutan sin error | Cumplido (Postgres 16, 2026-09-12) | `ops/db/apply-migration.sh` sobre una base vacía, dos veces |
| Migraciones de empresa (`audit`, `scheduler`) aplican desde cero y se re-ejecutan | Cumplido (Postgres 16, 2026-09-12) | idem |
| Migración de celda (`mail`) aplica desde cero y se re-ejecuta; `mail_engine` sin acceso a `app_passwords` | Cumplido (Postgres 16, 2026-09-12) | idem |
| Se aprovisiona una celda y una empresa de punta a punta y su `tenant_admin` inicia sesión por el gateway | Cumplido (2026-09-12, binarios reales contra Postgres/NATS/Redis desechables: login superadmin, `POST /organizations` crea `mail_tenant_acme` y siembra `tenant_admin`, login del admin con sus permisos, 403 en `/cells`, 401 sin token) | `make e2e` (también en CI): plataforma vacía con `bootstrap-platform.sh`, empresa, acceso por el gateway, dominio y buzón en la celda, Redis de los motores, plantillas, supresión, planes y derechos de billing, autorización de envío con límite por hora en reputation, alcance de permisos (los de plataforma no llegan a un rol de empresa), contactos con consentimiento y audiencia, y el panel de analítica; 59 comprobaciones en verde (2026-09-13) |
| Los motores de `deploy/mail/` levantan contra el esquema `mail` | Parcial: copiados y adaptados a PostgreSQL (153 ficheros, compose validado, scripts con sintaxis comprobada); las 20 consultas de Postfix y Dovecot ejecutadas como `mail_engine` contra el esquema real sin errores (2026-09-12). Levantar la pila completa exige `mail-auth` (Dovecot no autentica sin él) y `mail-policy` (Rspamd no arranca sin su mapa `settings`), que son de la fase 2 | `docker compose -f deploy/mail/docker-compose.mail.yml up` con `MAIL_DB_*`, `postmap -q`, `doveadm user` |
| `ERP/` y `mailcow/` borrados | Los clones viven fuera del repositorio (scratchpad de sesión) y `.gitignore` los excluye si se clonan dentro; se borran al cerrar el punto anterior | |

## Pendientes que dejan los motores (fase 2)

* `mail-auth` implementado y declarado en `docker-compose.yml` en la red `mail-engines` con
  alias `mail-auth` (probado contra el esquema `mail` real y con Redis, 2026-09-12). Los
  mapas HTTP de los motores (8081/9081) y el contrato Redis los sirve `mail-security` con
  alias `mail-policy`.
* Tablas que los motores esperan y el esquema aún no tiene: `mta_sts` (acme). La
  cuarentena, las políticas antispam por objeto, el pie por dominio y las alias internas
  viven en `mail_security` o se leen de las vistas `mail.v_routing_*` (probado contra
  Postgres y Redis reales, 2026-09-12).
* `mail-security` ya admite varias réplicas: el `Last-Modified` de `/settings` vive en la
  base de la celda (`mail_security.engine_documents`, migración 03) y lo adelanta, bajo
  `FOR UPDATE`, la réplica que ve un contenido nuevo; todas responden con la misma marca
  (probado con dos réplicas contra Postgres, 2026-09-13).
* `mail-security` escribe `SMTP_LIMITED_ACCESS` / `SMTP_ALLOW_NETS_<usuario>` (redes SMTP
  por buzón, `/api/v1/mail-security/smtp-access`, migración 04) y las claves `F2B_WHITELIST`,
  `F2B_BLACKLIST`, `F2B_OPTIONS` y `F2B_QUEUE_UNBAN` de netfilter (cortafuegos de la celda,
  solo superadmin, `/api/v1/mail-security/firewall/*`, migración 05), con reconciliación
  desde la base. Sus eventos y los de `mail-directory` salen por `pkg/outbox` (2026-09-13).
* Pendiente en `mail-security`: el aviso de cuarentena al buzón (hay ajustes y columna
  `notified`, falta el emisor; necesita `transactional`, contrato en
  `deploy/mail/README.md`). `clean_q_aged.sh` de Dovecot no hace nada (busca
  `mail.quarantine`, que no existe): la poda la hace el servicio.
* El gateway debe servir `/.well-known/acme-challenge/` o usarse `ACME_DNS_CHALLENGE=y`.
* Primer despliegue: smoke test con `postmap -q` y `doveadm user` sobre la celda.

## Deuda conocida que sale de la copia (no bloquea la fase 0)

* Outbox disponible en `pkg/outbox` (probado contra Postgres): los servicios copiados en
  fase 0 siguen publicando tras el commit; se migran a `Enqueue` cuando se toquen. Los
  servicios nuevos lo usan desde el principio para sus publicaciones críticas. Los de celda
  (`mail-directory`, `mail-security`) encolan en `platform.event_outbox` de la celda y la
  vacía un solo relé por celda (`Relay.RunExclusive` con `db.TryLeaderLock` y
  `outbox.CellRelayLockKey`, compartida por los dos).
* `pkg/auth` firma HS256 con un secreto compartido por todos los servicios. Decisión:
  pasar a EdDSA con `kid` cuando exista más de un emisor; hoy solo firma identity.
* La tercera capa (`pkg/authz.RequirePermission`) la usan los servicios nuevos y todo el
  plano de control: identity, organization, audit, scheduler y access-control (este en
  proceso, con su caso de uso de política) exigen el permiso de acción concreto. Ningún
  handler queda con `IsPrivileged` o `RequireRoles(tenant_admin)` como única comprobación.
* Enrutado por celda implementado (`db.NewTenantRouting`, aprovisionamiento en la celda de
  la empresa). Queda una credencial por celda (hoy una sola de plataforma).
* Ciclo de vida de una ejecución del scheduler cerrado (2026-09-13, unitarias e integración
  contra Postgres 16): `scheduler.job.started`, `.completed` y `.failed` salen por la outbox
  en la transacción que cambia la ejecución (stream `SCHEDULER`); el ejecutor cierra por
  `POST /internal/scheduler/executions/{id}/complete|fail` y un barrido con
  `db.TryLeaderLock` vence por timeout y despacha los reintentos con espera creciente. El
  catálogo de manejadores (`services/scheduler/handlers.json`) está vacío porque ningún
  servicio consume hoy `scheduler.job.started`: hasta que un ejecutor se declare, crear o
  editar un trabajo responde 422. Pendiente del scheduler: las tareas puntuales
  (`scheduled_tasks`) se marcan `executed` sin despachar nada y `cron_expression` no se
  evalúa (un trabajo `cron` corre cada hora).
* Contrato JSON del scheduler fijado en snake_case (DTOs del adaptador HTTP, con test de
  contrato; la duración sale como `duration_ms`). `web/` todavía no lo consume.
* Lecturas del registro que todavía van a tablas ajenas, sancionadas con motivo en
  `ops/scaffold/coupling-allowlist.txt`: `identity` lee `organization.tenants` (slug y
  nombre de la empresa) y `access-control` lee `organization.module_catalog` y
  `tenant_modules`; faltan las vistas de `organization`. `organization` además escribe en
  `identity` y `access_control` al sembrar y borrar una empresa
  (`coupling-writes-allowlist.txt`); falta que esos servicios expongan la operación.

## Lo que la fase 0 dejó mejor que la base

* Gateway con tabla de rutas en datos (`routes.json`) y validación al arrancar.
* Límites de peticiones del gateway comunes a todas sus réplicas: el general de `/api/v1`
  (`API_RATE_LIMIT_PER_MIN`) y el estricto de autenticación (`AUTH_RATE_LIMIT_PER_MIN`:
  `/auth` y `POST /api/v1/webmail/session`) cuentan por IP en el Redis de la plataforma
  (`REDIS_*`); con Redis caído cada réplica decide en memoria con el mismo cupo y lo cuenta
  en `rate_limit_degraded_total`. Probado con dos réplicas contra Redis 7 real y con Redis
  inalcanzable (2026-09-13). Los límites propios de los demás servicios siguen en memoria
  (motivo en `docs/arquitectura/CSP-Y-SESION.md`).
* RBAC en `enforce` y `fail-closed` por defecto.
* Dos bugs de identity corregidos: el cambio de contraseña propio no persistía y el logout
  no revocaba la sesión en servidor; el historial de contraseñas ahora se aplica.
* Roles del sistema reducidos a dos; ningún nombre de rol de negocio en código.
* Directorio de correo por celda con rol `mail_engine` de mínimo privilegio.
