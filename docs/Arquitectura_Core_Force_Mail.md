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
JetStream, S3, ClickHouse (analitica, fase 4), Prometheus/Grafana/Loki.
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
`organization`); `billing-service` y `policy-service` no existen todavia (fase 3/4).

### 2.2 Correo corporativo (celda). Estado: motores copiados; servicios Go en fase 2

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

### 2.3 Transaccional y marketing (empresa). Estado: fase 3 y 4

| Servicio | Responsabilidad |
|---|---|
| `transactional` | API de envio transaccional (idempotente), adaptador SES v2, plantillas por referencia, ingesta de eventos SES (SNS con firma verificada o EventBridge), `POST /internal/send-email` para la propia plataforma |
| `templates` | Plantillas versionadas con `html/template`, parte de texto generada, variables validadas |
| `suppression` | Lista por empresa y global: rebotes duros, quejas, bajas; consulta previa a cualquier encolado |
| `reputation` | Puntuacion, limites y eleccion de configuration set / pool por empresa y clase de envio |
| `contacts`, `segments` | Contactos con consentimiento y origen; segmentacion estatica y dinamica |
| `campaigns` | Campanas, estados, programacion; `sender-orchestrator` (expansion, politicas, `email.requested` a NATS) y `sender-worker` (consumo durable, claim-before-send) |
| `automations` | Flujos disparados por eventos |
| `analytics` | Agregados; eventos masivos en ClickHouse |
| `billing`, `policy` | Planes, cuotas, metering; limites por empresa |

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
| 3 | Transaccional: `transactional`, `templates`, `suppression`, `reputation`, ingesta SES, `billing` | Pendiente |
| 4 | Marketing: `contacts`, `segments`, `campaigns`, `automations`, `analytics` con ClickHouse, `policy` | Pendiente |
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
