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
| Los motores de `deploy/mail/` levantan contra el esquema `mail` | Cumplido (2026-09-13, dos ejecuciones seguidas desde cero en verde: 156 comprobaciones, unos 100 s cada una con las imágenes construidas). Postfix, Dovecot, Rspamd, ClamAV (firmas reales de freshclam), Unbound, Olefy, postfix-tlspol y redis-mail con las imágenes de `deploy/mail`, junto a `mail-auth`, `mail-security` (`mail-policy`), `mail-directory` y `webmail` construidos con sus Dockerfile y con la credencial de la celda. Celda y empresa por el camino real (`bootstrap-platform.sh`, alta por el gateway), dominio verificado por `domain-service` contra un DNS de la prueba (activa el dominio y entrega la clave DKIM), buzones, aliases y enrutado por el API de `mail-directory`. Probado: los 18 mapas pgsql con `postmap -q` (con `smtpd_sender_login_maps` sobre `mail.sender_login_owners`, migración 07); `doveadm user`; IMAP por `passwd-verify.lua` y `mail-auth`; usuario maestro del webmail solo desde la red de los motores; submission autenticado, Rspamd con DKIM y el mapa `settings` de `mail-policy`, entrega por LMTP leída por IMAP; 553 a un remitente ajeno (también con la credencial maestra) y envío como alias con permiso, dominio alias y `sender_acl`; EICAR rechazado y en cuarentena; `DOMAIN_MAP` y claves DKIM en redis-mail; webmail por el gateway. Corrigió tres fallos que solo se ven con los motores reales (`deploy/mail/README.md`). Sustituidos en la prueba: certificado (CA propia en lugar de ACME), `acme`, `netfilter`, `watchdog` y `dockerapi` fuera | `make e2e-mail` (`ops/e2e/mail.sh`); en CI, `.github/workflows/mail-engines.yml` (no bloquea: cambios que le afectan, cada noche y a mano). En producción, el primer despliegue repite `postmap -q` y `doveadm user` sobre la celda |
| `ERP/` y `mailcow/` borrados | Cumplido: no existen en el repositorio (comprobado 2026-09-13); `.gitignore` los excluye si se vuelven a clonar dentro | `ls ERP mailcow` |

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
* `mail-security` envía el aviso de cuarentena al buzón por `transactional`
  (`purpose=quarantine_notice`, barrido con cerrojo de líder, `notified` marcado en la misma
  transacción que registra el aviso, migración 07) con enlaces firmados, con caducidad y de
  un solo uso para liberar o descartar sin sesión (probado contra Postgres con las
  migraciones aplicadas dos veces, 2026-09-13; sin SES; contrato en
  `deploy/mail/README.md`). `clean_q_aged.sh` de Dovecot no hace nada (busca
  `mail.quarantine`, que no existe): la poda la hace el servicio.
* Enlaces publicos de cuarentena por celda (2026-09-13, unitarias, integracion contra
  Postgres, `make e2e` y `make e2e-mail`, que firma el enlace del EICAR en cuarentena y lo
  sigue por el gateway): la celda va en la ruta y en la firma, el gateway enruta con
  `MAIL_SECURITY_CELL_HOSTS` sin la clave de los enlaces y cada `mail-security` solo acepta
  los de su celda (`Modelo_de_Datos_y_Celdas.md`, 5.3). No hay forma sin celda: nada se
  desplego con la anterior y se retiro sin ventana de compatibilidad.
* El gateway debe servir `/.well-known/acme-challenge/` o usarse `ACME_DNS_CHALLENGE=y`.
* Primer despliegue: smoke test con `postmap -q` y `doveadm user` sobre la celda.
* Corregido con la prueba de los motores (2026-09-13): `mail-policy` responde al `HEAD` con el
  que Rspamd 4 pide `/settings` y `/forwardinghosts` (sin él Rspamd daba la carga por fallida
  y nunca aplicaba el mapa `settings`); `/pipe` acepta los metadatos de Rspamd 4 (símbolos
  como objetos, `rcpt` como `unknown`), que respondía 400 y no guardaba ninguna cuarentena;
  y los mapas de Postfix que buscaban el buzón como destino del alias `buzón -> buzón` de
  mailcow (TLS obligatorio al recibir y al enviar, relayhost propio) casan ya el propio
  buzón, con la credencial SASL del mismo relayhost que elige el transporte.
* Sin probar contra los motores: la entrada por el puerto 25 desde internet (postscreen, DNSBL,
  greylisting, SPF/DKIM/DMARC de remitentes externos), la salida real por DANE/MTA-STS y los
  relayhosts, la réplica de Dovecot, netfilter y watchdog. Lo cubre el smoke test del primer
  despliegue.

## Deuda conocida que sale de la copia (no bloquea la fase 0)

* Outbox disponible en `pkg/outbox` (probado contra Postgres): los servicios copiados en
  fase 0 siguen publicando tras el commit; se migran a `Enqueue` cuando se toquen. Los
  servicios nuevos lo usan desde el principio para sus publicaciones críticas. Los de celda
  (`mail-directory`, `mail-security`) encolan en `platform.event_outbox` de la celda y la
  vacía un solo relé por celda (`Relay.RunExclusive` con `db.TryLeaderLock` y
  `outbox.CellRelayLockKey`, compartida por los dos).
* Firma del access token hecha (2026-09-13): `pkg/auth` firma con EdDSA (Ed25519) y `kid`,
  el verificador fija el algoritmo y exige `kid` conocido y `typ`, solo identity recibe la
  clave privada (`JWT_SIGNING_KEY`) y el gateway verifica con `JWT_PUBLIC_KEYS`. `JWT_SECRET`
  desaparece de `pkg/config`, del gateway y del almacén; sin transición HS256 (motivo en
  `docs/arquitectura/CSP-Y-SESION.md`). Probado en unitarias de `pkg/auth`, identity y
  gateway y en `make e2e`.
* La tercera capa (`pkg/authz.RequirePermission`) la usan los servicios nuevos y todo el
  plano de control: identity, organization, audit, scheduler y access-control (este en
  proceso, con su caso de uso de política) exigen el permiso de acción concreto. Ningún
  handler queda con `IsPrivileged` o `RequireRoles(tenant_admin)` como única comprobación.
* Enrutado por celda implementado (`db.NewTenantRouting`, aprovisionamiento en la celda de
  la empresa). Credencial propia por celda para los servicios de celda hecha (2026-09-13:
  `ops/db/cell-service-role.sh`, `db.NewCellPool`, falla cerrado fuera de desarrollo;
  `Modelo_de_Datos_y_Celdas.md` 5.1). Pendiente: reparto de secretos por servicio en
  compose, la entrada del rol en `userlist.txt` de PgBouncer, `mail_engine` por celda y la
  credencial por servicio de las bases de empresa (5.2).
* Ciclo de vida de una ejecución del scheduler cerrado (2026-09-13, unitarias e integración
  contra Postgres 16): `scheduler.job.started`, `.completed` y `.failed` salen por la outbox
  en la transacción que cambia la ejecución (stream `SCHEDULER`); el ejecutor cierra por
  `POST /internal/scheduler/executions/{id}/complete|fail` y un barrido con
  `db.TryLeaderLock` vence por timeout y despacha los reintentos con espera creciente. El
  catálogo de manejadores (`services/scheduler/handlers.json`) está vacío porque ningún
  servicio consume hoy `scheduler.job.started`: hasta que un ejecutor se declare, crear o
  editar un trabajo responde 422. Pendiente del scheduler: las tareas puntuales
  (`scheduled_tasks`) se marcan `executed` sin despachar nada.
* `cron_expression` evaluada (2026-09-13, unitarias e integración contra Postgres 16) con
  el parser de `github.com/robfig/cron/v3` v3.0.1; la planificación sigue en la base. Se
  admiten los cinco campos estándar y `@hourly`, `@daily`, `@weekly`, `@monthly` y
  `@every <duración>` (mínimo 1 minuto). Crear, editar o reactivar un `cron` con una
  expresión vacía, inválida o que nunca ocurre responde 422 (`ErrInvalidCron`). La
  siguiente ejecución se cuenta desde la hora prevista, no desde la real (sin deriva), y
  las ocurrencias que una caída se saltó se lanzan una sola vez. La primera vuelta de cada
  proceso por empresa reconcilia los calendarios `cron` que dejó la versión que no
  evaluaba la expresión y desactiva los que tienen una expresión inválida.
* Zona horaria por trabajo (2026-09-13, unitarias e integración contra Postgres 16):
  `job_definitions.timezone` (`03_job_timezone.sql`, IANA, `UTC` por defecto; las filas
  previas quedan en UTC). La expresión cron se evalúa en esa zona; `@every`, `interval` y
  `one_time` cuentan tiempo transcurrido. Cada hora de pared se lanza una vez: la que cae
  en la hora saltada se lanza en el salto y la de la hora repetida solo en su primera
  pasada. Zona inválida: 422 `INVALID_TIMEZONE`; reglas en `GET /api/v1/scheduler/meta`. El
  binario incorpora `time/tzdata`, no arranca sin base de zonas y el Dockerfile lo
  comprueba (`--tzcheck`).
* Contrato JSON del scheduler fijado en snake_case (DTOs del adaptador HTTP, con test de
  contrato; la duración sale como `duration_ms`). `web/` lo consume (2026-09-13, pruebas
  unitarias y de componente en jsdom; P: en navegador real contra el servicio): `/scheduler`
  y `/scheduler/:id` con trabajos, alta y edición, historial de ejecuciones y tareas
  puntuales. Faltan en el API: la próxima y la última ejecución de cada trabajo en su DTO,
  la paginación de `GET /scheduler/jobs`, y `job_types` y los topes de nombre, código,
  intervalo, reintentos y plazo en `GET /scheduler/meta`.
* Lecturas del registro entre esquemas, todas por vistas publicadas: `identity` resuelve la
  empresa del login y el nombre de la empresa del listado de sesiones por
  `organization.v_tenants` (`025_organization_tenants_view.sql`, sin `db_name`, `cell_id`
  ni `settings`), y `access-control` lee el catálogo de módulos y su estado por empresa por
  `organization.v_module_catalog` y `organization.v_tenant_modules`
  (`024_organization_published_views.sql`). Ninguno de los dos figura ya en
  `ops/scaffold/coupling-allowlist.txt`: si vuelven a leer las tablas, `check-coupling`
  falla. Tampoco queda ninguna escritura cruzada: las dos allowlists de acoplamiento están
  vacías (punto siguiente).
* Alta y baja de empresa por saga, sin escribir en esquemas ajenos (2026-09-13; unitarias de
  cada punto de fallo y su compensación, contrato de las rutas internas, integración contra
  Postgres 16 y `make e2e`): `organization` ya no inserta ni borra en `identity` ni en
  `access_control`. El alta registra la empresa inactiva con su saga
  (`organization.tenant_sagas`, `026_organization_tenant_sagas.sql`), crea y migra la base
  marcada con el id de la empresa y pide por API interna el rol `tenant_admin` a
  access-control, el primer usuario a identity (con su política de contraseñas y la
  comprobación de filtraciones, que el sembrado anterior se saltaba) y la asignación; solo
  entonces la activa. Un fallo se deshace en orden inverso y deja la saga en `failed`; una
  caída deja el paso guardado y el reintento sigue desde él, y el barrido de `organization`
  termina o deshace lo que quede sin dueño. La baja retira roles y cuentas por las mismas
  APIs y se retoma hasta terminar. Rutas, contrato y comportamiento en
  `Usuarios_Roles_y_Acceso.md`, sección 7. `make check-coupling` pasa con
  `coupling-allowlist.txt` y `coupling-writes-allowlist.txt` vacías.

## Lo que la fase 0 dejó mejor que la base

* Gateway con tabla de rutas en datos (`routes.json`) y validación al arrancar.
* Límites de peticiones del gateway comunes a todas sus réplicas: el general de `/api/v1`
  (`API_RATE_LIMIT_PER_MIN`) y el estricto de autenticación (`AUTH_RATE_LIMIT_PER_MIN`:
  `/auth` y `POST /api/v1/webmail/session`) cuentan por IP en el Redis de la plataforma
  (`REDIS_*`); con Redis caído cada réplica decide en memoria con el mismo cupo y lo cuenta
  en `rate_limit_degraded_total`. Probado con dos réplicas contra Redis 7 real y con Redis
  inalcanzable (2026-09-13). Los límites propios de los demás servicios siguen en memoria
  (motivo en `docs/arquitectura/CSP-Y-SESION.md`).
* TLS hacia el Redis de la plataforma (2026-09-13): todos sus clientes (gateway,
  access-control, mail-auth, reputation, webmail) toman `REDIS_TLS`, `REDIS_TLS_CA_FILE` y
  `REDIS_TLS_SERVER_NAME` de `pkg/config` (`LoadRedis`, `RedisConfig.TLSConfig`), sin
  forma de saltarse la verificación, y fuera de un `ENVIRONMENT` declarado de desarrollo o
  de prueba no arrancan en claro. Probado en unitarias de `pkg/config` (incluida una
  negociación real en proceso), en integración contra un Redis 7 que solo escucha en
  `tls-port` con CA y certificado generados en la prueba (conexión verificada; sin la CA,
  con otra CA, con otro nombre o en claro, falla) y en `make e2e` (el gateway con
  `ENVIRONMENT=production` y sin TLS no arranca). El Redis de los motores sigue en claro
  dentro de la red de la celda: motivo en `deploy/mail/README.md`, Contrato Redis.
* RBAC en `enforce` y `fail-closed` por defecto.
* Dos bugs de identity corregidos: el cambio de contraseña propio no persistía y el logout
  no revocaba la sesión en servidor; el historial de contraseñas ahora se aplica.
* Roles del sistema reducidos a dos; ningún nombre de rol de negocio en código.
* Directorio de correo por celda con rol `mail_engine` de mínimo privilegio.
