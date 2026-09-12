**  
CORE FORCE MAIL**

Informe técnico de arquitectura

**Correo corporativo + transaccional + marketing**

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><strong>Propósito del documento<br />
</strong>Definir desde cero una arquitectura robusta de producción, basada en microservicios y arquitectura hexagonal, con backend Go, frontend React, PostgreSQL, NATS JetStream y separación operativa entre correo corporativo y marketing. El diseño conserva motores de correo maduros y reemplaza las capas de aplicación y administración por componentes propios.</th>
</tr>
</thead>
<tbody>
</tbody>
</table>

Versión 1.0 • Septiembre 2026

# Resumen ejecutivo

Core Force Mail se plantea como una plataforma de correo empresarial de nueva generación. No se propone reescribir desde cero los protocolos SMTP/IMAP ni los motores anti-spam, sino mantener componentes maduros —Postfix, Dovecot y Rspamd— y construir alrededor de ellos una capa moderna, desacoplada y escalable en Go y React.

La plataforma se divide en dos dominios operativos principales: Correo Corporativo y Marketing/Transaccional. Ambos comparten identidad, tenants, políticas, auditoría y datos maestros, pero se despliegan de forma separada para evitar que una campaña masiva degrade el correo empresarial. El envío de marketing y transaccional se apoya en Amazon SES, mientras que el correo corporativo conserva recepción, almacenamiento y acceso IMAP mediante infraestructura propia.

| **Decisión**              | **Selección**                                        |
|---------------------------|------------------------------------------------------|
| Arquitectura              | Microservicios + arquitectura hexagonal por servicio |
| Backend                   | Go                                                   |
| Frontend                  | React + TypeScript                                   |
| Base relacional           | PostgreSQL                                           |
| Cache                     | Redis                                                |
| Mensajería                | NATS JetStream                                       |
| Correo corporativo        | Postfix + Dovecot + Rspamd                           |
| Marketing/transaccional   | Go workers + Amazon SES                              |
| Analítica de alto volumen | ClickHouse                                           |
| Búsqueda semántica        | PostgreSQL + pgvector                                |
| Objetos/adjuntos          | Amazon S3                                            |
| Cloud objetivo            | AWS                                                  |

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><strong>Principio rector<br />
</strong>Go y React constituyen el producto propio. Los componentes C existentes se mantienen únicamente donde aportan décadas de madurez protocolaria. PHP se elimina de la capa de aplicación; Python se minimiza y queda reservado a herramientas puntuales que no justifiquen reimplementación.</th>
</tr>
</thead>
<tbody>
</tbody>
</table>

# Índice

- 1\. Objetivos y alcance

- 2\. Principios de diseño

- 3\. Arquitectura general

- 4\. Separación Corporativo vs Marketing

- 5\. Arquitectura hexagonal

- 6\. Microservicios propuestos

- 7\. Stack tecnológico

- 8\. Diseño de datos y multi-tenancy

- 9\. Mensajería y colas

- 10\. Flujo de correo corporativo

- 11\. Flujo de marketing y transaccional

- 12\. Seguridad y Zero Trust

- 13\. Entregabilidad y reputación

- 14\. Observabilidad y auditoría

- 15\. Infraestructura AWS

- 16\. Dimensionamiento inicial

- 17\. Escalabilidad y alta disponibilidad

- 18\. Organización de repositorios

- 19\. Fases de implementación

- 20\. Riesgos y controles

- 21\. Decisiones finales

# 1. Objetivos y alcance

## 1.1 Objetivo general

Diseñar e implementar una plataforma de correo para producción capaz de prestar servicios de correo corporativo, correo transaccional y marketing digital en un único ecosistema, con aislamiento por tenant, escalabilidad horizontal, seguridad empresarial y capacidad de integrarse con Core Force ERP y otros sistemas mediante API.

## 1.2 Objetivos específicos

- Reemplazar la capa de administración tradicional basada en PHP/Python por servicios de negocio en Go.

- Disponer de una interfaz moderna en React + TypeScript, independiente del motor de correo.

- Mantener Postfix, Dovecot y Rspamd como motores especializados de SMTP, IMAP y filtrado.

- Implementar marketing y correo transaccional sobre Amazon SES, con separación de pools, reputación y eventos.

- Implementar una arquitectura multi-tenant preparada para aislamiento por celdas y clientes enterprise dedicados.

- Aplicar arquitectura hexagonal para que la lógica de negocio no dependa de AWS, Postfix, Dovecot, PostgreSQL ni SES.

- Manejar procesos asíncronos mediante NATS JetStream y workers Go.

- Incorporar desde el diseño controles de ciberseguridad, observabilidad, auditoría y recuperación ante desastres.

## 1.3 Fuera de alcance inicial

- Reescribir Postfix o implementar SMTP completo desde cero.

- Reescribir Dovecot o implementar IMAP completo desde cero.

- Crear un motor anti-spam propio que sustituya a Rspamd.

- Operar desde el día uno una red propia de IPs de marketing cuando SES ya resuelve esta capa.

- Guardar binarios de mensajes y adjuntos grandes directamente en PostgreSQL.

# 2. Principios de diseño

| **Principio**                   | **Aplicación**                                                               |
|---------------------------------|------------------------------------------------------------------------------|
| Separación de responsabilidades | Corporate Mail y Marketing tienen cargas, riesgos y escalado independientes. |
| Hexagonal                       | El dominio no conoce infraestructura; usa puertos/interfaces.                |
| Event-driven                    | Las tareas largas se publican a NATS y son procesadas por workers.           |
| Defense in depth                | Múltiples barreras: identidad, RBAC, cell routing, RLS, red privada e IAM.   |
| Stateless cuando sea posible    | APIs Go sin estado; sesión/token fuera del proceso.                          |
| Managed first                   | RDS, S3, SES y servicios AWS administrados cuando reduzcan riesgo operativo. |
| Observabilidad por defecto      | Logs, métricas y trazas desde la primera versión.                            |
| Escalado independiente          | Marketing puede multiplicar workers sin tocar IMAP corporativo.              |
| Tenant isolation                | Aislamiento lógico y físico progresivo según plan/riesgo.                    |

# 3. Arquitectura general

INTERNET  
│  
CloudFront / AWS WAF  
│  
React + TypeScript  
│  
Go Gateway  
│  
┌───────────────────────┼────────────────────────┐  
│ │ │  
CONTROL PLANE CORPORATE MAIL MARKETING / TX  
Identity / Tenant Postfix / Dovecot Campaign Services  
Billing / Policy Rspamd Go Workers  
│ │ │  
└───────────────┬───────┴───────────────┬────────┘  
│ │  
PostgreSQL / Redis NATS JetStream  
│ │  
│ Amazon SES  
│ │  
S3 Internet recipients  
│  
ClickHouse / pgvector

La arquitectura separa el plano de control de los planos de ejecución. El plano de control conoce tenants, dominios, planes, roles, políticas, configuración y routing. El plano corporativo gestiona buzones, recepción y acceso IMAP. El plano de marketing se orienta a campañas, segmentación, automatización, tracking y entrega mediante SES.

# 4. Separación Corporativo vs Marketing

## 4.1 Por qué deben separarse

| **Aspecto**           | **Corporativo**                    | **Marketing / Transaccional**  |
|-----------------------|------------------------------------|--------------------------------|
| Objetivo              | Comunicación diaria entre personas | Envíos automatizados o masivos |
| Patrón                | Constante, bajo/moderado volumen   | Picos y lotes grandes          |
| Recepción             | Sí                                 | Normalmente no                 |
| IMAP                  | Sí                                 | No requerido                   |
| Persistencia de buzón | Sí                                 | Solo metadata/eventos          |
| Motor de entrega      | Postfix / relay                    | AWS SES                        |
| Riesgo reputacional   | Debe protegerse al máximo          | Variable según campañas        |
| Escalado              | Conexiones y almacenamiento        | Workers y throughput           |
| Servidor recomendado  | 8 GB RAM inicial                   | 4 GB RAM inicial               |

## 4.2 Despliegue recomendado

SERVIDOR / ENTORNO A - CORPORATIVO  
Postfix + Dovecot + Rspamd  
4 vCPU / 8 GB RAM inicial  
almacenamiento persistente  
  
SERVIDOR / ENTORNO B - MARKETING  
Go campaign workers + adapters SES  
2 vCPU / 4 GB RAM inicial  
escalado horizontal  
  
SERVICIOS COMPARTIDOS ADMINISTRADOS  
PostgreSQL RDS Multi-AZ  
Redis / ElastiCache  
NATS JetStream  
S3  
Observabilidad

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><strong>Regla de aislamiento<br />
</strong>Una campaña masiva, un tenant abusivo o un pico de tracking no debe afectar la lectura, recepción ni envío cotidiano de los buzones corporativos.</th>
</tr>
</thead>
<tbody>
</tbody>
</table>

# 5. Arquitectura hexagonal

Cada microservicio seguirá arquitectura hexagonal (Ports & Adapters). El dominio contiene las reglas de negocio y no importa librerías concretas de AWS, PostgreSQL, SES, Postfix o NATS. Los adaptadores implementan los puertos definidos por el dominio.

ADAPTADORES DE ENTRADA  
HTTP / gRPC / NATS Consumer / Scheduler  
│  
▼  
APPLICATION LAYER  
Casos de uso Go  
│  
▼  
DOMAIN  
Entidades + reglas + puertos  
│  
▼  
ADAPTADORES DE SALIDA  
PostgreSQL / Redis / SES / Postfix / NATS / S3

## 5.1 Ejemplo de puertos

type MailSender interface {  
Send(ctx context.Context, message Message) (DeliveryID, error)  
}  
  
type CampaignRepository interface {  
Create(ctx context.Context, c Campaign) error  
Find(ctx context.Context, id CampaignID) (Campaign, error)  
}  
  
type EventPublisher interface {  
Publish(ctx context.Context, event DomainEvent) error  
}

Con este diseño, un adapter SES puede reemplazarse por otro proveedor sin modificar el dominio. Del mismo modo, Postfix puede quedar encapsulado en un adapter dedicado y las pruebas unitarias pueden usar implementaciones en memoria.

# 6. Microservicios propuestos

## 6.1 Plano de control

| **Servicio**     | **Responsabilidad**                                                |
|------------------|--------------------------------------------------------------------|
| identity-service | Usuarios, sesiones, MFA, passkeys, tokens, recuperación de cuenta. |
| tenant-service   | Alta de empresas, estado, cell asignada, límites y configuración.  |
| access-service   | RBAC/ABAC, roles, permisos, scopes API.                            |
| domain-service   | Dominios, DNS requerido, DKIM, SPF, DMARC, verificación.           |
| billing-service  | Planes, cuotas, metering, suscripciones.                           |
| audit-service    | Registro inmutable de acciones sensibles.                          |
| policy-service   | Rate limits, restricciones, reglas de envío y seguridad.           |

## 6.2 Correo corporativo

| **Servicio**          | **Responsabilidad**                                     |
|-----------------------|---------------------------------------------------------|
| mailbox-service       | Buzones, cuotas, aliases, lifecycle.                    |
| mail-routing-service  | Mapeo dominio/buzón, reglas y aliases.                  |
| mail-admin-service    | Operaciones administrativas sobre Postfix/Dovecot.      |
| mail-search-service   | Metadata e integración opcional con pgvector.           |
| mail-security-service | Políticas antispam, cuarentena, allow/deny lists.       |
| mail-storage-service  | Referencias de almacenamiento y políticas de retención. |

## 6.3 Marketing y transaccional

| **Servicio**        | **Responsabilidad**                                         |
|---------------------|-------------------------------------------------------------|
| contact-service     | Contactos, atributos, consentimiento y origen.              |
| segment-service     | Segmentación dinámica y estática.                           |
| campaign-service    | Campañas, estados, programación y lifecycle.                |
| template-service    | Plantillas, variables y versionado.                         |
| automation-service  | Flujos disparados por eventos y reglas.                     |
| scheduler-service   | Programación de campañas y jobs.                            |
| sender-orchestrator | Expansión de destinatarios, políticas y publicación a cola. |
| sender-worker       | Envío efectivo vía SES.                                     |
| tracking-service    | Aperturas/clics cuando corresponda y bajo configuración.    |
| bounce-service      | Rebotes duros/blandos y actualización de estado.            |
| complaint-service   | Quejas y eventos de spam.                                   |
| suppression-service | Lista global/tenant de exclusiones.                         |
| reputation-service  | Score, límites y selección de pool/configuration set.       |
| analytics-service   | Agregación y consultas de métricas.                         |

# 7. Stack tecnológico

| **Capa**         | **Tecnología**                                | **Rol**                                             |
|------------------|-----------------------------------------------|-----------------------------------------------------|
| Frontend         | React + TypeScript                            | Panel administrativo, correo, campañas y analytics. |
| Backend          | Go                                            | APIs, dominio, workers, jobs y adapters.            |
| Relacional       | PostgreSQL                                    | Datos transaccionales y metadata.                   |
| Vectorial        | pgvector                                      | Búsqueda semántica e IA por tenant.                 |
| Cache            | Redis                                         | Cache, counters, locks ligeros y rate limiting.     |
| Event bus        | NATS JetStream                                | Mensajería durable, consumers y retries.            |
| SMTP corporativo | Postfix                                       | Recepción/relay/cola SMTP.                          |
| IMAP             | Dovecot                                       | Acceso a buzones, índices y entrega local.          |
| Anti-spam        | Rspamd                                        | Scoring, reglas, DKIM/DMARC helpers.                |
| Entrega masiva   | Amazon SES                                    | Marketing y transaccional.                          |
| Object storage   | Amazon S3                                     | Adjuntos, exports, imports y artefactos.            |
| Analytics        | ClickHouse                                    | Eventos de gran volumen y series agregadas.         |
| Edge             | CloudFront + WAF                              | Entrega frontend y protección HTTP.                 |
| Observabilidad   | OpenTelemetry + Prometheus/Grafana/CloudWatch | Logs, métricas y trazas.                            |

## 7.1 Componentes que se eliminan o minimizan

| **Tecnología**       | **Decisión**                    | **Motivo**                                                   |
|----------------------|---------------------------------|--------------------------------------------------------------|
| PHP                  | Eliminar de la capa de producto | La lógica administrativa se reimplementa en Go.              |
| Python               | Minimizar                       | Solo utilidades puntuales donde aporte valor real.           |
| MariaDB              | Reemplazar                      | PostgreSQL unifica el stack de datos.                        |
| C de Postfix/Dovecot | Mantener                        | Motores protocolarios maduros; no es rentable reescribirlos. |
| Bash                 | Minimizar                       | Solo bootstrap/operación; no lógica de negocio.              |

# 8. Diseño de datos y multi-tenancy

## 8.1 Modelo por celdas

GLOBAL CONTROL DATABASE  
tenants  
cells  
plans  
subscriptions  
global_domains  
routing  
  
CELL PE-01  
PostgreSQL  
Redis  
workers  
tenants A..N  
  
CELL PE-02  
PostgreSQL  
Redis  
workers  
tenants N+1..M  
  
ENTERPRISE CELL  
PostgreSQL dedicado  
Redis dedicado  
workers dedicados

La arquitectura de celdas limita el radio de impacto de fallos y permite mover clientes de alto consumo a infraestructura dedicada. Dentro de cada cell, los registros mantienen tenant_id y PostgreSQL RLS como barrera adicional.

## 8.2 Separación de tipos de datos

| **Dato**                                         | **Destino**                                |
|--------------------------------------------------|--------------------------------------------|
| Tenants, usuarios, dominios, campañas, contactos | PostgreSQL                                 |
| Mensajes/buzones corporativos                    | Dovecot mail storage / volumen persistente |
| Adjuntos, imports, exports                       | S3                                         |
| Cache, rate limits, locks                        | Redis                                      |
| Eventos de workflow                              | NATS JetStream                             |
| Eventos masivos de delivery/open/click           | ClickHouse                                 |
| Embeddings                                       | pgvector                                   |

# 9. Mensajería y colas

Se utilizarán dos niveles de cola con propósitos diferentes: la cola SMTP de Postfix para entrega corporativa y NATS JetStream para procesos de aplicación, campañas y eventos.

CORPORATIVO  
Postfix Queue -\> retry SMTP -\> destino  
  
MARKETING  
Campaign -\> NATS JetStream -\> Go Workers -\> SES  
│  
├-\> retry  
├-\> delayed processing  
└-\> dead-letter policy

## 9.1 Subjects recomendados

- tenant.created

- domain.verified

- mailbox.created

- campaign.created

- campaign.scheduled

- email.requested

- email.sent

- email.delivered

- email.bounced

- email.complained

- email.opened

- email.clicked

- contact.unsubscribed

## 9.2 Semántica

- Procesamiento at-least-once para eventos críticos.

- Consumers durables.

- Idempotency keys en comandos de envío.

- Retries con backoff.

- Dead-letter stream para eventos agotados.

- Correlation ID y trace ID propagados entre servicios.

# 10. Flujo de correo corporativo

ENTRANTE  
Internet -\> Postfix -\> Rspamd -\> políticas -\> Dovecot/LMTP -\> Mail Storage  
-\> evento NATS -\> indexing/audit  
  
SALIENTE  
Cliente IMAP/SMTP -\> Submission -\> auth -\> policy -\> Postfix -\> Internet/relay

## 10.1 Funciones

- Recepción SMTP 24/7.

- Submission autenticado.

- IMAPS para clientes compatibles.

- Aliases y dominios múltiples.

- Cuotas por mailbox y plan.

- Antispam y cuarentena.

- Auditoría administrativa.

- Búsqueda full-text/semántica opcional.

# 11. Flujo de marketing y transaccional

React / API  
│  
Campaign Service  
│  
Segment Service -\> recipients  
│  
Policy + Consent + Suppression  
│  
NATS email.requested  
│  
Go Sender Workers  
│  
SES Configuration Set / IP Pool  
│  
Internet  
│  
SES Events -\> ingestion -\> NATS -\> ClickHouse/PostgreSQL

## 11.1 Diferencias entre marketing y transaccional

| **Tipo**      | **Ejemplos**                       | **Prioridad**      | **Pool**             |
|---------------|------------------------------------|--------------------|----------------------|
| Transaccional | OTP, facturas, pedidos, alertas    | Alta               | Transactional        |
| Marketing     | Promociones, newsletters, campañas | Normal/planificada | Marketing            |
| Sistema       | Notificaciones internas            | Alta               | System/Transactional |

# 12. Seguridad y Zero Trust

## 12.1 Controles base

| **Área**          | **Control**                                                          |
|-------------------|----------------------------------------------------------------------|
| Identidad         | MFA/passkeys, sesiones revocables, access tokens cortos.             |
| Autorización      | RBAC/ABAC, scopes, least privilege.                                  |
| Tenant            | Cell routing + tenant_id + RLS + autorización Go.                    |
| Red               | Private subnets; DB/Redis/NATS no públicos.                          |
| Secretos          | AWS Secrets Manager + KMS.                                           |
| Datos en tránsito | TLS 1.2/1.3; mTLS interno cuando aplique.                            |
| Datos en reposo   | Cifrado KMS en RDS/S3/volúmenes/backups.                             |
| API               | WAF, rate limiting, schema validation, idempotencia.                 |
| Supply chain      | SAST, govulncheck, SBOM, escaneo de contenedores, imágenes firmadas. |
| Adjuntos          | Cuarentena, MIME validation, antivirus, políticas de tipo/tamaño.    |
| Auditoría         | Eventos append-only y retención definida.                            |

## 12.2 Exposición pública mínima

PÚBLICO  
443 HTTPS  
25 SMTP  
465/587 SMTP submission  
993 IMAPS  
  
PRIVADO  
5432 PostgreSQL  
6379 Redis  
4222/8222 NATS  
servicios internos Go

# 13. Entregabilidad y reputación

La reputación de envío se protege separando correo corporativo de marketing y segmentando el tráfico de SES mediante configuration sets/pools. El reputation-service aplica políticas antes de autorizar el envío.

Tenant -\> Sending Class -\> Reputation Score -\> Policy  
│  
┌──────────────┼──────────────┐  
▼ ▼ ▼  
Transactional Marketing Good Restricted  
│ │ │  
SES Pool SES Pool Limited Pool

- SPF, DKIM y DMARC por dominio.

- PTR/rDNS en infraestructura SMTP propia.

- Suppression list por hard bounce, complaint y unsubscribe.

- Rate limits por tenant, dominio, campaña y plan.

- Warm-up administrado cuando se utilicen IP dedicadas.

- Suspensión automática de tenants con patrones abusivos.

# 14. Observabilidad y auditoría

## 14.1 Tres pilares

| **Pilar** | **Ejemplos**                                                               |
|-----------|----------------------------------------------------------------------------|
| Logs      | JSON estructurado, tenant_id, trace_id, request_id, service, severity.     |
| Métricas  | latency, error rate, queue lag, delivery, bounces, IMAP sessions, CPU/RAM. |
| Trazas    | HTTP -\> service -\> NATS -\> worker -\> SES.                              |

## 14.2 Métricas críticas

- NATS consumer lag

- SES reject/bounce/complaint rate

- Postfix queue depth

- Dovecot concurrent sessions

- Rspamd processing latency

- PostgreSQL connections/locks/replication lag

- Redis memory/evictions

- Worker throughput

- Cell saturation

- S3/storage growth

# 15. Infraestructura AWS

AWS ACCOUNT PROD  
│  
├── CloudFront + WAF  
├── ALB  
├── ECS/Fargate or ECS/EC2  
│ ├── Go control services  
│ └── Go marketing workers  
├── RDS PostgreSQL Multi-AZ  
├── ElastiCache Redis  
├── EC2/ECS Mail Corporate  
│ ├── Postfix  
│ ├── Dovecot  
│ └── Rspamd  
├── Amazon SES  
├── S3  
├── KMS  
├── Secrets Manager  
└── CloudWatch / OTel stack

## 15.1 Ambientes

- AWS Account DEV

- AWS Account STAGING

- AWS Account PROD

La separación por cuenta evita que credenciales o errores de desarrollo tengan alcance directo sobre producción.

# 16. Dimensionamiento inicial

## 16.1 Dos entornos principales

| **Entorno**       | **CPU**  | **RAM** | **Notas**                                                                           |
|-------------------|----------|---------|-------------------------------------------------------------------------------------|
| Corporate Mail    | 4 vCPU   | 8 GB    | Postfix+Dovecot+Rspamd. Aumentar a 16 GB con más usuarios/indexación/antivirus.     |
| Marketing Workers | 2 vCPU   | 4 GB    | Suficiente como nodo inicial porque SES realiza la entrega. Escala horizontalmente. |
| Go APIs           | 2-4 vCPU | 4-8 GB  | Puede agrupar múltiples microservicios ligeros.                                     |
| PostgreSQL RDS    | 2-4 vCPU | 8-16 GB | Multi-AZ; ajustar por conexiones/IOPS.                                              |
| NATS              | 2 vCPU   | 4 GB    | JetStream durable; producción ideal en cluster.                                     |
| Redis             | \-       | 2-4 GB  | Servicio administrado.                                                              |

## 16.2 Qué consume recursos

| **Carga**          | **Principal recurso**    |
|--------------------|--------------------------|
| Buzones y adjuntos | Storage                  |
| IMAP concurrente   | RAM + conexiones         |
| Antispam/antivirus | CPU + RAM                |
| Campañas           | Workers + SES throughput |
| Tracking           | I/O + analytics          |
| Búsqueda semántica | CPU/DB + embeddings      |

# 17. Escalabilidad y alta disponibilidad

## 17.1 Escalado de marketing

NORMAL  
2 workers  
  
CAMPAÑA GRANDE  
2 -\> 10 -\> 30 workers  
  
FINALIZA CAMPAÑA  
30 -\> 2 workers

## 17.2 Escalado corporativo

El correo corporativo escala principalmente por almacenamiento, conexiones IMAP, throughput SMTP y filtros. Debe priorizar continuidad y consistencia sobre elasticidad extrema.

## 17.3 Estrategia de cells

- No sobrecargar una cell; crear nuevas cells antes de saturación.

- Enterprise puede migrar a cell dedicada.

- Nuevos tenants se asignan según capacidad y región.

- Backups y recuperación por cell reducen radio de impacto.

# 18. Organización de repositorios

## 18.1 Opción recomendada: monorepo lógico inicial

core-force-mail/  
├── apps/  
│ ├── web-react/  
│ ├── api-gateway/  
│ ├── identity-service/  
│ ├── tenant-service/  
│ ├── domain-service/  
│ ├── mailbox-service/  
│ ├── campaign-service/  
│ ├── contact-service/  
│ ├── sender-worker/  
│ └── analytics-service/  
├── internal/  
│ ├── platform/  
│ ├── observability/  
│ ├── auth/  
│ └── events/  
├── packages/  
│ ├── contracts/  
│ └── ui/  
├── deploy/  
│ ├── terraform/  
│ └── containers/  
└── docs/

## 18.2 Estructura hexagonal por servicio

campaign-service/  
├── cmd/server/  
├── internal/domain/  
├── internal/application/  
├── internal/ports/  
├── internal/adapters/http/  
├── internal/adapters/postgres/  
├── internal/adapters/nats/  
├── internal/adapters/ses/  
└── migrations/

# 19. Fases de implementación

## Fase 1 - Foundation

- IAM, VPC, Terraform, CI/CD.

- Identity, tenant, access y audit.

- PostgreSQL, Redis y NATS.

- React shell y design system.

## Fase 2 - Corporate Mail

- Domain service + DNS validation.

- Mailbox/alias management.

- Adapters Postfix/Dovecot/Rspamd.

- Submission/IMAP, quotas y auditoría.

## Fase 3 - Transactional

- SES adapter.

- Template service.

- API keys/scopes.

- NATS send pipeline.

- Delivery/bounce/complaint ingestion.

## Fase 4 - Marketing

- Contacts y consent.

- Segments.

- Campaigns y scheduler.

- Suppression/reputation.

- Tracking y analytics.

- Automations.

## Fase 5 - AI/Advanced

- pgvector y búsqueda semántica.

- Clasificación de correos.

- Resúmenes y respuesta asistida.

- Segmentación asistida por IA.

# 20. Riesgos y controles

| **Riesgo**           | **Impacto**                      | **Control**                                      |
|----------------------|----------------------------------|--------------------------------------------------|
| Reescribir demasiado | Retrasos y bugs protocolarios    | Mantener Postfix/Dovecot/Rspamd.                 |
| Tenant leakage       | Crítico                          | Cells + RLS + auth + tests de aislamiento.       |
| Campaña abusiva      | Reputación/costos                | Policy/reputation/suppression + SES pools.       |
| Saturación de DB     | Degradación global               | PgBouncer, cells, read replicas, observabilidad. |
| Pérdida de eventos   | Métricas/acciones inconsistentes | JetStream durable + idempotencia.                |
| Adjunto malicioso    | Compromiso                       | Cuarentena + scanning + tipo/tamaño.             |
| Fallo de mail node   | Interrupción corporate           | HA progresiva, backups, runbooks.                |
| Costos inesperados   | Margen reducido                  | Quotas, metering y alertas presupuestarias.      |

# 21. Decisiones finales

| **Tema**               | **Decisión final**                               |
|------------------------|--------------------------------------------------|
| Arquitectura           | Microservicios con arquitectura hexagonal.       |
| Producto propio        | Go + React + PostgreSQL.                         |
| PHP                    | Eliminar.                                        |
| Python                 | Minimizar.                                       |
| Motores C              | Mantener Postfix/Dovecot/Rspamd.                 |
| Corporate vs Marketing | Separación física/lógica.                        |
| Corporate baseline     | 4 vCPU / 8 GB.                                   |
| Marketing baseline     | 2 vCPU / 4 GB + SES.                             |
| BBDD                   | PostgreSQL por cells; control plane separado.    |
| Vectorial              | pgvector opcional, desacoplado del core.         |
| Colas                  | Postfix Queue + NATS JetStream.                  |
| Analítica              | ClickHouse para alto volumen.                    |
| Cloud                  | AWS; managed services donde reduzcan riesgo.     |
| Seguridad              | Zero Trust + Defense in Depth + Least Privilege. |

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><strong>Conclusión<br />
</strong>Core Force Mail no debe ser entendido como una reescritura literal de Mailcow. El objetivo es construir una plataforma propia, modular y orientada a producto, manteniendo únicamente los motores protocolarios maduros. La arquitectura resultante permite ofrecer correo corporativo, transaccional y marketing bajo un mismo ecosistema, pero con aislamiento operativo, escalabilidad independiente y controles de seguridad adecuados para producción.</th>
</tr>
</thead>
<tbody>
</tbody>
</table>

# Anexo A. Flujo end-to-end de campaña

1.  Usuario crea la campaña en React.

2.  campaign-service valida tenant, plan y permisos.

3.  segment-service resuelve destinatarios.

4.  suppression-service elimina contactos no elegibles.

5.  policy-service calcula límites y clase de envío.

6.  sender-orchestrator publica email.requested en NATS.

7.  sender-workers consumen de forma durable e idempotente.

8.  SES procesa la entrega.

9.  Eventos de SES regresan al ingestion service.

10. NATS distribuye bounce/delivery/complaint/open/click.

11. PostgreSQL actualiza estado operacional y ClickHouse almacena eventos analíticos.

12. React consulta analytics-service y presenta resultados.

# Anexo B. Criterios de aceptación de producción

- Despliegues reproducibles mediante IaC.

- Backups y restore probados.

- MFA obligatorio para administradores.

- Secrets fuera del código y rotación definida.

- Pruebas automatizadas de aislamiento tenant.

- Idempotencia de envío probada.

- Prueba de caída de worker sin pérdida de mensajes.

- Prueba de saturación de campaña sin degradar corporate.

- Alertas de cola, rebotes, complaints y DB activas.

- Runbooks de incidentes y recuperación disponibles.

- Auditoría de acciones críticas consultable.

- Prueba de load y capacity baseline documentada.
