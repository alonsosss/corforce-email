# Arquitectura de Core Force Mail

Qué se construye, con qué servicios, en qué orden, y en qué se aparta del informe de
partida (`Informe_Arquitectura_Core_Force_Mail.md`). Este documento manda sobre el informe
cuando se contradicen; el informe se conserva como origen de las decisiones.

## 1. Planos

```
INTERNET
   |
Proxy de borde (TLS)                 MX / IMAPS / Submission (deploy/mail, por celda)
   |                                        |
Gateway Go (unica entrada HTTP)      Postfix -> Rspamd -> Dovecot/LMTP -> Maildir
   |                                        |            |
   +-- PLANO DE CONTROL (registro)          |     mail-auth (HTTPS) / mail-policy (HTTP)
   |   identity, access-control,            |            |
   |   organization, audit, scheduler       +------ base de la CELDA (esquema mail)
   |                                                     |
   +-- CORREO CORPORATIVO (celda) ------------- mail-directory, mail-security, domain-service
   |
   +-- TRANSACCIONAL / MARKETING (empresa) ---- transactional, templates, suppression,
       contacts, segments, campaigns, automations, reputation, analytics -> Amazon SES

Infraestructura: PostgreSQL (registro + celdas + empresas) tras PgBouncer, Redis, NATS
JetStream, S3, Prometheus/Grafana/Loki. La analitica vive en Postgres; ClickHouse solo
con volumen medido y ADR (seccion 6).
```

## 2. Servicios

### 2.1 Plano de control (registro). Estado: copiados y limpios en fase 0

| Servicio | Responsabilidad | Esquema |
|---|---|---|
| `gateway` | Unica entrada. JWT, cabeceras de confianza, RBAC por modulo (lectura y escritura), revocacion instantanea, rastro de auditoria, CSP con nonce, rutas por `routes.json` | - |
| `identity` | Usuarios, sesiones (refresh opaco con rotacion), MFA TOTP, step-up, politicas de contrasena y sesion, reinicio de contrasena, comprobacion de filtraciones | `identity` |
| `access-control` | Roles por empresa, permisos `(module, resource, action)`, politica efectiva con cache Redis, gateo de modulos contratados, denegaciones | `access_control` |
| `organization` | Empresas, celdas, modulos por empresa, aprovisionamiento de bases y migraciones (registro, empresa), siembra de `tenant_admin` y del primer usuario | `organization` |
| `audit` | Rastro append-only con cadena SHA-256, eventos de seguridad, detector (fuerza bruta, dispositivo nuevo, exfiltracion) | `audit` (por empresa) |
| `scheduler` | Trabajos cron y tareas programadas por empresa con bloqueo en base | `scheduler` (por empresa) |

Difiere del informe: `tenant-service` se llama `organization` (nombre heredado, esquema
`organization`); `billing-service` es `billing` (tabla de 2.3; su esquema `billing` vive en
el registro) y `policy-service` se absorbe en `reputation` (seccion 6).

### 2.2 Correo corporativo (celda). Estado: `mail-directory`, `mail-auth`, `domain-service` y `mail-security` verificados contra Postgres real (2026-09-12); motores copiados, pendientes de levantar con `mail-security` como `mail-policy`

| Servicio | Responsabilidad | Esquema |
|---|---|---|
| `mail-directory` | Dominios (parte de enrutado), buzones, aliases, dominios alias, contrasenas de aplicacion, relayhosts, transportes, politicas TLS, sieve, cuota. Es lo que Postfix y Dovecot leen por SQL | `mail` (celda) |
| `mail-auth` (V) | `POST /` HTTPS que Dovecot invoca desde `passwd-verify.lua`: verifica contrasena o contrasena de aplicacion, aplica `*_access` por protocolo, freno de fuerza bruta en Redis, registra el inicio de sesion. Sin tablas propias: es la mitad de autenticacion del directorio. Lee `mailboxes` y `app_passwords` sin empresa (Dovecot no la conoce; `username` es unico en la celda) | `mail` (lee; escribe `sasl_logins`) |
| `mail-security` | Politicas antispam por empresa y buzon, listas, pies de pagina, limites de tasa, cuarentena; sirve los mapas dinamicos que Rspamd y Postfix piden por HTTP (8081: `settings`, `aliasexp`, `bcc`, `footer`, `forwardinghosts`; 9081: `pipe`, `pipe_rl`; alias de red `mail-policy`) y es el UNICO escritor de las claves de Redis de los motores (`DOMAIN_MAP`, `DKIM_*`, `RL_VALUE`, ...). Lee el directorio solo por las vistas `mail.v_routing_*` | `mail_security` (celda) |
| `domain-service` | Alta y verificacion de dominios (TXT de propiedad, MX, SPF, DKIM, DMARC), generacion y custodia cifrada de claves DKIM con rotacion; al verificar, activa el dominio en `mail-directory` y entrega la clave DKIM a `mail-security` | `domains` (empresa) |

Difiere del informe: `mailbox-service`, `mail-routing-service` y `mail-storage-service` se
funden en `mail-directory`, y `mail-policy` (los mapas HTTP de los motores) vive dentro de
`mail-security`, porque ambos leen y escriben las mismas politicas y solo debe haber un
escritor de Redis. Postfix resuelve un destinatario con UNA consulta que une
buzon, alias y dominio alias; repartir esas tablas en tres esquemas obligaria a que los
mapas SQL de Postfix cruzaran esquemas, que es justo lo que prohibe la regla de un
esquema por servicio. `mail-admin-service` queda cubierto por `dockerapi` y `watchdog` de
mailcow mas el `scheduler`.

Por que la celda y no la empresa: un motor SMTP no puede consultar N bases; ve todos los
dominios que sirve en una sola. El aislamiento entre empresas es `tenant_id` + RLS para
los servicios Go y el rol `mail_engine` de solo lectura para los motores.

Verificado de punta a punta con binarios reales (2026-09-12): alta de dominio por API en
`domain-service`, activacion interna en `mail-directory`, alta de buzon, y la fila que
Postfix/Dovecot leen bajo el rol `mail_engine` con su ruta Maildir; otra empresa lista cero
buzones (RLS).

### 2.3 Transaccional y marketing (empresa). Estado: `transactional`, `templates` y `suppression` verificados (2026-09-12); `reputation` verificado contra Postgres y Redis reales (2026-09-13); carril de marketing y autorizacion previa de `transactional` verificados con pruebas unitarias e integracion contra Postgres (2026-09-13), sin prueba de punta a punta con SES; `analytics` verificado con pruebas unitarias e integracion contra Postgres (2026-09-13); `campaigns` verificado con pruebas unitarias e integracion contra Postgres (2026-09-13), sin prueba de punta a punta con `contacts` y `transactional` reales; fase 4 en construccion (`contacts`, `billing`; despues `automations`)

| Servicio | Responsabilidad |
|---|---|
| `transactional` | API de envio transaccional (idempotente), adaptador SES v2, plantillas por referencia, ingesta de eventos SES (SNS con firma verificada o EventBridge), `POST /internal/send-email` para la propia plataforma. Carril de marketing para `campaigns` (`POST /internal/transactional/batch`, lotes de hasta 500): cola `transactional.marketing.queued`, consumidor `transactional-marketing-sender`, configuration set `SES_CONFIG_SET_MARKETING` y tasa `SES_MAX_SEND_RATE_MARKETING` propios; el carril sale de `messages.class`, nunca del subject. Autorizacion previa en `reputation` por clase y numero de destinatarios no suprimidos: una denegacion se respeta siempre (`rate_limited` con espera -> 429 con `Retry-After`; `rate_limited` sin espera, `suspended` y `reputation_restricted` -> 403 `SENDING_RESTRICTED`; cualquier motivo del plan -> 403 `PLAN_LIMIT_REACHED`); si `reputation` no responde, el marketing falla cerrado (503) y el transaccional sale igual y queda en el log. Todos los `transactional.email.*` llevan `class`, `campaign_id`, `contact_id` y `occurred_at`. El tipo de plantilla lo dice `templates` (`kind` en su render interno): la via de marketing exige `marketing` y un enlace de baja en el cuerpo (`TEMPLATE_NOT_MARKETING`, `TEMPLATE_MISSING_UNSUBSCRIBE`), y el carril transaccional rechaza una plantilla de marketing (`TEMPLATE_NOT_TRANSACTIONAL`). `billing` cuenta cada envio en el recurso de su clase y por destinatario, igual que autoriza `reputation` |
| `templates` | Plantillas versionadas con `html/template`, parte de texto generada, variables validadas; `POST /internal/templates/{id}/render` para `transactional` y `campaigns` |
| `suppression` | Lista por empresa y global: rebotes duros, quejas, bajas; consulta previa a cualquier encolado |
| `reputation` | Tasas de rebote y queja por empresa y clase de envio sobre una ventana movil, restricciones automaticas (la clase `transactional` solo se restringe sola al doble del umbral de bloqueo), suspension manual del superadmin, limites de tasa por hora y dia en Redis y derecho mensual consultado a `billing`: `POST /internal/reputation/authorize` responde a "puede esta empresa enviar N mensajes de esta clase ahora" (absorbe al `policy-service` del informe). Fail-open en la tasa y en el plan, nunca en el estado de reputacion |
| `contacts` | Contactos con atributos declarados, consentimiento como evidencia append-only (doble opt-in), listas estaticas y segmentos dinamicos con un DSL compilado a SQL parametrizado; entrega audiencias paginadas a `campaigns` (`POST /internal/contacts/audience`, keyset por id sobre el indice parcial de enviables). V (2026-09-13): servicio, migraciones `tenant/canonical/contacts/01` y `registry/012`, pruebas unitarias y de integracion contra Postgres (20.000 contactos; EXPLAIN sin seq scan en el filtro principal). Consume `suppression.entry.added` y `.removed` (estado del contacto y consentimiento revocado por baja) y publica por outbox en el stream `CONTACTS`. El correo del doble opt-in lo envia `automations` al consumir `contacts.consent.requested` `{tenant_id, contact_id, email, confirm_url}`; mientras `automations` no exista, el enlace se genera y queda en el evento sin enviarse |
| `campaigns` (V) | Campanas por empresa (esquema `campaigns`) con ciclo de vida `draft -> scheduled -> sending -> completed`, pausa (manual, por 403 `SENDING_RESTRICTED`/`PLAN_LIMIT_REACHED` o tras 10 fallos transitorios seguidos de un lote), cancelacion y fallo (422); la version de la plantilla se fija al programar o iniciar. Orquestador por empresa cada `CAMPAIGNS_TICK` que recorre la audiencia de `contacts` en lotes de `CAMPAIGNS_BATCH_SIZE` (tope 500): cada lote se confirma y reserva antes de llamar a nadie (claim-before-send), fija su pagina antes del primer envio y se entrega a la via de marketing de `transactional` con `idempotency_key = campaign:<id>:batch:<seq>`; el cursor solo avanza cuando el lote se cierra, asi que una caida no duplica ni pierde destinatarios. Estadisticas por campana desde `transactional.email.*` (durable `campaigns-stats`, idempotente por id de evento; aperturas y clics unicos por mensaje; los envios de prueba llevan un `contact_id` sintetico, UUIDv5 de la campana y la direccion, que el consumidor recalcula y no suma). Publica `campaigns.campaign.*` por outbox en el stream `CAMPAIGNS`. Pendiente: lectura interna de plantillas en `templates` (hoy la version publicada se averigua renderizando sin variables; si la plantilla declara variables requeridas sin valor por defecto hay que indicar `template_version`. El tipo `marketing` se exige al programar con el `kind` que devuelve el render interno, y `transactional` lo vuelve a exigir en cada lote) |
| `automations` | Flujos disparados por eventos |
| `analytics` (V) | Agregados de envio por empresa, dia (UTC), clase, campana y dominio destino sobre Postgres (esquema `analytics` de la empresa), alimentados por `transactional.email.*` y `campaigns.campaign.*` con consumidores durables idempotentes por id de evento; una fila por mensaje para aperturas y clics unicos, podada por `ANALYTICS_MESSAGE_RETENTION_DAYS` (los agregados no se podan); API de consulta del panel (`overview`, `timeseries`, `campaigns`, `domains`) con tasas como texto decimal. ClickHouse solo con volumen medido y ADR |
| `billing` | Plano de control (esquema `billing` del registro): planes con limites por recurso, suscripcion por empresa, contadores de consumo alimentados por eventos (idempotentes por id de evento) y consulta de derechos (`POST /internal/billing/entitlements/check`). V (2026-09-12): servicio, migraciones `013`/`014`, pruebas unitarias y de integracion contra Postgres. Pendiente: pasarela de pago y facturas (el cierre de periodo publica `billing.period.closed` para ellas), y que los servicios consulten el derecho antes de crear |

## 3. Datos: tres planos

Ver `Modelo_de_Datos_y_Celdas.md`. Resumen: registro (`mail_registry`, uno), celda
(`mail_cell_<code>`, una por celda, esquema `mail` y `mail_security`), empresa
(`mail_tenant_<slug>`, una por empresa). PgBouncer en modo transaccion delante de todo; las
migraciones conectan directo.

## 4. Seguridad

* Gateway como unica entrada HTTP; los motores exponen solo 25, 465, 587, 143, 993, 110,
  995, 4190 por celda.
* Sesion: `docs/arquitectura/CSP-Y-SESION.md`. Tres capas de acceso:
  `Usuarios_Roles_y_Acceso.md`.
* Secretos en el almacen; credenciales de terceros cifradas por empresa con rotacion.
* `mail_engine` no puede leer contrasenas de buzon: la verificacion pasa por `mail-auth`.
* ClamAV en Rspamd para todo lo que entra; ClamAV antes de guardar cualquier adjunto que
  suba una persona.
* Auditoria con cadena de hashes; exfiltracion detectada en el gateway.

## 5. Fases

| Fase | Contenido | Estado |
|---|---|---|
| 0 | Copia y limpieza del plano de control, `pkg/`, operativa; motores de mailcow en `deploy/mail/`; esquema `mail` de celda; documentos | En curso: `Fase0_Estado.md` |
| 1 | `web/` (React + TypeScript, una sola aplicacion): login, MFA, usuarios, roles, empresas, celdas | En curso |
| 2 | Correo corporativo: `mail-directory`, `mail-auth`, `mail-security`, `domain-service`; motores levantados contra la celda; webmail | En curso |
| 3 | Transaccional: `transactional`, `templates`, `suppression`, `reputation`, ingesta SES, `billing` | En curso: `transactional`, `templates` y `suppression` hechos |
| 4 | Marketing: `contacts` (con segmentos), `campaigns`, `automations`, `analytics` | En curso |
| 5 | pgvector: busqueda semantica, clasificacion, resumen, segmentacion asistida | Pendiente |

## 6. Decisiones que se apartan del informe

| Informe | Decision | Motivo |
|---|---|---|
| `mailbox-service`, `mail-routing-service`, `mail-storage-service` | `mail-directory` | Los motores necesitan un solo esquema (seccion 2.2) |
| Datos de correo por empresa | Directorio de correo por celda | Postfix/Dovecot consultan una base |
| `tenant-service` | `organization` | Nombre y esquema heredados; misma responsabilidad |
| OpenTelemetry | Prometheus + zap + Loki | Sin metricas que exijan trazas distribuidas; ADR si cambia |
| Module federation en el frontend | Una sola aplicacion Vite | Un equipo, un producto; la federacion del ERP obligaba a compartir sesion por `window` |
| RBAC en modo `audit` por defecto | `enforce` y `fail-closed` por defecto | Es una plataforma de correo con datos personales: lo que no se configura, bloquea |
| `contact-service` y `segment-service` | `contacts` con listas y segmentos | Evaluar un segmento es filtrar contactos: separarlos obliga a leer tablas ajenas o a copiar la base de contactos |
| `policy-service` | Dentro de `reputation` | Un envio necesita una sola respuesta (cuota del plan y restriccion por reputacion); dos servicios serian dos llamadas y dos verdades |
| ClickHouse desde el principio | Agregados en Postgres; ClickHouse con ADR | Sin volumen medido no se introduce infraestructura nueva (`CLAUDE.md`) |
| `sender-orchestrator` y `sender-worker` con `email.requested` | `campaigns` entrega lotes a la via de marketing de `transactional` | Un solo servicio habla con SES, aplica supresion, reputacion y bajas RFC 8058; un segundo emisor duplicaria esas garantias y abriria un camino que se las salta |
