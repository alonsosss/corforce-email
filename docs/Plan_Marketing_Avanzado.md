# Marketing avanzado: mejoras del editor, campanas, automatizaciones, captacion y SMTP

Estado a 2026-09-23. Sigue a `docs/Plan_Editor_Correos.md` (editor y verificador, en produccion) y a
`docs/Plan_SES_Endurecimiento.md`. Objetivo: la plataforma de marketing comparable a emBlue sin romper las
reglas de entregabilidad del proyecto (supresion antes de encolar, baja RFC 8058 firmada, clases separadas en
SES, reputacion por empresa). Cada oleada se integra, se prueba (checks, integracion, `make e2e`,
`make e2e-mail` si toca motores, navegador) y se despliega antes de la siguiente. Seccion 6: registro de estado.

## 1. Reglas comunes

* Molde hexagonal, migraciones aditivas e idempotentes con cabecera, permisos como datos de `access_control`
  sembrados por migracion de registro y exigidos en el handler, eventos por outbox, nada hardcodeado.
* Numeros de migracion de REGISTRO reservados para no chocar: 039 oleada 1-A, 040 oleada 1-B, 041 oleada 1-C,
  042 oleada 2-E, 043 oleada 2-F, 044 oleada 3-G. Las de empresa y celda, el siguiente libre del directorio
  del servicio en el momento de integrar (se renumera al integrar si choca).
* Toda direccion que reciba correo pasa por `suppression` antes de encolarse; nada nuevo sale sin baja en
  marketing ni fuera de `transactional` (unico que habla con SES).
* Web: textos en `web/src/i18n/es.ts`, iconos SVG del sistema de diseno, sin datos falsos.

## 2. Oleada 1

### 1-A. Editor: crear desde la galeria, envio de prueba y ver en el navegador

* Alta de plantilla sin HTML escrito a mano: el alta ofrece "Empezar desde la galeria" o "En blanco" y abre el
  editor; el backend admite crear la plantilla con el primer diseno del editor (HTML compilado incluido).
* Envio de prueba de una version desde el editor y desde el detalle: `POST /api/v1/templates/{id}/versions/{v}/test-send`
  `{ "to": ["..."], "variables": {..} }`, hasta 5 destinatarios, renderizado con los valores dados o los de
  ejemplo, asunto con prefijo de prueba, enviado por `transactional` con el remitente verificado de la empresa
  y marcado como prueba (no cuenta en estadisticas ni en facturacion; respeta la supresion). Reutiliza el
  camino del envio de prueba de campanas si existe.
* `view_in_browser_url`: enlace firmado (como la baja) a una pagina publica que muestra el correo tal como se
  envio a ese destinatario, con caducidad; rellenado en marketing y transaccional.

### 1-B. Enlaces y analitica

* UTM automaticos por campana (`utm_source`, `utm_medium=email`, `utm_campaign`, configurables y desactivables):
  se anaden al render de marketing sin tocar enlaces de baja, de ver en el navegador ni los que ya los traen.
* Clics por enlace: agregar la URL de cada evento de clic de SES por campana (sin datos personales en la
  agregacion) y exponerlo; mapa de calor sobre la vista previa de la campana (porcentaje de clics por enlace).

Hecho (2026-09-23, rama de 1-B, sin desplegar; detalle en las filas de `transactional` y `analytics` de
`Arquitectura_Core_Force_Mail.md`):

* Contrato del lote (`POST /internal/transactional/batch`): objeto opcional
  `"utm": {"enabled": true, "source": "", "campaign": "", "content": ""}`. Ausente = activado con valores
  derivados; `source` del nombre del remitente (o su dominio), `campaign` del nombre de la campana que envia
  `campaigns` (o de su id), `utm_medium=email` fijo. Configuracion de quien opera:
  `MARKETING_UTM_EXCLUDED_DOMAINS`. `campaigns` solo cambia la construccion del lote (`CampaignName` en
  `ports.BatchRequest`, rellenado en `orchestrator.go` y en el envio de prueba de `lifecycle.go`, y el objeto
  `utm` en `transactionalclient`). Pendiente: guardar por campana `enabled`, `source` y `content` editables
  (campo de `campaigns`, oleada de 1-C o posterior); hoy van los valores por defecto.
* Clics por enlace: migracion de empresa `analytics/03_campaign_links.sql`, sin permisos nuevos (se usa
  `analytics/reports/read`; el numero de registro 040 queda libre). API
  `GET /api/v1/analytics/campaigns/{id}/links`.
* Web: `/marketing/analytics/campaigns/:id/links` (`CampaignLinksPage`, `linkHeatmap.ts`), enlazada desde la
  tabla de campanas de la analitica y con un boton en el detalle de la campana.

### 1-C. Campanas

* Prueba A/B de asunto o de contenido (2 a 4 variantes), muestra configurable, criterio (aperturas o clics),
  ventana de decision y envio automatico de la ganadora al resto.
* Reenvio a quien no abrio, con otro asunto y un retraso configurable, una sola vez por campana.
* Envio por zona horaria del contacto (hora local objetivo), con la zona de la empresa para quien no la tiene.

### 1-D. Dominio de seguimiento propio (lo hace quien opera, sin agente)

`clics.core-force.com`: registro DNS, nombre en el certificado de `acme`, bloque del borde que reenvia a
`r.<region>.awstrack.me`, `SES_TRACKING_DOMAIN` en `setup-ses.sh` (fase D de `Plan_SES_Endurecimiento.md`).

## 3. Oleada 2

### 2-E. Segmentos por comportamiento y automatizaciones completas

* Proyeccion de interaccion por contacto (ultimas aperturas y clics por campana, desde los eventos
  `transactional.email.*`) en `contacts`, y reglas de segmento nuevas: abrio/hizo clic en una campana, en las
  ultimas N campanas o en N dias; no abrio.
* Automatizaciones con ramas (si abrio, si hizo clic, si cumple un segmento), disparadores por fecha de un
  atributo del contacto (cumpleanos, aniversario, con hora y zona), y editor visual del flujo en la web.

### 2-F. Captacion

* Formularios de suscripcion por empresa: campos del contacto, lista destino, doble opt-in obligatorio,
  proteccion anti-abuso (limite por IP, campo trampa, tiempo minimo), incrustables por script o iframe.
* Paginas de aterrizaje simples con el editor (bloques web) servidas en la ruta publica de la empresa.

## 4. Oleada 3

### 3-G. Claves de API y SMTP

* Claves de API por empresa (hash, alcance por permisos, caducidad, revocacion, auditoria) aceptadas por el
  gateway en la API publica de envio.
* Servicio `smtp-relay`: SMTP con TLS en `smtp.core-force.com` (2525 con STARTTLS y un puerto con TLS
  implicito), autenticacion con credenciales SMTP por empresa, entrega a `transactional` (mismas reglas que la
  API), nunca por Postfix. ADR propio.

## 5. Riesgos

* 1-D toca el certificado que usan los motores: se despliega solo `acme-mail` y se comprueba que Postfix y
  Dovecot recargan con el certificado nuevo.
* 3-G abre un puerto publico nuevo: cortafuegos, fail2ban o limite equivalente, y monitorizacion propios.

## 6. Registro de estado

| Pieza | Estado |
|---|---|
| 1-A Editor | En curso |
| 1-B Enlaces y analitica | Hecho en rama, sin desplegar (2026-09-23) |
| 1-C Campanas | Hecho en rama (2026-09-23): fases de envio (`tenant/canonical/campaigns/03_phases.sql`), prueba A/B con decision auditada por outbox (`campaigns.campaign.ab_decided`), reenvio a quien no abrio (una vez, con la limitacion de Apple Mail documentada), envio por zona horaria con zona de respaldo indicada al programar (no hay zona de empresa en `organization`), `subject` opcional en el lote de `transactional`, `utm.content` por variante, `GET /campaigns/{id}/phases` y web de campanas. Sin migracion de registro (041 sin usar). Unitarias, integracion y `make e2e` sin SES real |
| 1-D Dominio de seguimiento | Casi hecho (2026-09-23): DNS, certificado (con `AUTODISCOVER_SAN=n`), identidad verificada en SES y borde sirviendo `clics.core-force.com`; falta `SES_TRACKING_DOMAIN` en la pila (administrador de AWS) |
| 2-E Comportamiento y automatizaciones | Pendiente |
| 2-F Captacion | Pendiente |
| 3-G API y SMTP | Pendiente |
