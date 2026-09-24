# Plan: webmail competitivo

Estado: en ejecucion (2026-09-24). Decision del responsable del producto: implementar todo lo de este
plan, sin fases de aprobacion intermedias, y desplegar al terminar cada bloque verificado.

## 1. Objetivo

El webmail (`services/webmail` y `web/src/pages/webmail`) cubre hoy leer, redactar en texto plano,
mover y borrar de uno en uno, borradores manuales, respuesta automatica, libreta de la empresa y avisos
en tiempo real. Frente a los webmails del mercado (Hostinger Mail, Zoho Mail, Gmail, Outlook) le faltan
la agenda personal, las carpetas propias, las acciones sobre varios mensajes, la firma, el envio
programado, las reglas y el reenvio, el calendario y la edicion con formato. Este plan cierra esa
distancia sin cambiar la arquitectura: el webmail sigue sin base de datos propia, los datos de cada
buzon viven en el servicio que ya los posee (`mail-directory` en la celda, `mail-dav` en la empresa) y
los motores se configuran, no se parchean.

## 2. Alcance

| # | Funcion | Donde vive el dato | Piezas |
|---|---|---|---|
| F1 | Carpetas propias: crear, renombrar, borrar | IMAP (Dovecot) | webmail, web |
| F2 | Seleccion multiple: marcar, mover, borrar, spam | IMAP | webmail, web |
| F3 | Marcar como spam / no es spam (entrena Rspamd) | IMAP + imapsieve existente | webmail, web |
| F4 | Vaciar Papelera y Spam | IMAP | webmail, web |
| F5 | Ver original (.eml) e imprimir | IMAP | webmail, web |
| F6 | Busqueda avanzada (de, para, asunto, fechas, no leidos, destacados, adjuntos) | IMAP SEARCH | webmail, web |
| F7 | Firma del buzon | celda: `mail.mailbox_signatures` | mail-directory, webmail, web |
| F8 | Reglas de correo y reenvio | celda: `mail.mailbox_filters` + Sieve | mail-directory, Dovecot, webmail, web |
| F9 | Cambiar mi contrasena | celda: `mail.mailboxes` | mail-directory, webmail, web |
| F10 | Envio programado | IMAP (mensaje) + celda: `mail.scheduled_sends` (indice durable) | mail-directory, webmail, web |
| F11 | Deshacer envio | interfaz (retardo antes de enviar) | web |
| F12 | Contactos personales (lista, ficha, busqueda, importar y exportar vCard, "anadir remitente") | empresa: `mail_dav` (CardDAV) | mail-dav, mail-auth, webmail, web |
| F13 | Calendario (mes, semana, agenda; eventos con repeticion simple) | empresa: `mail_dav` (CalDAV) | mail-dav, webmail, web |
| F14 | Redaccion con formato (negrita, cursiva, listas, enlaces, citas) | interfaz; el HTML lo sanea el servicio | web |
| F15 | Borrador automatico mientras se escribe | IMAP (Borradores) | web |
| F16 | Atajos de teclado | interfaz | web |
| F17 | Notificaciones de escritorio de correo nuevo | interfaz (avisos SSE existentes) | web |
| F18 | Autocompletar destinatarios con la agenda personal y la de la empresa | mail-dav + mail-directory | web |

Fuera de alcance (anotado como mejora futura en la seccion 9): vista por conversaciones, posponer
mensajes, acuses de lectura, invitaciones de calendario (iTIP), calendarios compartidos, segundo factor
del buzon.

## 3. Decisiones

1. **El webmail sigue sin base de datos.** Lo durable va al servicio dueno del dato por su API interna
   (`X-Gateway-Token`, rutas `/internal/<svc>/...`, que el gateway nunca expone). El usuario sale
   siempre de la sesion del webmail, nunca del cuerpo de la peticion.
2. **Envio programado: el mensaje en IMAP, el indice en la celda.** Redis no sirve: en produccion corre
   con `allkeys-lru` y puede descartar claves. El mensaje ya compuesto se guarda en la carpeta
   `Scheduled` del buzon (el usuario lo ve, cuenta en su cuota y puede cancelarlo) y
   `mail-directory` guarda la fila durable (`mail.scheduled_sends`) con la hora y la referencia IMAP.
   Un trabajador del webmail reclama las filas vencidas (`FOR UPDATE SKIP LOCKED` con arriendo), envia
   por el mismo camino que un envio normal (usuario maestro, `smtpd_sender_login_maps` del buzon real,
   copia en Enviados) y cierra la fila. Si el mensaje ya no esta en `Scheduled`, la fila se cancela.
3. **Reglas y reenvio por Sieve generado**, como la respuesta automatica: la regla se guarda
   estructurada, `mail-directory` genera el script (los textos del usuario solo entran como cadenas
   Sieve citadas) y Dovecot lo lee por `mail.v_sieve_user` en una ranura nueva `sieve_before3`, antes
   del archivado de spam global. El script entero va dentro de `if not header :contains "X-Spam-Flag"
   "YES"`: una regla nunca reenvia ni archiva spam. El reenvio usa `redirect` (con `:copy` si se
   conserva copia); Dovecot ya reenvia con el remitente del buzon (`sieve_redirect_envelope_from =
   recipient`), asi que SPF pasa en el destino sin SRS.
4. **Spam y no spam es mover a y desde `Junk`.** Dovecot ya tiene `imapsieve` con
   `report-spam.sieve` y `report-ham.sieve` hacia Rspamd. Se verifica en el e2e que dispara tambien con
   el usuario maestro.
5. **Contactos y calendario: API JSON interna en `mail-dav`**, sobre los mismos casos de uso de
   CardDAV y CalDAV (lo que se crea en la web aparece en el movil y al reves). El webmail necesita la
   empresa y el buzon: `mail-auth` los devuelve ya para `dav` y pasa a devolverlos tambien para
   `webmail`; el webmail los guarda en su sesion. Una sesion anterior sin ellos responde
   `SESSION_EXPIRED` en esas rutas y el usuario vuelve a entrar.
6. **Contrasena: el webmail comprueba la actual contra `mail-auth`** antes de pedir el cambio a
   `mail-directory`. El evento `mail.mailbox.credentials_changed` revoca todas las sesiones del buzon,
   tambien la que hizo el cambio: la interfaz lleva al inicio de sesion con un aviso.
7. **Deshacer envio es un retardo en la interfaz** (10 s por defecto): hasta que vence no sale ninguna
   peticion. Cerrar la pestana durante ese plazo cancela el envio y la interfaz lo avisa con
   `beforeunload`.
8. **Redaccion con formato sin dependencias nuevas**: editor `contenteditable` con una barra minima.
   El HTML resultante lo sanea el servicio al enviar (`HTMLSanitizer.Outgoing`, bluemonday), que
   tambien genera la parte de texto.

## 4. Contratos

Todas las respuestas usan el envelope de `pkg/response`. Los topes los sirve el servicio (`limits` en
la respuesta o en `/meta`); la interfaz no los copia.

### 4.1 webmail, publico (`/api/v1/webmail`, sesion del buzon, `Origin` en toda escritura)

Carpetas y mensajes (IMAP):

| Metodo y ruta | Cuerpo | Respuesta |
|---|---|---|
| `POST /folders` | `{"name":"Clientes/2026"}` | 201 `Folder` |
| `PATCH /folders/{folder}` | `{"name":"Nuevo"}` | 200 `Folder` |
| `DELETE /folders/{folder}` | - | 204 |
| `POST /folders/{folder}/empty` | - | 200 `{"removed":N}` |
| `POST /folders/{folder}/messages/batch` | `{"uids":[1,2],"action":"flags","add":[],"remove":[]}` o `{"uids":[...],"action":"move","to":"X"}` o `{"uids":[...],"action":"delete"}` | 200 `{"affected":N,"permanent":bool}` |
| `GET /folders/{folder}/messages/{uid}/raw` | - | `message/rfc822`, `Content-Disposition: attachment` |
| `GET /folders/{folder}/messages` | query nueva: `from`, `to`, `subject`, `since`, `before` (`YYYY-MM-DD`), `unread`, `flagged`, `has_attachments` (`true`) | igual que hoy |

Reglas: `INBOX` y las carpetas con papel (`sent`, `drafts`, `trash`, `junk`, `archive`, `scheduled`) no
se renombran ni se borran (409 `FOLDER_PROTECTED`); una carpeta con subcarpetas no se borra (409
`FOLDER_HAS_CHILDREN`); el nombre nuevo pasa `ValidateFolderName` y no puede existir (409
`FOLDER_EXISTS`). `empty` solo en `trash` y `junk` (409 `FOLDER_NOT_EMPTIABLE`). `batch` admite hasta
`max_batch_uids` (500) UIDs; `delete` mueve a Papelera o, desde Papelera, borra para siempre.
`/meta` suma `limits.max_batch_uids`, `limits.max_scheduled_days` y `folder_roles` incluye `scheduled`.

Envio programado:

| Metodo y ruta | Cuerpo | Respuesta |
|---|---|---|
| `POST /send` | el formulario de hoy mas `send_at` (RFC 3339, al menos 1 minuto en el futuro, como mucho `max_scheduled_days`) | 202 `{"scheduled":{"id","send_at"}}` en lugar del resultado de envio |
| `GET /scheduled` | - | `[{"id","send_at","subject","recipients":["a@b"],"created_at","status"}]` |
| `PATCH /scheduled/{id}` | `{"send_at":"..."}` | 200 fila |
| `DELETE /scheduled/{id}` | - | 204; el mensaje vuelve a Borradores |

Ajustes del buzon:

| Metodo y ruta | Cuerpo | Respuesta |
|---|---|---|
| `GET`/`PUT /signature` | `{"enabled":bool,"html":"...","on_replies":bool}` | `{..., "text":"...", "limits":{"max_html_bytes"}}` |
| `GET`/`PUT /filters` | `{"rules":[Rule],"forwarding":{"enabled":bool,"addresses":["x@y"],"keep_copy":bool}}` | lo mismo mas `limits` |
| `POST /password` | `{"current_password":"...","new_password":"..."}` | 204 (la sesion queda revocada) |

`Rule`: `{"id":"uuid","name":"...","enabled":bool,"match":"all"|"any","conditions":[{"field":"from"|"to"|"cc"|"recipient"|"subject","op":"contains"|"not_contains"|"is","value":"..."}],"actions":[{"type":"move","folder":"X"}|{"type":"mark_read"}|{"type":"flag"}|{"type":"forward","address":"x@y","keep_copy":bool}|{"type":"discard"}],"stop":bool}`.
Topes (los sirve `mail-directory`): 50 reglas, 10 condiciones y 5 acciones por regla, 5 direcciones de
reenvio, 256 caracteres por valor. Errores de validacion: 422 `VALIDATION_ERROR` con
`details.field` (`rules[3].conditions[0].value`, `forwarding.addresses[1]`...).

Contactos y calendario (proxy a `mail-dav`):

| Metodo y ruta | Cuerpo | Respuesta |
|---|---|---|
| `GET /contacts?q=&page=&per_page=` | - | `[Contact]` + `meta` de paginacion |
| `GET /contacts/{id}` | - | `Contact` |
| `POST /contacts` | `ContactInput` | 201 `Contact` |
| `PUT /contacts/{id}` | `ContactInput` + cabecera `If-Match: <etag>` opcional | 200 `Contact` (412 `PRECONDITION_FAILED`) |
| `DELETE /contacts/{id}` | - | 204 |
| `GET /contacts/export` | - | `text/vcard`, todos |
| `POST /contacts/import` | multipart `file` (.vcf, varias tarjetas) | 200 `{"imported":N,"updated":N,"skipped":[{"index","reason"}]}` |
| `GET /calendar/events?start=&end=` | RFC 3339, ventana de hasta 62 dias | `[Occurrence]` |
| `GET /calendar/events/{id}` | - | `Event` |
| `POST /calendar/events` | `EventInput` | 201 `Event` |
| `PUT /calendar/events/{id}` | `EventInput` + `If-Match` opcional | 200 `Event` |
| `DELETE /calendar/events/{id}` | - | 204 (la serie entera) |

`Contact`: `{"id","etag","name","given_name","family_name","emails":[{"value","type"}],"phones":[{"value","type"}],"organization","title","notes","birthday","updated_at"}`.
`ContactInput`: lo mismo sin `id`, `etag` ni `updated_at`. `type` en `home|work|mobile|other`.
`Event`: `{"id","etag","title","start","end","all_day","location","description","recurrence":{"freq":"daily"|"weekly"|"monthly"|"yearly","interval":1,"count":null,"until":null,"by_day":["MO"]}|null,"reminder_minutes":null|N}`.
`Occurrence`: `{"id","start","end","all_day","title","location","recurring":bool}`.
El `id` es el nombre del recurso sin extension (`[A-Za-z0-9._@=+~-]`); `mail-dav` lo genera como UUID al
crear. Un evento con reglas que `mail-dav` no sabe expandir con exactitud aparece en su primera
ocurrencia con `recurring: true`. Editar una ocurrencia suelta de una serie no esta en el alcance: se
edita la serie.

### 4.2 mail-directory, interno (celda; `?username=` sale de la sesion del webmail)

| Metodo y ruta | Uso |
|---|---|
| `GET`/`PUT /internal/mail-directory/signature?username=` | F7; guarda `html` saneado por el webmail y `text` |
| `GET`/`PUT /internal/mail-directory/filters?username=` | F8; valida, genera el Sieve y lo guarda |
| `PUT /internal/mail-directory/password?username=` | F9; `{"password":"..."}`, misma politica y evento que el cambio del administrador |
| `POST /internal/mail-directory/scheduled-sends` | F10; `{"username","message_id","folder","uid_validity","uid","send_at","subject","recipients"}` -> `{"id"}` |
| `GET /internal/mail-directory/scheduled-sends?username=` | F10; las pendientes del buzon |
| `PATCH`/`DELETE /internal/mail-directory/scheduled-sends/{id}?username=` | F10; cambiar la hora o cancelar |
| `POST /internal/mail-directory/scheduled-sends/claim` | F10; `{"limit":N,"lease_seconds":S}` -> filas vencidas de toda la celda, que pasan a `sending` con arriendo |
| `POST /internal/mail-directory/scheduled-sends/{id}/finish` | F10; `{"status":"sent"|"failed"|"canceled","error":"..."}`; `failed` con reintento reprograma con espera creciente hasta 5 intentos |

Migracion `migrations/cell/canonical/mail-directory/13_webmail_settings.sql`: `mail.mailbox_signatures`,
`mail.mailbox_filters` (reglas `jsonb`, reenvio `jsonb`, `script_data`), vista
`mail.v_sieve_user` (misma forma que `v_sieve_vacation`, `SELECT` solo para `mail_engine`),
`mail.scheduled_sends`. RLS como `09_vacation.sql`. Al borrar un buzon se borran sus filas.

### 4.3 mail-dav, interno (empresa; identidad por cabeceras que solo pone el webmail)

Mux nuevo delante del manejador DAV: `/internal/mail-dav/...` con `RequireGatewayToken` (ya global) y
`RequireInternalCaller`; cabeceras obligatorias `X-Mailbox-Tenant-ID` y `X-Mailbox-ID` (UUID). Mismas
rutas y cuerpos que 4.1 (`/internal/mail-dav/contacts...` y `/internal/mail-dav/calendar/events...`)
sobre la libreta y el calendario por defecto. El dominio gana un traductor estructurado <-> vCard 4.0
(al actualizar conserva las propiedades que no conoce) y <-> iCalendar (un `VEVENT`), y
`Occurrences(rango, presupuesto)` exportado sobre `RRule.Each`.

### 4.4 mail-auth

La respuesta a `service: "webmail"` suma `tenant_id` y `mailbox_id`, como ya hace para `dav`. El
webmail los guarda en `domain.Session`.

### 4.5 Dovecot

`sieve_before3 = dict:proxy::sieve_user;name=active;bindir=/var/vmail/sieve_user_bindir`, diccionario
`sieve_user` sobre `mail.v_sieve_user` generado en `docker-entrypoint.sh` como el de vacaciones, y la
carpeta `Scheduled` en `dovecot.folders.conf` (`auto = no`). `UPSTREAM.md` y el manifiesto lo anotan.

## 5. Bloques de trabajo

| Bloque | Contenido | Piezas que toca |
|---|---|---|
| B1 | F7, F8, F9, F10 (lado de datos) y Dovecot | `services/mail-directory`, `migrations/cell/canonical/mail-directory`, `deploy/mail/dovecot` |
| B2 | F12, F13 (lado de datos) y 4.4 | `services/mail-dav`, `services/mail-auth` |
| B3 | Todo el lado del webmail (F1-F10, F12, F13) | `services/webmail` |
| B4 | Toda la interfaz (F1-F18) | `web/` |
| B5 | Integracion, e2e, documentos, despliegue | `ops/e2e`, `docs/`, despliegue |

B1-B4 se construyen en paralelo contra los contratos de la seccion 4. B5 los une.

## 6. Pruebas

- Unitarias y de componente en cada bloque; integracion contra Postgres real para las tablas nuevas
  (`make test-integration`), incluida la reclamacion concurrente de envios programados.
- `ops/e2e/mail.sh` con Dovecot y Postfix reales: crear, renombrar y borrar carpeta; mover varios; spam
  que llega a Rspamd como aprendizaje; firma; regla que archiva en carpeta y regla de reenvio con copia;
  cambio de contrasena que revoca la sesion; envio programado que sale a su hora y cancelacion que
  vuelve a Borradores; contacto creado por la web que CardDAV devuelve; evento creado por la web que
  CalDAV devuelve.
- `make checks`, `make check-web`, `make e2e-mail` y `make e2e` en verde antes de desplegar.

## 7. Despliegue

Orden: migracion de celda (`13_webmail_settings.sql`) -> `mail-directory`, `mail-auth`, `mail-dav` ->
`webmail` -> motor `dovecot-mail` -> `web`. Sin secretos ni variables obligatorias nuevas; las
opcionales del webmail (`WEBMAIL_SCHEDULED_*`) tienen valor por defecto.

## 8. Riesgos

- Un script Sieve generado que Dovecot no compila deja sin filtrar ese buzon: el generador se prueba
  compilando con `sievec` en el e2e y los textos del usuario nunca entran como codigo.
- Reenvio hacia fuera: cuenta para la reputacion de salida de la plataforma. Se limita a 5 direcciones,
  nunca reenvia spam, y el reenvio queda auditado por el evento del cambio.
- El envio programado depende del usuario maestro, como el envio normal; si el buzon se desactiva antes
  de la hora, la fila termina `failed` sin reintento.

## 9. Mejoras futuras

Vista por conversaciones (IMAP `THREAD`), posponer mensajes, invitaciones de calendario (iTIP/iMIP),
calendarios y libretas compartidas, editar una ocurrencia suelta, segundo factor del buzon, politica
de la empresa para limitar el reenvio externo.

## 10. Registro

| Bloque | Estado |
|---|---|
| B1 | en curso |
| B2 | en curso |
| B3 | en curso |
| B4 | en curso |
| B5 | pendiente |
