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
| Se aprovisiona una celda y una empresa de punta a punta y su `tenant_admin` inicia sesión por el gateway | Cumplido (2026-09-12, binarios reales contra Postgres/NATS/Redis desechables: login superadmin, `POST /organizations` crea `mail_tenant_acme` y siembra `tenant_admin`, login del admin con sus permisos, 403 en `/cells`, 401 sin token) | `POST /cells`, `POST /organizations`, `POST /auth/login`, `GET /access/my-modules` |
| Los motores de `deploy/mail/` levantan contra el esquema `mail` | Parcial: copiados y adaptados a PostgreSQL (153 ficheros, compose validado, scripts con sintaxis comprobada); las 20 consultas de Postfix y Dovecot ejecutadas como `mail_engine` contra el esquema real sin errores (2026-09-12). Levantar la pila completa exige `mail-auth` (Dovecot no autentica sin él) y `mail-policy` (Rspamd no arranca sin su mapa `settings`), que son de la fase 2 | `docker compose -f deploy/mail/docker-compose.mail.yml up` con `MAIL_DB_*`, `postmap -q`, `doveadm user` |
| `ERP/` y `mailcow/` borrados | Los clones viven fuera del repositorio (scratchpad de sesión) y `.gitignore` los excluye si se clonan dentro; se borran al cerrar el punto anterior | |

## Pendientes que dejan los motores (fase 2)

* `mail-auth` implementado y declarado en `docker-compose.yml` en la red `mail-engines` con
  alias `mail-auth` (probado contra el esquema `mail` real y con Redis, 2026-09-12). Los
  mapas HTTP de los motores (8081/9081) y el contrato Redis los sirve `mail-security` con
  alias `mail-policy`.
* Tablas que los motores esperan y el esquema aún no tiene: `quarantine` y las políticas
  antispam por objeto (`mail_security`), pie de página por dominio, `mta_sts` (acme).
* El gateway debe servir `/.well-known/acme-challenge/` o usarse `ACME_DNS_CHALLENGE=y`.
* Primer despliegue: smoke test con `postmap -q` y `doveadm user` sobre la celda.

## Deuda conocida que sale de la copia (no bloquea la fase 0)

* Outbox disponible en `pkg/outbox` (probado contra Postgres): los servicios copiados en
  fase 0 siguen publicando tras el commit; se migran a `Enqueue` cuando se toquen. Los
  servicios nuevos lo usan desde el principio para sus publicaciones críticas.
* `pkg/auth` firma HS256 con un secreto compartido por todos los servicios. Decisión:
  pasar a EdDSA con `kid` cuando exista más de un emisor; hoy solo firma identity.
* La tercera capa existe (`pkg/authz.RequirePermission`) y la usan los servicios nuevos;
  los copiados en fase 0 (identity, access-control, organization, audit, scheduler) siguen
  con `RequireRoles(tenant_admin)` o `IsPrivileged` y se migran cuando se toquen.
* Enrutado por celda implementado (`db.NewTenantRouting`, aprovisionamiento en la celda de
  la empresa). Queda una credencial por celda (hoy una sola de plataforma).
* El scheduler crea ejecuciones y publica `scheduler.job.started`, pero ningún ejecutor
  las cierra: hace falta definir el consumidor.
* El scheduler serializa sus entidades en PascalCase (sin etiquetas JSON); fijar el
  contrato antes de que lo consuma `web/`.
* `identity` lee `access_control.roles`/`user_roles` por join directo dentro del registro;
  conviene una vista publicada `v_user_roles`.
* Rate limiting en memoria por proceso (no se comparte entre réplicas del gateway).

## Lo que la fase 0 dejó mejor que la base

* Gateway con tabla de rutas en datos (`routes.json`) y validación al arrancar.
* RBAC en `enforce` y `fail-closed` por defecto.
* Dos bugs de identity corregidos: el cambio de contraseña propio no persistía y el logout
  no revocaba la sesión en servidor; el historial de contraseñas ahora se aplica.
* Roles del sistema reducidos a dos; ningún nombre de rol de negocio en código.
* Directorio de correo por celda con rol `mail_engine` de mínimo privilegio.
