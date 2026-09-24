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

Hecho (2026-09-23, rama de 1-A, sin desplegar; detalle en las filas de `templates` y `transactional` de
`Arquitectura_Core_Force_Mail.md`, operacion en `Operacion_Despliegue.md`, «Envio de prueba de plantillas y correo
en el navegador»). Migraciones: registro `039_templates_test_send_permissions.sql` y empresa
`transactional/07_template_test_sends.sql`.

* Alta: el formulario ofrece "Desde la galeria", "En blanco" y "HTML propio". Los dos primeros abren el editor en
  `/sending/templates/new/editor` con el nombre, la descripcion y el tipo; la plantilla se crea con `POST /templates`
  (con `editor`) al guardar, publicar o probar el primer diseno, y el editor pasa a su ruta. Un nombre repetido pide
  otro en el propio editor sin perder el diseno.
* Envio de prueba: `POST /api/v1/templates/{id}/versions/{v}/test-send`
  `{"from": {"email", "name"?}, "reply_to"?, "to": [1..5], "variables"?: {..}}`, permiso `templates/test_send/create`
  -> 202 `{"messages": [{"id", "status", "email"}], "suppressed": [{"email", "reason"}]}`. Remitente: la parte local
  mas uno de los dominios con `can_send` de `GET /api/v1/transactional/sending-domains` (permiso
  `transactional/sending_domains/read`). No reutiliza el lote de campanas (exige campana, contacto y plantilla de
  marketing publicada): va por `POST /internal/transactional/test-send`, con las mismas piezas internas (remitente
  verificado, supresion, reputation, `is_test`, carril por clase). Tope `TRANSACTIONAL_TEST_SENDS_PER_HOUR`. Boton
  en el editor (guarda antes si hay cambios) y en cada version del detalle.
* No cuenta: `analytics` ya descartaba `test`; ahora tampoco `billing` ni el volumen de `reputation` (sus rebotes
  permanentes y quejas si), ni `GET /transactional/stats`. Las pruebas de campana tambien quedan fuera de esos
  contadores.
* `view_in_browser_url`: `GET /api/v1/public/transactional/view?t&m&x&sig`, firmado sobre `view`, empresa, mensaje y
  caducidad (`VIEW_IN_BROWSER_TTL`, 90 dias). Sirve el HTML guardado en `transactional.messages` al renderizar: es
  exactamente lo que recibio esa persona y no depende de que la plantilla siga igual ni de volver a resolver
  variables. Pagina sin ejecucion (CSP `default-src 'none'` + `sandbox`), sin marco, sin indexar y sin cache; el
  gateway conserva esa CSP (`"content": "untrusted_html"`). El host de `PUBLIC_BASE_URL` queda fuera de los UTM
  (1-B).
* Pendiente: comprobar en el navegador con el backend desplegado el alta desde la galeria, la prueba desde el editor
  y la pagina publica con un correo real de SES.

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

Hecho (2026-09-23, rama de 2-E, sin desplegar; detalle en las filas de `contacts` y `automations` de
`Arquitectura_Core_Force_Mail.md` y en `Modelo_de_Datos_y_Celdas.md`). Migraciones de empresa
`contacts/05_engagement.sql` y `automations/03_branches_and_dates.sql`; sin migracion de registro (042 sin usar:
todo usa los permisos existentes de `segments` y `automations`).

* Proyeccion: `contacts.engagement` por contacto y envio (campana o flujo), alimentada por tres durables de
  `transactional.email.delivered/opened/clicked`, idempotente por construccion (LEAST/GREATEST), poda por
  `CONTACTS_ENGAGEMENT_RETENTION_DAYS` (400; 365..3650). "Ultimas N campanas" son las ultimas que recibio cada
  contacto, sin consumir `campaigns.*`.
* DSL: `campaign` `opened|clicked`, `last_campaigns` `opened|clicked|not_opened` (N 1..50, `not_opened` exige
  haber recibido N), `last_days` `opened|clicked` (N 1..365). El catalogo (`GET /segments/meta`) publica los
  campos, `max` por campo y los topes; el editor de segmentos los ofrece con selector de campana y avisa de la
  limitacion de Apple Mail. La barrida de contacts contra suppression no se toca.
* Automatizaciones: grafo acotado (40 pasos, 20 de profundidad, sin ciclos, todo alcanzable; `domain/graph.go`),
  paso `branch` con `then`/`else` y condiciones `email_opened`, `email_clicked` (sobre un envio que domina la
  rama), `segment` y `attribute` (evaluadas en `POST /internal/contacts/match`). Disparador `contact.date`
  (aniversario de un atributo de fecha a una hora local, zona del contacto o de respaldo, una vez al ano por la
  clave `date:<ano>`; 29 de febrero el 28 en anos no bisiestos) con `POST /internal/contacts/anniversaries` y
  `AUTOMATIONS_DATE_SCAN_INTERVAL` (10 min). Flujos lineales anteriores compatibles sin migrar datos.
* Web: lienzo propio en `web/src/pages/automations` (`FlowCanvas.tsx`, `workflowGraph.ts`, sin dependencias
  nuevas, compatible con la CSP: posiciones por la API de estilos del DOM, sin `eval` ni HTML), con paleta
  arrastrable, huecos para insertar por teclado, ramas con dos salidas, reenlazado a cualquier paso y validacion
  en vivo; constructor de segmentos con las reglas nuevas.
* Pendiente: comprobar en el navegador con el backend desplegado y con aperturas reales de SES.

### 2-F. Captacion

* Formularios de suscripcion por empresa: campos del contacto, lista destino, doble opt-in obligatorio,
  proteccion anti-abuso (limite por IP, campo trampa, tiempo minimo), incrustables por script o iframe.
* Paginas de aterrizaje simples con el editor (bloques web) servidas en la ruta publica de la empresa.

Hecho (2026-09-23, rama de 2-F, sin desplegar; detalle en las filas de `contacts` y `templates` de
`Arquitectura_Core_Force_Mail.md`, operacion y rutas publicas en `Operacion_Despliegue.md`, «Captacion»). Migraciones:
registro `043_capture_permissions.sql`, empresa `contacts/06_subscription_forms.sql` y `templates/04_landing_pages.sql`.

* Formularios: CRUD con permisos `contacts/forms/*`, doble opt-in obligatorio sobre el flujo existente
  (`contacts.consent.requested` -> `automations` -> `transactional`), evidencia con texto aceptado e ip truncada,
  supresion respetada (a una excluida no se le pide), misma respuesta siempre, token firmado de un solo uso con tiempo
  minimo, campo trampa, cupos por IP y por formulario en Redis, CORS y `frame-ancestors` solo para los origenes
  declarados. Un envio publico no cambia los datos de un contacto existente. Estadisticas de envios y confirmaciones.
* Paginas: CRUD y versiones con permisos `templates/pages/*`, editor GrapesJS en modo web, HTML saneado y CSS
  comprobado al guardar, formulario incrustado por marcador, servidas en `/p/<empresa>/<slug>` con CSP sin scripts.
* Gateway: `"cors": "service"`, `"content": "embeddable_html"` y `"alias"` en las rutas publicas.
* Web: pestana Formularios en contactos (`/marketing/forms/:id` con vista previa, codigo para incrustar y
  estadisticas) y paginas en Envios (`/sending/pages`, detalle y editor a pantalla completa).
* Pendiente: comprobar en el navegador con el backend desplegado el editor de paginas y un formulario incrustado en un
  sitio de otro origen; un atributo obligatorio declarado despues de crear un formulario bloquea sus envios hasta que
  se anada al formulario.

## 4. Oleada 3

### 3-G. Claves de API y SMTP

* Claves de API por empresa (hash, alcance por permisos, caducidad, revocacion, auditoria) aceptadas por el
  gateway en la API publica de envio.
* Servicio `smtp-relay`: SMTP con TLS en `smtp.core-force.com` (2525 con STARTTLS y un puerto con TLS
  implicito), autenticacion con credenciales SMTP por empresa, entrega a `transactional` (mismas reglas que la
  API), nunca por Postfix. ADR propio.

Hecho (2026-09-23, rama de 3-G, sin desplegar; `docs/adr/0013-claves-de-api-y-relay-smtp.md`, filas de `gateway`,
`access-control`, `transactional` y `smtp-relay` de `Arquitectura_Core_Force_Mail.md`, `Usuarios_Roles_y_Acceso.md` 4.1 y
«Claves de API y relay SMTP» de `Operacion_Despliegue.md`). Migraciones: registro `044_access_control_api_keys.sql` (tablas y
permisos en la reservada) y empresa `transactional/08_raw_messages.sql`.

* Claves en access-control: `cfm_<prefijo>_<secreto>`, HMAC-SHA256 con `API_KEY_HASH_KEY` del almacen, alcance acotado al
  creador en el alta y en cada uso, caducidad, revocacion inmediata (marca en Redis), ultimo uso y outbox. Web en Acceso,
  Claves de API (el secreto solo se ve al crearla, con los datos de SMTP si `SMTP_RELAY_PUBLIC_HOST` esta puesta).
* Gateway: `api_key_routes` en `routes.json` (enviar y leer el estado de un mensaje), cupo por clave, RBAC por alcance.
* `smtp-relay` (servicio nuevo, `make new-service`, sin ruta en el gateway ni base): 2525 STARTTLS y 2465 TLS, AUTH PLAIN y
  LOGIN con la clave, freno, cupos, ClamAV, `pkg/rawmail` y entrega a `POST /internal/transactional/raw-messages`, que sale
  por SES con contenido Raw.
* Desplegado y abierto el 2026-09-24: secreto en el almacen, `ADDITIONAL_SAN` con `smtp.core-force.com`, DNS, `ses-envio` con
  `ses:SendRawEmail`, `SMTP_RELAY_BIND_ADDRESS=0.0.0.0` y `SMTP_RELAY_PUBLIC_HOST=smtp.core-force.com`. Pendiente: las reglas de
  UFW (root, solo documentan) y, si un cliente lo pide, acotar por origen en `DOCKER-USER`
  (`Operacion_Despliegue.md`), y ver en el navegador la pagina de claves (su API ya da `smtp.core-force.com`).

## 5. Riesgos

* 1-D toca el certificado que usan los motores: se despliega solo `acme-mail` y se comprueba que Postfix y
  Dovecot recargan con el certificado nuevo.
* 3-G abre un puerto publico nuevo: cortafuegos, fail2ban o limite equivalente, y monitorizacion propios.

## 6. Registro de estado

| Pieza | Estado |
|---|---|
| 1-A Editor | Desplegado en produccion (2026-09-23, `bfbf81a`): migraciones aplicadas en las dos empresas, servicios sanos y sin errores. Hecho en rama, sin desplegar (2026-09-23) |
| 1-B Enlaces y analitica | Desplegado en produccion (2026-09-23, `bfbf81a`): migraciones aplicadas en las dos empresas, servicios sanos y sin errores. Hecho en rama, sin desplegar (2026-09-23) |
| 1-C Campanas | Desplegado en produccion (2026-09-23, `bfbf81a`): migraciones aplicadas en las dos empresas, servicios sanos y sin errores. Hecho en rama (2026-09-23): fases de envio (`tenant/canonical/campaigns/03_phases.sql`), prueba A/B con decision auditada por outbox (`campaigns.campaign.ab_decided`), reenvio a quien no abrio (una vez, con la limitacion de Apple Mail documentada), envio por zona horaria con zona de respaldo indicada al programar (no hay zona de empresa en `organization`), `subject` opcional en el lote de `transactional`, `utm.content` por variante, `GET /campaigns/{id}/phases` y web de campanas. Sin migracion de registro (041 sin usar). Unitarias, integracion y `make e2e` sin SES real |
| 1-D Dominio de seguimiento | Hecho (2026-09-24): DNS, certificado (con `AUTODISCOVER_SAN=n`), identidad verificada en SES, borde sirviendo `clics.core-force.com` y `SES_TRACKING_DOMAIN` en la pila (`cfm-marketing`, HTTPS obligatorio) |
| 2-E Comportamiento y automatizaciones | Desplegado en produccion (2026-09-24, `4bb639e`): migraciones aplicadas en las dos empresas, servicios sanos y sin errores. Hecho en rama, sin desplegar (2026-09-23): `contacts/05_engagement.sql`, `automations/03_branches_and_dates.sql`, sin registro (042 sin usar). Unitarias, integracion y `make e2e` con la apertura sembrada en la outbox (sin SES real) |
| 2-F Captacion | Desplegado en produccion (2026-09-24, `4bb639e`): migraciones aplicadas en las dos empresas, servicios sanos y sin errores. Hecho en rama, sin desplegar (2026-09-23): formularios con doble opt-in obligatorio y anti abuso en `contacts`, paginas de aterrizaje en `templates`, rutas publicas en el gateway y web. Unitarias, integracion y `make e2e` |
| 3-G API y SMTP | Desplegado en produccion (2026-09-24, `4f78120`): registro `044` y `transactional/08` aplicados en las dos empresas, servicios sanos y sin errores, certificado de acme con `smtp.core-force.com`, DNS aplicado y `smtp-relay` solo en 127.0.0.1 (sin AUTH antes de STARTTLS, comprobado) hasta el mismo dia; abierto el 2026-09-24 tras aplicar `ses:SendRawEmail` en `ses-envio`: envio real por 2525 al simulador de SES `delivered`. Al probarlo aparecio y se corrigio (`8220fed`) que reputation leia el -1 de billing (plan sin limite) como remanente y denegaba todo envio de la empresa de la plataforma. Hecho en rama (2026-09-23): claves de API en access-control (`044`), gateway con `api_key_routes`, servicio `smtp-relay` (STARTTLS y TLS implicito, AUTH con la clave, ClamAV) y ruta de MIME crudo en `transactional` (`08_raw_messages.sql`, SES Raw). Unitarias, integracion y `make e2e` (API con clave, SMTP hasta la cola de transactional y revocacion; sin SES real) |
