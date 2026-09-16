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
  la empresa); en el gateway, las rutas con sesion de `mail-directory` y `mail-security` van a
  la instancia de la celda de la empresa (2026-09-13, `make e2e` con dos celdas; 5.4), y cada
  instancia rechaza por su cuenta a la empresa que organization no situa en su celda (403
  `TENANT_NOT_IN_CELL` sin escribir nada, `pkg/tenantcell`; `make e2e` manda una empresa de
  pe-02 a pe-01 por un gateway sin celdas); el webmail va a la celda del dominio del buzon y
  despues a la de su token, con el indice global de dominios activos de organization, que da
  tambien la unicidad de un dominio entre celdas (2026-09-13, `make e2e` con un webmail por
  celda; 5.5). Credencial propia por celda para los servicios de celda hecha (2026-09-13:
  `ops/db/cell-service-role.sh`, `db.NewCellPool`, falla cerrado fuera de desarrollo;
  `Modelo_de_Datos_y_Celdas.md` 5.1). Pendiente: reparto de secretos por servicio en
  compose, la entrada del rol en `userlist.txt` de PgBouncer, `mail_engine` por celda y la
  credencial por servicio de las bases de empresa (5.2).
* Ciclo de vida de una ejecución del scheduler cerrado (2026-09-13, unitarias e integración
  contra Postgres 16): `scheduler.job.started`, `.completed` y `.failed` salen por la outbox
  en la transacción que cambia la ejecución (stream `SCHEDULER`); el ejecutor cierra por
  `POST /internal/scheduler/executions/{id}/complete|fail` y un barrido con
  `db.TryLeaderLock` vence por timeout y despacha los reintentos con espera creciente. El
  catálogo de manejadores (`services/scheduler/handlers.json`) declara desde 2026-09-15 su
  primer ejecutor real, `analytics.retention.prune` (servicio `analytics`, alcances `tenant`
  y `platform`, plazo máximo 300 s). Declarar un manejador ahí es comprometerse a consumir
  `scheduler.job.started` y cerrar la ejecución; el scheduler comprueba el nombre contra el
  catálogo al crear o editar un trabajo, al programar una tarea, al despacharlos y antes de
  reintentar (422 con `details.field = handler` y `details.rule = not_allowed`). Crear o
  editar un trabajo ya no responde 422 por un catálogo vacío. Las tareas puntuales
  (`scheduled_tasks`) se despachan de verdad (V, 2026-09-15, unitarias e integración contra
  Postgres 16): el barrido marca `executed` con la hora del caso de uso y encola
  `scheduler.task.started` en la MISMA transacción, así que una tarea cancelada entre la
  lectura y la escritura sigue cancelada y no se despacha, y un despacho que no se puede
  encolar deshace la marca en vez de dejarla ejecutada sin despachar. Una tarea cuyo
  manejador salió del catálogo queda `cancelled` con
  `failure_reason = handler_not_allowed` (`05_task_dispatch.sql`), que es lo que un trabajo
  anota en su ejecución y una tarea no tenía dónde anotar. Una tarea no informa cómo acabó:
  su garantía es la del bus (reentrega y DLQ de `pkg/events`); lo que deba quedar registrado
  va en un trabajo `one_time`, que sí tiene ejecución.
* La poda de la analítica es un trabajo del scheduler (2026-09-15; unitarias, cliente contra
  `httptest`, integración contra Postgres 16 y `make e2e`): `analytics` ya no la corre en un
  `time.Ticker` propio y sin cerrojo de líder (cada réplica podaba cada 24 h contadas desde
  su propio arranque, sin historial, sin reintentos y sin forma de lanzarla a mano). Ahora
  la despacha el trabajo de plataforma `analytics-retention-prune` que siembra
  `05_task_dispatch.sql` en cada base de empresa (`@daily` en UTC, con `next_run_at` en la
  medianoche siguiente para no podar en el propio despliegue; la empresa lo ve y no puede
  cambiarlo, 403 `ErrPlatformJob`), el consumidor durable `analytics-scheduler-jobs` la
  ejecuta y la cierra por `POST /internal/scheduler/executions/{id}/complete|fail`. Un fallo
  se informa como reintentable y lo reintenta el SCHEDULER con su espera creciente y su
  presupuesto, no el bus: dos reintentos superpuestos repetirían la misma poda a destiempo.
  `analytics` pasa a necesitar `SCHEDULER_URL` e `INTERNAL_GATEWAY_TOKEN`. Orden de
  despliegue: analytics primero (ya consume), después el scheduler (ya declara el manejador)
  y al final la migración 05 (siembra el trabajo); al revés, la primera pasada despacharía a
  un ejecutor que todavía no escucha y la ejecución vencería por plazo.
* `cron_expression` evaluada (2026-09-13, unitarias e integración contra Postgres 16) con
  el parser de `github.com/robfig/cron/v3` v3.0.1; la planificación sigue en la base. Se
  admiten los cinco campos estándar y `@hourly`, `@daily`, `@weekly`, `@monthly` y
  `@every <duración>` (mínimo 1 minuto). Crear, editar o reactivar un `cron` con una
  expresión vacía, inválida o que nunca ocurre responde 422 (`ErrInvalidCron`). La
  siguiente ejecución se cuenta desde la hora prevista, no desde la real (sin deriva), y
  las ocurrencias que una caída se saltó se lanzan una sola vez. La primera vuelta de cada
  proceso por empresa reconcilia los calendarios `cron` que dejó la versión que no
  evaluaba la expresión y desactiva los que tienen una expresión inválida.
* Reactivar un trabajo (2026-09-13, unitarias con reloj inyectado e integración contra
  Postgres 16; `domain.JobDefinition.ResumeAt`): nada de lo que no se lanzó mientras estuvo
  parado se lanza al reactivarlo y `next_run_at` nunca queda en el pasado. `cron`: la
  primera ocurrencia posterior a ahora en su zona. `interval`: sigue su rejilla sin
  deriva (la hora guardada más k periodos): si la hora guardada aún no llegó se respeta; si
  pasó, la primera de la rejilla posterior a ahora; sin calendario, o con una hora a más de
  un periodo (el intervalo se acortó mientras estaba parado), un periodo desde ahora; sin
  `interval_minutes` válido, 422 con el campo. `one_time`: si el calendario nunca lo
  despachó, sale en la pasada siguiente, como al crearlo; si ya lo despachó, 409
  `JOB_ALREADY_RUN` y sigue inactivo (volver a lanzarlo es `POST /jobs/{id}/run`, con
  `jobs/run`; reactivarlo lo habría relanzado con solo `jobs/update`). Reactivar uno activo
  no toca su calendario.
* Despacho de un `interval` sin deriva (2026-09-13, unitarias con reloj inyectado,
  contrato e integración contra Postgres 16; `domain.JobDefinition.NextAfterDispatch`): la
  siguiente ejecución es la primera de su rejilla (la hora prevista más k periodos)
  posterior a la pasada, contada desde la hora prevista y no desde la real, igual que
  `@every`. Un ticker que llega tarde no desplaza las siguientes (prevista 10:00, pasada
  10:00:29, cada 5 min: la siguiente es 10:05:00); tras una caída, lo que se saltó sale una
  sola vez (prevista 03:00, pasada 10:17, cada 7 min: una ejecución y la siguiente a las
  10:21). Cuenta tiempo transcurrido: la zona del trabajo y los cambios de hora no la
  mueven. Un trabajo guardado sin una definición planificable (minutos nulos, cero o
  negativos, un tipo desconocido, además de la expresión o la zona inválidas de un cron) se
  desactiva sin lanzarse, en vez de lanzarse en cada pasada.
* Editar el calendario de un trabajo (2026-09-13, unitarias, contrato e integración contra
  Postgres 16; `ScheduleChanged` y `FirstRunAt` del dominio): si la edición cambia cuándo
  corre (el tipo, la expresión o la zona de un `cron`, los minutos de un `interval`), la
  próxima ejecución se recalcula desde el momento de la edición con la definición nueva,
  como en un alta: `cron`, su primera ocurrencia posterior; `interval`, un periodo desde
  ahora (su rejilla empieza ahí); `one_time`, la pasada siguiente. Nunca queda en el pasado
  ni lanza lo que venció sin despacharse (cada 5 min con la próxima a las 09:58 sin
  despachar, pasado a 15 a las 10:00: la próxima es 10:15 y a las 10:00 no sale nada).
  Cambiar el tipo empieza además un calendario nuevo (`last_run_at` a NULL): un trabajo que
  pasa a `one_time` corre una vez aunque antes corriera como intervalo. Una edición que no
  toca el calendario (nombre, manejador, payload, reintentos, plazo, la zona de un
  intervalo) respeta la ejecución prevista. En un trabajo inactivo la hora queda guardada
  sin exponerse y al reactivarlo decide `ResumeAt`. Editar y reactivar bloquean el
  calendario y después el trabajo, en el orden del despacho: esperan al despacho en curso y
  leen lo que dejó, y el despacho salta el calendario mientras duran, sin interbloqueo. La
  edición no escribe `is_active` (lo cambian solo activar, desactivar y el calendario): un
  PUT leído antes de que el calendario despache un `one_time` guarda la edición y lo deja
  inactivo, y reactivarlo sigue siendo 409 `JOB_ALREADY_RUN` (unitarias e integración
  contra Postgres 16 con las transacciones intercaladas). Desactivar toma los mismos
  bloqueos en el mismo orden (2026-09-15): si llega antes que el despacho, este salta el
  calendario y la pasada vencida no sale; si un despacho ya lo reclamó, espera a que
  confirme y desactiva después, sin interbloqueo, y esa ejecución sigue su curso (nació con
  su `scheduler.job.started` en la misma transacción; pararla es cancelarla, con
  `executions/cancel`). Un trabajo de plataforma se rechaza antes de bloquear nada.
* Concurrencia optimista de la edición de un trabajo (2026-09-15, unitarias, contrato,
  jsdom e integración contra Postgres 16 con las transacciones intercaladas;
  `04_job_version.sql`): el trabajo lleva `version`, un entero que empieza en 1 y sube en uno
  con cada edición (no `updated_at`, que el trigger pisa con `NOW()`, la hora de inicio de la
  transacción, y que el navegador lee con milisegundos); activar, desactivar y el calendario
  no la cambian, porque el PUT no escribe `is_active`. `PUT /api/v1/scheduler/jobs/{id}` la
  exige en el cuerpo y solo se aplica si sigue siendo la guardada, comparada bajo el
  bloqueo: sin ella (o `null`) 428 `VERSION_REQUIRED`; con otra, 409 `VERSION_CONFLICT` sin
  escribir ni replanificar nada (RFC 9110, 15.5.10); menor que 1, 422 con
  `details.field = version`. No es `If-Match`: la representación lleva `next_run_at` y
  `last_execution`, que cambian en cada despacho, y una ETag fuerte tendría que cambiar con
  ellos. `web/` envía la versión que leyó y, ante el 409, explica que otra persona guardó
  cambios, bloquea el envío y ofrece recargar el formulario con la versión vigente. Orden de
  despliegue: la migración 04 en las bases de empresa, el scheduler y después la web (la web
  anterior recibe 428 al editar; la nueva contra el scheduler anterior, 400 por un campo
  desconocido).
* Aislamiento por empresa del scheduler (2026-09-13, unitarias, contrato e integración
  contra Postgres 16): ninguna lectura ni escritura por id va sin la empresa del token.
  Leer y cancelar una tarea, `GET /jobs/{id}/history`, `GET /executions` y las escrituras de
  trabajos (`Update`, `Deactivate`) y ejecuciones (`Update`) llevan la condición en el SQL;
  lo de otra empresa responde 404 como lo que no existe, sin efectos. `POST
  /tasks/{id}/cancel` responde 204 (también si ya estaba cancelada), 404 o 409 si ya se
  ejecutó; `GET /tasks/{id}` separa el 404 de un fallo de la base (500 registrado). El
  scheduler no audita sus escrituras (P).
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
  puntuales. El API los sirve (2026-09-13, unitarias, contrato e integración contra
  Postgres 16) y `web/` los consume: el trabajo lleva `next_run_at` (null si está inactivo),
  `last_run_at` (última pasada del calendario) y `last_execution` (`id`, `status`,
  `completed_at`, `failure_reason` o null) y `already_run` (true en un `one_time` que el
  calendario ya despachó, calculado con la misma regla, `OneTimeAlreadyRun`, con la que el
  servicio rechaza reactivarlo con 409 `JOB_ALREADY_RUN`; la web no ofrece Activar en ese
  caso y lo explica), leídos con dos consultas por página;
  `GET /scheduler/jobs`, `GET /scheduler/jobs/{id}/history`, `GET /scheduler/executions`
  (activas de la empresa y de plataforma, de la más reciente a la más antigua con el id como
  desempate) y `GET /scheduler/tasks` paginan con `page` y `per_page` y `meta` `page`,
  `per_page`, `total`, `total_pages` (20 por defecto, tope 100, desplazamiento que satura:
  una página enorme no tiene filas en vez de ser un 500) sobre la empresa del token;
  `GET /scheduler/meta` (`jobs/read`) publica `job_types`, `limits` y `pagination` desde
  `domain/validation.go`, y la ventana de las tareas pendientes va en la meta de
  `GET /scheduler/tasks` (`pending_window_seconds`, `tasks/read`); `web/` pagina las tareas
  en el servidor y lee de ahí la ventana. Todo error de campo del scheduler (422, también
  `INVALID_TIMEZONE`, y el 409 de código repetido) lleva `error.details.field` y
  `error.details.rule`, una regla estable del dominio: `required`, `too_long`,
  `out_of_range`, `invalid_format`, `not_allowed`, `never_matches` o `duplicate`. `web/` lo
  explica en español por la regla con el límite de la meta (o del manejador elegido) y solo
  muestra el mensaje del servidor ante una regla que no conoce; un campo fuera de su columna
  ya no es un 500.
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
  APIs, da de baja el correo de la empresa en el directorio de su celda y suelta sus dominios
  del índice global (2026-09-15), y se retoma hasta terminar. Rutas, contrato y comportamiento en
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
