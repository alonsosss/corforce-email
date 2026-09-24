# Plan: webmail innovador para la empresa

Estado: en ejecucion (2026-09-24). Continua `docs/Plan_Webmail_Competitivo.md` (fase 1, en produccion).
Decision del responsable del producto: implementar todo, sin fases de aprobacion intermedias, y
desplegar al terminar cada bloque verificado.

## 1. Objetivo

La fase 1 dejo el webmail a la altura de los del mercado. Esta fase lo lleva mas alla de lo
convencional con funciones pensadas para quien dirige una empresa pequena o mediana: que no se le
escape ningun seguimiento, que no le cuelen un fraude por correo, que sus clientes agenden con el sin
ir y venir de mensajes, que pueda enviar ficheros grandes sin servicios de terceros y que un asistente
le resuma y le proponga respuestas. Sin cambiar la arquitectura: los datos durables viven en el
servicio que los posee, el webmail sigue sin base propia y todo lo que llega al navegador pasa por API.

## 2. Alcance

| # | Funcion | Para que le sirve al empresario |
|---|---|---|
| G1 | Vista por conversaciones (hilos) | Ver una negociacion entera de un vistazo |
| G2 | Bandeja inteligente: Principal, Notificaciones y Boletines; baja de boletines con un clic (RFC 8058) | Lo importante primero; limpiar suscripciones sin buscar el enlace |
| G3 | Escudo antifraude: remitente externo, dominio parecido al propio, suplantacion del nombre de un companero, fallos de SPF, DKIM y DMARC | Frenar el fraude del CEO y la factura falsa antes de que alguien pague |
| G4 | Ficha del remitente al leer: contacto personal, correos anteriores y proximas reuniones con el | Contexto inmediato sin buscar |
| G5 | Posponer un mensaje hasta una fecha (vuelve arriba, sin leer) | Bandeja limpia, nada olvidado |
| G6 | Seguimiento: "avisame si no responden en N dias" | Ningun presupuesto o cobro sin respuesta se queda olvidado |
| G7 | Respuestas rapidas con variables (`{nombre}`, `{empresa}`) | Responder lo repetitivo en segundos |
| G8 | Ficheros grandes por enlace (hasta el tope servido), analizados con ClamAV, con caducidad y revocables | Enviar planos, contratos o videos sin WeTransfer |
| G9 | Calendario: zona horaria del evento, editar o borrar una sola aparicion de una serie | Corrige los limites conocidos de la fase 1 |
| G10 | Invitaciones de calendario (iTIP/iMIP): invitar por correo, aceptar, tentativo o rechazar desde el mensaje; el organizador ve las respuestas | Reuniones con clientes y equipo como en Outlook o Gmail |
| G11 | Disponibilidad del equipo al planificar (solo libre u ocupado, nunca el contenido) | Encontrar hueco sin preguntar a cada uno |
| G12 | Pagina publica de citas: el cliente elige un hueco libre y la reunion aparece en el calendario con invitacion a los dos | Agenda de ventas sin herramientas externas |
| G13 | Asistente: resumir un hilo, proponer respuesta, cambiar el tono, extraer tareas y fechas a eventos | Menos tiempo escribiendo; activable por empresa |
| G14 | Aplicacion instalable (PWA): icono en el escritorio y el movil | Acceso directo como una aplicacion |
| G15 | Correcciones de la fase 1: casillas de seleccion que se ven rellenas sin estar marcadas | Calidad visible |

Fuera de alcance, anotado en la seccion 9: bandeja compartida con asignaciones y notas internas,
calendarios compartidos editables, cifrado extremo a extremo.

## 3. Decisiones

1. **Durable, en su dueno.** Posponer, seguimiento y respuestas rapidas son datos del buzon: van a la
   celda (`mail-directory`) con el patron de `mail.scheduled_sends` (trabajador del webmail que reclama
   con arriendo). Enlaces de ficheros, citas y ajustes del asistente son datos de la empresa: van a la
   base de la empresa del servicio que los posee.
2. **Posponer mueve el mensaje a la carpeta `Snoozed`** (el usuario lo ve y lo puede sacar a mano); a su
   hora vuelve a su carpeta original sin `\Seen`. Si el usuario lo movio antes, la fila se cierra sin
   tocar nada.
3. **Seguimiento**: a su hora se busca respuesta en el buzon por `In-Reply-To`/`References` del
   `Message-ID` enviado; si no la hay, el mensaje enviado se copia a INBOX con `\Flagged` y sin `\Seen`
   (y el aviso SSE lo notifica). Si la hay, la fila se cierra en silencio.
4. **Bandeja inteligente y escudo antifraude se calculan en el webmail** al listar y leer, con reglas
   deterministas sobre cabeceras (`List-Id`, `List-Unsubscribe`, `Precedence`, `Auto-Submitted`,
   `Authentication-Results`/`X-Spamd-Result` que ponen nuestros motores) y el directorio de la empresa
   (dominios propios y nombres de companeros). Nada se envia fuera. La baja en un clic la hace el
   servicio (RFC 8058 `POST` a una URL `https` publica con proteccion SSRF: sin IP privadas, sin
   redirecciones, tiempo y tamano acotados) o, si solo hay `mailto:`, un correo desde el propio buzon.
5. **Ficheros grandes**: el fichero va al almacen de objetos (`pkg/objectstore`, MinIO en produccion),
   se analiza con ClamAV antes de publicarse y el correo lleva un enlace firmado
   (`MAIL_LINK_SIGNING_KEY`) a una ruta publica del gateway que sirve la descarga con caducidad y tope
   de descargas; el remitente lo revoca cuando quiere.
6. **Invitaciones**: el evento guarda `ORGANIZER`/`ATTENDEE`; al guardar con invitados, el webmail envia
   por el mismo camino que un correo normal un mensaje con `text/calendar; method=REQUEST` (y `CANCEL` al
   borrar). Un correo entrante con `text/calendar` se muestra con Aceptar, Tentativo y Rechazar, que
   actualizan el calendario del buzon y envian `METHOD:REPLY`. `mail-dav` sigue sin guardar `METHOD`: se
   quita al almacenar.
7. **Disponibilidad**: `mail-dav` expone solo intervalos ocupados (sin titulo ni lugar) de buzones de la
   misma empresa, por una funcion `SECURITY DEFINER` que devuelve inicio y fin y nada mas.
8. **Pagina de citas**: ruta publica sin sesion en el gateway, con limite por IP, trampa para robots,
   tope de reservas por dia y confirmacion por correo al visitante. La cita se crea en el calendario del
   dueno y se invita a los dos (G10).
9. **Asistente**: proveedor Anthropic (API de Claude), clave solo en el almacen de secretos
   (`ANTHROPIC_API_KEY`), apagado por defecto y activable por empresa por su `tenant_admin`. Sin clave o
   sin activar, la interfaz no lo muestra. Solo se envia el texto que el usuario pide procesar, nunca
   adjuntos; no se guarda nada del contenido. ADR nuevo en `docs/adr/` con el tratamiento de datos.
10. **PWA** sin cache de datos: el service worker solo guarda la carcasa estatica; nunca respuestas del
    API ni correo.

## 4. Propiedad de ficheros y numeracion (para trabajar en paralelo)

| Bloque | Funciones | Migraciones que crea | Prefijos de API |
|---|---|---|---|
| C1 | G1, G2, G3, G4, G14, G15 | ninguna | webmail: `/threads`, `/folders/{f}/messages?view=`, `/unsubscribe`, `/sender-insight` |
| C2 | G5, G6, G7 | celda `mail-directory/14_webmail_reminders.sql` | webmail: `/snooze`, `/follow-ups`, `/quick-replies`; interno `/internal/mail-directory/{reminders,quick-replies}` |
| C3 | G9, G10, G11, G12 | empresa `mail-dav/05_scheduling.sql` | webmail: `/calendar/...`, `/invitations`, `/availability`, `/booking`; publico `/api/v1/public/booking/*` |
| C4 | G8 | empresa en el servicio que elija el bloque segun el molde (propuesta: `webmail` no tiene base; usar `templates`, que ya maneja el almacen de objetos, o un servicio nuevo con `make new-service` si el acoplamiento lo exige) | webmail: `/large-files`; publico `/api/v1/public/files/*` |
| C5 | G13 | celda `mail-directory/15_ai_settings.sql` o registro, segun donde viva el ajuste por empresa | webmail: `/assistant/*`; panel: ajuste por empresa |

Cada bloque solo toca lo suyo; los ficheros compartidos (`services/webmail/internal/adapters/http/handler.go`,
`web/src/i18n/es.ts`, `web/src/api/endpoints.ts`, `web/src/pages/webmail/MessageView.tsx`,
`services/gateway/routes.json`) admiten anadidos, nunca reescrituras de lo ajeno.

## 5. Pruebas

Unitarias e integracion en cada bloque; `ops/e2e/mail.sh` gana una seccion por bloque (la escribe B6 al
integrar). `make checks`, `make check-web`, `make e2e-mail` y `make e2e` en verde antes de desplegar.

## 6. Despliegue

Orden: migraciones (celda, empresa) -> servicios de datos (`mail-directory`, `mail-dav`, el de C4) ->
`webmail` -> `gateway` (rutas publicas) -> `web`. El asistente necesita `ANTHROPIC_API_KEY` en el almacen;
sin ella se despliega apagado.

## 7. Riesgos

- Baja en un clic: una peticion saliente a una URL de terceros. Mitigado con la proteccion SSRF y
  solo por accion explicita del usuario.
- Pagina publica de citas: abuso. Limite por IP, tope diario y confirmacion por correo.
- Asistente: el contenido sale a un proveedor externo. Apagado por defecto, activacion explicita por la
  empresa, ADR y aviso en la interfaz.

## 8. Bloques de trabajo

C1-C5 en paralelo, cada uno en su worktree; B6 (integracion, e2e, documentos y despliegue) los une.

## 9. Mejoras futuras

Bandeja compartida (asignar un hilo a un companero, estados y notas internas) con ACL de IMAP,
calendarios compartidos editables, cifrado de extremo a extremo, integracion con el CRM de marketing en
la ficha del remitente.

## 10. Registro

| Bloque | Estado |
|---|---|
| C1 | en curso |
| C2 | en curso |
| C3 | en curso |
| C4 | en curso |
| C5 | en curso |
| B6 | pendiente |
