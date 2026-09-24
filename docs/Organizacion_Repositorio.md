# Organización del repositorio

Qué se copió de cada base en la fase 0, qué no, qué nace nuevo y dónde va cada cosa.
Después de la fase 0 no se vuelve a copiar nada: lo que interese se reimplementa.

## 1. Estructura

```
corforce-email/
├── pkg/                    kernel compartido (del ERP, limpio)
├── services/               un directorio por microservicio, molde hexagonal
│   ├── gateway/            única entrada; tabla de rutas en routes.json
│   ├── identity/           usuarios, sesiones, MFA, contraseñas
│   ├── access-control/     roles, permisos (module, resource, action), gateo de módulos
│   ├── organization/       empresas, celdas, módulos, aprovisionamiento y migraciones
│   ├── audit/              rastro append-only con cadena de hashes
│   └── scheduler/          trabajos y tareas por empresa
├── migrations/
│   ├── registry/           plano de control (mail_registry)
│   ├── cell/canonical/     directorio de correo por celda (mail_cell_<code>)
│   └── tenant/canonical/   por empresa (mail_tenant_<slug>)
├── deploy/mail/            motores de correo (de mailcow, adaptados a PostgreSQL)
├── ops/                    scaffold, secretos, respaldos, observabilidad, ECR, AWS, servidor
├── scripts/                despliegue desde el PC
├── pgbouncer/              pooler con TLS al upstream
├── docs/                   documentos rectores
├── web/                    (fase 1) aplicación React + TypeScript
├── docker-compose.yml      plano de control e infraestructura de desarrollo
└── Makefile                build, checks, scaffolding
```

## 2. Del ERP de referencia (`github.com/alonsosss/ERP`)

### 2.1 Se copió, una vez, y se limpió

| Origen | Destino | Cambios en la copia |
|---|---|---|
| `pkg/{response,observability,events,httpclient,crypto,validate,totp,sqlsource,auth,server,config,middleware,db,objectstore,oauth2}` | `pkg/` | Módulo renombrado; emisor JWT `core-force-mail`; roles del sistema `superadmin`/`tenant_admin` en vez de la lista del ERP; RLS con `app.is_privileged` (sin `app.is_admin`); rol RLS `mail_app`; eliminados los resolutores de ecommerce de `pkg/db`; CSP sin dominios ajenos; almacén de objetos sin bucket ni URL por defecto |
| `services/identity` | `services/identity` | Sin sucursales, zonas, empleados, socios, portal ni roles base; `TransactionalMailer` apunta al servicio transaccional propio; emisor MFA "Core Force Mail" |
| `services/access-control` | `services/access-control` | Sin sucursales, capacidades ni términos por sector; gateo de módulos por `module_catalog.permission_modules`; contrato `my-modules` nuevo |
| `services/organization` | `services/organization` | Solo empresas, celdas (nuevo), módulos, aprovisionamiento y migraciones; fuera sucursales, zonas, departamentos, país, sectores, paneles |
| `services/audit`, `services/scheduler` | igual | Subjects auditados por configuración; barrido concurrente por empresa; esquema `scheduler` propio |
| `services/gateway` | `services/gateway` | Reescrito: rutas en `routes.json`, RBAC sin portal ni lecturas auxiliares, sin edge/WS/ecommerce |
| `migrations/registry/001_init.sql` y las columnas añadidas después | `migrations/registry/001..005` | Consolidadas y renumeradas desde 1; sin sucursales, país ni SUNAT |
| `migrations/tenant/canonical/audit` | igual | Renumeradas 01..03; sin triggers a tablas del ERP |
| `ops/scaffold/{check-migrations,check-migration-drops,service-paths,check-coupling,check-sql-arity,check-silent-errors,check-base-images,check-streams,validate,new-service,gen-events,eventcontracts}` | `ops/scaffold/` | `validate.sh` lee `routes.json`; `check-coupling.sh` con nuestros esquemas; listas de excepciones vacías; nuevo `check-clean-copy.sh` |
| `ops/security/secrets/*` | igual | `secret-keys.txt` reescrito para este producto; rutas `core-force-mail` |
| `ops/{db,backup,observability,ecr,aws,server-template,maintenance}` | igual | Sin padrón SUNAT ni alertas de planta; prefijos `mail_` |
| `pgbouncer/`, `docker-compose.observability.yml`, `.github/workflows/*`, `scripts/deploy-ecr.sh`, `scripts/check-image-drift.sh` | igual | Nombres nuevos; CI sin frontend federado ni agentes de borde |
| `docs/arquitectura/{CSP-Y-SESION,OBSERVABILIDAD,EVENTS}.md` | igual | EVENTS.md se regenera con `make gen-events` |

### 2.2 No se copió

Todo el dominio del ERP (ventas, facturación, inventario, compras, finanzas, contabilidad,
RR. HH., producción, planta, calidad, logística, ecommerce, CRM, anuncios, IA), sus 620
migraciones canónicas, el frontend federado y sus 26 remotes, `mobile/`, `edge/`,
`extensions/`, `storefront-*`, `deploy/lycet`, `ops/sunat-padron`, `pkg/{country,
branchscope, approvals, cssguard, textsearch, orgname, previewtoken, channelgrant}` y
los paquetes de dominio de `pkg/`.

Los servicios de correo del ERP (`crm-email`, `crm-marketing`, `notification`) tampoco:
enviaban por SMTP sin SDK de SES, sin tracking, con el webhook de rebotes sin enrutar y
sin verificar la firma SNS. Se reimplementan (ver `Arquitectura_Core_Force_Mail.md`); de
ellos se rescatan solo ideas: supresión, baja RFC 8058 firmada, claim-before-send y la
correlación de rebotes.

## 3. De mailcow-dockerized

### 3.1 Se copió a `deploy/mail/`

Postfix (`main.cf`, `master.cf`, mapas generados por `postfix.sh`), Dovecot (`dovecot.conf`,
carpetas, sieve global, FTS, `passwd-verify.lua`), Rspamd (`local.d`, `override.d`, `lua`,
`custom`), Unbound, ClamAV, Olefy, postfix-tlspol, netfilter, acme, watchdog, dockerapi,
Redis (`redis-conf.sh`) y los cron de Dovecot. Los detalles y adaptaciones están en
`deploy/mail/README.md`.

### 3.2 No se copió

`data/web` (UI y API PHP), `init_db.inc.php` (esquema MySQL), SOGo, php-fpm, nginx,
memcached, MySQL, `dynmaps/*.php`, `meta_exporter/*.php`, `mailcowauth.php`, LDAP,
imapsync, `update.sh`.

Su esquema sirvió de referencia para el esquema `mail` de la celda
(`migrations/cell/canonical/mail-directory/01_mail.sql`), que conserva la semántica que
los motores entienden traducida a PostgreSQL.

## 4. Nace nuevo

* `services/gateway/routes.json` y `routes.go`: enrutado por datos.
* `migrations/cell/`: el plano de datos de celda.
* Celdas en `organization` (`organization.cells`, `tenants.cell_id`).
* `ops/scaffold/check-clean-copy.sh` y el target `make clean-copy`.
* `scripts/deploy-mail.sh` (despliegue de los motores) y `scripts/lib/despliegue.sh` (destino,
  candado, guardia de retroceso y verificación de la imagen desplegada, extraídos de `deploy-ecr.sh`
  y compartidos con él).
* `docker-compose.images.save.yml`: override de imagen del transporte `save`, generado junto al de
  ECR (`make gen-compose-images`).
* `docs/*` de este producto.
* `services/mail-migration` (plano de empresa) y `deploy/mail/migration-runner` (ejecutor con `imapsync`,
  modulo Go propio sin dependencias): migracion de buzones desde otro proveedor
  (`docs/adr/0002-migracion-de-buzones-con-imapsync.md`). `imapsync` no se copio de mailcow: se instala
  desde el paquete de Alpine (community, serie 2.314; Debian y Ubuntu no lo empaquetan) en la imagen del ejecutor.
* `services/observability` (plano de control): visor de registros del superadmin sobre Loki, sin base ni bus
  (`docs/adr/0009-visor-de-registros-y-lectura-del-antispam.md`). Sus permisos, en `migrations/registry/037_observability_permissions.sql`.
* `services/smtp-relay` (plano de empresa, sin base ni ruta en el gateway): relay SMTP de envio de las empresas, con
  sus claves de API como credencial, que entrega a `transactional` (`docs/adr/0013-claves-de-api-y-relay-smtp.md`).
  El protocolo SMTP es `github.com/emersion/go-smtp` (MIT); la lectura del MIME, `pkg/rawmail`, compartida con
  `transactional`, y la resolucion de claves, `pkg/apikey`, compartida con el gateway.
* `services/mail-dav` (plano de empresa): CardDAV y CalDAV, con su esquema `mail_dav` en `migrations/tenant/canonical/mail-dav/`
  (tablas, RLS y rol de servicio). El protocolo WebDAV/CardDAV/CalDAV se escribio a mano en
  `internal/adapters/http`, y el iCalendar con sus recurrencias en `internal/domain` (no se copio ni se importo nada de
  un servidor DAV ni de una libreria de iCalendar: `docs/adr/0004-contactos-y-calendario-carddav-caldav.md`).
  Tocan a otros servicios el flag `dav_access` (`mail-auth`, `mail-directory`, migracion `11_mailbox_dav_access.sql` de
  la celda) y el gateway (`services/gateway/webdav.go`: metodos WebDAV, `MKCALENDAR` incluido, y `/.well-known/carddav` y `/.well-known/caldav`).
* Fases siguientes: `mail-directory`, `mail-auth`, `mail-policy`, `domain-service`,
  `transactional`, `contacts`, `campaigns`, `templates`, `suppression`, `reputation`,
  `analytics`, `billing`, `policy`, `web/`.

## 5. Dónde va cada cosa

| Necesito | Va en |
|---|---|
| Un servicio nuevo | `make new-service name=<svc> port=<p> module=<m>`; su ruta en `services/gateway/routes.json`; sus permisos en `migrations/registry/NNN_<svc>_permissions.sql` |
| Una tabla del plano de control | `migrations/registry/NNN_<svc>_<nombre>.sql` |
| Una tabla que leen los motores | `migrations/cell/canonical/mail-directory/NN_<nombre>.sql` (esquema `mail`) |
| Una tabla de negocio por empresa | `migrations/tenant/canonical/<svc>/NN_<nombre>.sql` |
| Un secreto | `ops/security/secrets/secret-keys.txt` + el almacén; nunca `.env` |
| Una variable no sensible | `.env.example` con comentario |
| Un evento | subject `<dominio>.<entidad>.<accion>`; `make gen-events` |
| Un proceso que sale a Internet con credenciales de terceros | Un contenedor propio sin acceso a la base, con modulo Go aparte (`deploy/mail/migration-runner`), que pide trabajo por HTTP al servicio dueno de los datos |
| Configuración de un motor | `deploy/mail/<motor>/` |
| Una decisión de arquitectura | `docs/adr/NNNN-<titulo>.md` y el documento rector afectado |
