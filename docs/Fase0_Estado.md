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
| Los motores de `deploy/mail/` levantan contra el esquema `mail` | Pendiente: copia y adaptación a PostgreSQL en curso | `docker compose -f deploy/mail/docker-compose.mail.yml up` con `MAIL_DB_*` |
| `ERP/` y `mailcow/` borrados | Pendiente hasta cerrar lo anterior | |

## Deuda conocida que sale de la copia (no bloquea la fase 0)

* `pkg/events` sin outbox: las publicaciones críticas se hacen tras el commit y pueden
  perderse si el proceso cae entre ambos. Decisión: outbox en `pkg/events` antes de la
  fase 3 (envíos).
* `pkg/auth` firma HS256 con un secreto compartido por todos los servicios. Decisión:
  pasar a EdDSA con `kid` cuando exista más de un emisor; hoy solo firma identity.
* Falta `RequirePermission(module, resource, action)` en `pkg/middleware` (tercera capa);
  los handlers usan `RequireRoles(tenant_admin)` o `IsPrivileged`.
* `pkg/db` resuelve el DSN de empresa contra un solo cluster; el enrutado por celda
  (`cells.db_host/db_port`) está diseñado pero no implementado
  (`Modelo_de_Datos_y_Celdas.md`, sección 5).
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
