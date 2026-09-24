# ADR 0004: contactos y calendario personales por CardDAV y CalDAV

## Estado

**Parcialmente implementado: CardDAV y CalDAV** (2026-09-21). El responsable del producto autorizo construirlo.
Hecho: el servicio `mail-dav` con CardDAV (contactos personales de cada buzon) y con CalDAV (calendarios y eventos
`VEVENT` de cada buzon), sincronizables con iOS, Thunderbird y DAVx5; su autenticacion contra `mail-auth`, el flag
`dav_access` del buzon, los datos de conexion en la ficha del buzon y las pruebas. **Pendiente**: comparticion entre
buzones, la libreta de solo lectura "Directorio de la empresa", limites como derechos del plan de `billing`, y de
CalDAV lo que dice "Pendiente" al final (tareas, planificacion por CalDAV, alarmas, `free-busy-query`, `PROPPATCH`). Las
invitaciones por correo, la disponibilidad del equipo, la zona horaria, las apariciones sueltas y la pagina de citas
estan hechas (2026-09-24, "Invitaciones, disponibilidad y citas"). El borrado
de los contactos y de los calendarios de un buzon borrado esta hecho (2026-09-21, ver "Borrado de los datos de un
buzon"). Cierra el diseno de `Plan_Estrategico_Mejoras_Correo.md`, C3 fase 2. La fase 1 (libreta compartida de la
empresa en el webmail) ya estaba hecha y no depende de esto.

**Revision adversaria de seguridad, robustez y escalabilidad (2026-09-21)**: hecha sobre el protocolo escrito a mano,
el dominio iCalendar, vCard y RRULE, el repositorio, la autenticacion contra `mail-auth` y el gateway; los hallazgos
y lo que cambio estan en "Endurecimiento tras la revision adversaria".

Lo verificado y lo que no: el codigo compila y pasa las pruebas unitarias, las de integracion contra Postgres real y
`make checks`; los cuerpos de peticion de DAVx5, iOS y Thunderbird de las pruebas estan escritos a mano segun las RFC
(no capturados de un cliente). **No se ha probado con un cliente real ni con `make e2e-mail`** (las secciones de
CardDAV y de CalDAV de `ops/e2e/mail.sh` estan escritas y sin ejecutar): el ADR original exige DAVx5, Thunderbird e
iOS antes de darlo por bueno.

## Contexto

mailcow entrega contactos y calendario con SOGo (SOGo, PHP, nginx y MySQL, que aqui no se copiaron). Sin ellos,
el correo de esta plataforma se usa con el calendario y los contactos de otro proveedor. Los clientes de
escritorio y movil hablan CardDAV (RFC 6352) y CalDAV (RFC 4791) sobre WebDAV (RFC 4918); ActiveSync queda
descartado en el plan (protocolo propietario y caro).

## Decision

Un servicio nuevo `mail-dav`, en Go, molde hexagonal, **plano de empresa** (los contactos y eventos son datos de la
empresa, van en `mail_tenant_<slug>`, con `tenant_id`, RLS y sin claves foraneas entre esquemas). Fases:

1. **CardDAV primero** (libretas y contactos vCard 3.0 y 4.0), porque es la mitad barata y valida el resto:
   autenticacion, ruta publica, sincronizacion por `ctag`, `etag` y `sync-collection` (RFC 6578), cliente real (iOS,
   Thunderbird, DAVx5). **Hecha** (sin cliente real, ver Estado).
2. **CalDAV despues** (calendarios y eventos iCalendar), con `sync-collection` (RFC 6578) y recurrencias sin expandir
   en el servidor: se guardan y se devuelven, el cliente las expande (la unica expansion es acotada y solo decide un
   `calendar-query` por rango, ver "Lo decidido al implementar (CalDAV)"). **Hecha** para eventos, sin cliente real
   (ver Estado); las tareas (`VTODO`) quedaron fuera.

El almacenamiento es propio (Postgres) y el protocolo lo sirve el propio servicio (ver "Libreria WebDAV").

### Autenticacion

Los clientes DAV usan HTTP Basic, no sesion ni JWT. Se autentican contra `mail-auth` con un servicio nuevo `dav`
(`ProtocolDAV`, con flag propio `dav_access` en el buzon y en la contrasena de aplicacion, como `sieve_access`),
que admite **contrasena de aplicacion** y la principal. `mail-dav` no valida credenciales: pregunta a `mail-auth`
y recibe el buzon y su empresa. Freno por buzon e IP, como el webmail. Cada peticion se atribuye a un buzon y solo
ve sus colecciones (las que se compartan con el son la fase de comparticion, pendiente).

El gateway sigue siendo la unica entrada: `mail-dav` se declara con un prefijo `self_authenticated` en
`services/gateway/routes.json`, igual que el webmail, y el gateway no valida JWT para el. Las rutas de
descubrimiento (`/.well-known/carddav` y `/.well-known/caldav`) responden con la redireccion al prefijo.

### Datos

`mail_dav.addressbooks`, `contacts` (vCard como texto validado y campos indexados: uid, nombre, correos),
`collection_changes` (para `sync-collection`); `calendars`, `events` (iCalendar como texto validado, uid, resumen,
inicio de la primera aparicion y fin de la ultima, `etag`) y `calendar_changes` (el mismo registro de cambios, para
calendarios); y, pendiente, `shares` (compartir con otro buzon de la empresa, solo lectura o escritura). Limites por
buzon (tamano de un objeto, numero de objetos y colecciones); como derechos del plan de `billing` queda pendiente.
Los objetos importados se validan y se acotan: un vCard o un iCalendar es entrada de un tercero.

### Encaje con lo que ya hay

* La libreta compartida de la fase 1 (directorio de buzones activos de la empresa) se ofrecera como una libreta
  de solo lectura "Directorio de la empresa" de cada buzon, sin duplicar datos: se genera desde `mail-directory`.
  Pendiente.
* El webmail usa los contactos y el calendario personales por una API JSON interna de `mail-dav` (ver "API interna
  para el webmail"), sobre los mismos almacenes que CardDAV y CalDAV: lo que se crea en la web aparece en el movil y
  al reves. La libreta de la empresa sigue siendo la de `mail-directory`.

### API interna para el webmail (2026-09-24)

`docs/Plan_Webmail_Competitivo.md` (4.3) la define; su contrato exacto esta alli y en
`services/mail-dav/internal/adapters/http/api.go`. Lo que decide este servicio:

* **Entrada**: `/internal/mail-dav/...` va por su propia cadena en `main.go` (token interno, `RequireInternalCaller`, cupo
  por buzon y no por IP, porque todo llega desde el webmail, y el mismo tope de peticiones simultaneas y plazo que DAV). El
  buzon lo nombran `X-Mailbox-Tenant-ID` y `X-Mailbox-ID` (UUID, obligatorias), que el webmail toma de la respuesta de
  `mail-auth` al abrir su sesion; se liga como una peticion DAV (`BindMailbox`, RLS por empresa y buzon).
* **Colecciones**: la libreta `contacts` y el calendario `calendar` por defecto (se crean si faltan); si el buzon los
  borro por DAV, la primera que tenga.
* **Traductores** (`internal/domain/contact_fields.go`, `event_fields.go`): vCard 4.0 e iCalendar con un `VEVENT`,
  textos escapados (RFC 6350 y 5545) y plegados a 75 octetos; todo lo generado vuelve a pasar `ParseVCard` y
  `ParseCalendarObject` (fuzz incluido). Al actualizar se conserva todo lo que la API no expresa (version y UID del vCard,
  `PHOTO`, `X-*`, grupos de etiquetas, otros `VALARM`, `VTIMEZONE`) y la linea original de cada valor que no cambio; desde
  2026-09-24 la API expresa tambien la zona, el organizador y los invitados (ver "Invitaciones, disponibilidad y citas"). Si cambia el inicio, el tipo de dia o la repeticion, se retiran `EXDATE`, `RDATE` y las sobrescrituras
  (nombran apariciones que ya no existen); si el inicio tenia una zona IANA, las horas nuevas se escriben en esa zona. Una
  regla que la API no expresa (`HOURLY`, `BYSETPOS`...) se lee como sin repeticion o simplificada y, si no se toca, se
  conserva tal cual.
* **Apariciones**: `CalendarObject.Occurrences` expande con `RRule.Each` y el mismo presupuesto por evento y por consulta
  que `calendar-query`, con `EXDATE`, `RDATE` y sobrescrituras; lo que no se sabe expandir aparece en su primera
  ocurrencia con `recurring: true`.
* **Topes nuevos** (`.env.example`, servidos en `/internal/mail-dav/meta`): `MAIL_DAV_MAX_IMPORT_BYTES` (4 MiB),
  `MAIL_DAV_MAX_IMPORT_CARDS` (1000), `MAIL_DAV_MAX_EVENT_WINDOW_DAYS` (62), `MAIL_DAV_MAX_OCCURRENCES` (5000),
  `MAIL_DAV_CONTACTS_PAGE_SIZE` (50) y `MAIL_DAV_MAX_CONTACTS_PAGE_SIZE` (100).

### Invitaciones, disponibilidad y citas (2026-09-24)

`docs/Plan_Webmail_Innovador.md`, bloque C3 (G9 a G12, decisiones 6 a 8). Migracion de empresa
`migrations/tenant/canonical/mail-dav/05_scheduling.sql`.

* **Zona horaria (G9).** `EventInput.timezone` (IANA). Con zona, `DTSTART`/`DTEND` se escriben con `TZID` y el objeto
  lleva un `VTIMEZONE` generado de la base embebida (`time/tzdata`, `vtimezone.go`): una observancia `STANDARD` o
  `DAYLIGHT` por clase de transicion con su regla anual (`FREQ=YEARLY;BYMONTH;BYDAY`) si una regla describe las de los
  doce anios alrededor del evento, o sus fechas enumeradas (`RDATE`) si no. Probado que, leido con el evaluador propio de
  `VTIMEZONE` (como lo leeria un cliente que no conoce el nombre), da los mismos instantes que la zona real de 2026 a
  2031 en Madrid, Nueva York, Sidney, Lima, Santiago y Calcuta. La expansion sigue el reloj de pared, asi que una serie
  semanal de las 10:00 en Madrid es 08:00 UTC en verano y 09:00 en invierno (probado). Vacio en una actualizacion conserva
  la zona del evento; `UTC` la quita; cambiarla cuenta como cambiar el inicio (se retiran las excepciones). Un dia
  completo no lleva zona.
* **Una sola aparicion (G9).** `PUT` y `DELETE /calendar/events/{id}/occurrences/{rid}`, con `rid` el `recurrence_id` que
  ahora devuelve cada aparicion (su inicio original). Cambiar escribe (o reemplaza) la sobrescritura `RECURRENCE-ID` con
  las horas en la forma del `DTSTART` de la serie (`TZID`, UTC, fecha o flotante) y los invitados de la serie; borrar
  anade el `EXDATE` y retira la sobrescritura. Una `rid` que no es una aparicion viva es 404. Sube la `SEQUENCE`.
* **Reuniones (G10).** `EventInput.organizer` y `attendees` (`email`, `name`, `partstat`). El webmail pone siempre al buzon
  de la sesion como organizador si hay invitados. Al cambiar la hora el organizador pide respuesta de nuevo (`PARTSTAT`
  vuelve a `NEEDS-ACTION` con `RSVP`); un invitado que no cambio conserva su linea original. La copia de una reunion que
  organiza otro conserva su `ORGANIZER`.
* **iTIP (G10), `itip.go`.** `ParseInvitation` acota el iCalendar de un correo como un `PUT` (bytes, lineas,
  anidamiento, UTF-8) y exige un `METHOD` `REQUEST`, `REPLY` o `CANCEL`; `REQUEST` y `CANCEL` deben ser, sin `METHOD`, un
  objeto de calendario valido (`ParseCalendarObject`), y un `REPLY` solo necesita UID y el invitado que responde. Se
  guarda siempre sin `METHOD`. `BuildInvitation` escribe `REQUEST` (la serie y sus sobrescrituras, sin `VALARM`) o `CANCEL`
  (`STATUS:CANCELLED`, `SEQUENCE+1`). `RespondToInvitation` guarda la copia del invitado con su `PARTSTAT` y escribe el
  `REPLY`; aceptar o dejar en tentativo guarda (reemplaza si ya estaba con ese UID, salvo que la guardada sea mas
  reciente), rechazar la quita. `ApplyReply` solo cuenta si el remitente del correo es el propio invitado, si el evento
  lo organiza el buzon y si no es de una `SEQUENCE` anterior; `CANCEL` solo si lo envia el organizador del evento (con
  `RECURRENCE-ID`, borra esa aparicion). Ida y vuelta y fuzz (`FuzzParseInvitation`, 2,5 millones de entradas sin
  panico) probados.
* **Disponibilidad (G11).** Cada evento materializa al guardarse su ocupacion en `mail_dav.event_busy` (inicio y fin de
  sus apariciones que ocupan tiempo: sin `TRANSP:TRANSPARENT`, sin `STATUS:CANCELLED` y, un dia completo, solo con
  `TRANSP:OPAQUE`) desde `MAIL_DAV_BUSY_LOOKBACK_DAYS` atras hasta `MAIL_DAV_BUSY_HORIZON_DAYS` adelante, con
  `MAIL_DAV_MAX_BUSY_PER_EVENT` tramos como mucho; `events.busy_until` dice hasta donde (`NULL`: todas). Los eventos
  anteriores a la migracion quedan con `-infinity` (pendientes) y los completa, con la identidad de su propio buzon, la
  primera consulta que los necesita (hasta `MAIL_DAV_BUSY_REFRESH_MAX`; `partial` si quedan). La direccion de cada buzon
  (`mail_dav.mailbox_addresses`) la registra el propio buzon al usar el calendario (la de `mail-auth` en DAV, la de la
  sesion en el webmail por `X-Mailbox-Address`). Las politicas de fila dejan ver solo el buzon de la sesion; cruzar
  buzones lo hacen funciones `SECURITY DEFINER` (`search_path` fijo, empresa de la sesion o `42501`, topes de filas,
  solo `EXECUTE` para `mail_dav_service`): `resolve_busy_mailboxes` (direccion a buzon, hasta 50),
  `busy_intervals` (tres columnas: buzon, inicio y fin; ventana de 62 dias como mucho) y `busy_pending_events` (ids).
  Probado contra Postgres con el rol de servicio: otra empresa no aparece ni pidiendola por id, la sesion no puede
  preguntar por otra empresa ni sin sesion, la tabla no se ve fuera del buzon, y quitar el filtro de empresa de
  `busy_intervals` hace fallar la prueba (mutacion revertida).
* **Citas (G12).** `mail_dav.booking_pages` (una por buzon: titulo, duracion, franjas por dia, antelacion minima y
  maxima, margen, tope diario, zona, activa, y el dueno que pone el webmail con la sesion) y `mail_dav.bookings` (solo lo
  que piden los topes: pagina, evento, horas y SHA-256 del correo del visitante con el id de la pagina; lo de mas de dos
  dias se retira). El enlace es un identificador de 144 bits aleatorios que se puede regenerar. La pagina publica llega
  sin buzon: `booking_page_owner` (`SECURITY DEFINER`, solo paginas activas de la empresa de la sesion) da el dueno y
  desde ahi se trabaja con su identidad. Reservar comprueba que el hueco sea uno de la pagina y este libre, y guarda el
  evento (dueno organizador que ya acepto, visitante invitado, la nota solo en la descripcion del dueno) y la reserva en
  una transaccion con el cerrojo del buzon, con los topes diario (`daily_limit`, techo `MAIL_DAV_BOOKING_MAX_DAILY`) y por
  visitante (`MAIL_DAV_BOOKING_MAX_PER_VISITOR`); diez reservas simultaneas del mismo hueco dejan una (probado). La
  invitacion al visitante no lleva la descripcion.

**Contratos de la API interna** (`/internal/mail-dav`, token interno; con buzon salvo las de `booking/public`):

| Metodo y ruta | Entrada | Salida |
|---|---|---|
| `PUT /calendar/events/{id}/occurrences/{rid}` | evento (`title`, `start`, `end`, `all_day`, `location`, `description`), `If-Match` | evento |
| `DELETE /calendar/events/{id}/occurrences/{rid}` | `If-Match` | evento |
| `POST /calendar/events/{id}/itip` | `{method: REQUEST\|CANCEL}` | `{method, ical, recipients[], event}` |
| `POST /itip/inspect` | `{ical, addresses[]}` | invitacion con `event_id`, `attendee`, `partstat`, `is_organizer` |
| `POST /itip/respond` | `{ical, addresses[], response}` | `{reply, organizer, attendee, event_id}` |
| `POST /itip/apply` | `{ical, from, addresses[]}` | `{method, changed, event_id}` |
| `POST /availability` | `{addresses[], start, end}` | `[{address, known, partial, busy[{start, end}]}]` |
| `GET` / `PUT /booking` | configuracion (`weekly` como `{"MO": [{"start":"09:00","end":"13:00"}]}`), `owner_name`, `regenerate_link` | pagina con `public_id` y dueno |
| `GET /booking/public/{tenant}/{page}?start&end` | | pagina, `owner_address` (solo para el webmail) y `slots[]` |
| `POST /booking/public/{tenant}/{page}/reservations` | `{start, name, email, note}` | `{event_id, title, start, end, timezone, owner, invitation}` |

Los eventos ganan `timezone`, `organizer` y `attendees`; las apariciones, `recurrence_id`. Errores nuevos: 409
`SLOT_UNAVAILABLE` y 429 `LIMIT_EXCEEDED` (`limit: bookings`, con `Retry-After`). El cupo de `booking/public` es por
empresa, no por buzon.

**El webmail** envia desde el buzon, por su `Sender` (el submission de la celda, autenticado como el buzon), un correo
`multipart/mixed` con el texto y el iCalendar en `text/calendar; method=...` mas el mismo `.ics` adjunto: `REQUEST` al
crear o cambiar una reunion que organiza (y una aparicion), `CANCEL` al borrarla, `REPLY` al organizador al responder
(desde la direccion invitada, que debe ser un remitente del buzon). El texto del correo es del servidor
(`domain/invitation_mail.go`). Un fallo del envio no deshace el cambio del calendario: la respuesta lo dice
(`invitations.sent`). Rutas: `/calendar/events/{id}/occurrences/{rid}`, `/invitations/{folder}/{uid}` (`GET`,
`/respond`, `/apply`), `/availability?addresses=&start=&end=`, `/booking`, y `?notify=false` en los cambios del
calendario para no avisar. La pagina publica es `/api/v1/public/booking/{cell}/{tenant}/{page}` (`GET` huecos, `POST`
reservar), ruta publica del gateway hacia el webmail de la celda del enlace, con el cupo general y, la reserva, el
estricto por IP (`"limit": "strict"` en `routes.json`); el webmail rechaza una celda que no es la suya, comprueba que el
dueno sea un buzon de su celda (sus remitentes) antes de reservar, ignora sin reservar lo que rellena la trampa para
robots y envia la invitacion al visitante con copia al dueno desde el buzon del dueno, que es la confirmacion.

**Configuracion nueva** (`.env.example`; todo arranca con sus valores): `MAIL_DAV_BUSY_HORIZON_DAYS` (400, `0` apaga la
planificacion y sus rutas responden 503), `MAIL_DAV_BUSY_LOOKBACK_DAYS` (7), `MAIL_DAV_MAX_BUSY_PER_EVENT` (1000),
`MAIL_DAV_BUSY_REFRESH_MAX` (500), `MAIL_DAV_AVAILABILITY_MAX_ADDRESSES` (20, hasta 50), `MAIL_DAV_BOOKING_MAX_DAILY` (50),
`MAIL_DAV_BOOKING_MAX_PER_VISITOR` (2), `MAIL_DAV_BOOKING_MAX_SLOTS` (300) y `MAIL_DAV_BOOKING_MAX_WINDOW_DAYS` (31).

**Limites conocidos.** Un `REPLY` o un `CANCEL` falsificado con el `From` del invitado o del organizador pasaria la
comprobacion de remitente: el escudo antifraude del lector (SPF, DKIM y DMARC) es lo que lo delata. Una serie sin fin se
materializa hasta el horizonte y se completa cuando una consulta lo pasa. Un buzon que nunca uso el calendario aparece
como "sin datos" (`known: false`). Las invitaciones no se guardan en Enviados.

## Lo decidido al implementar (CardDAV)

### Libreria WebDAV: no se usa `emersion/go-webdav`, el subconjunto se escribe a mano

El ADR original preveia `go-webdav` (MIT). Se evaluo leyendo su codigo (v0.7.0, octubre de 2025, la ultima; licencia
MIT, copyright de Simon Ser; depende de `go-vcard` y `go-ical`) y no encaja por lo que hace falta aqui:

* **Su servidor CardDAV no implementa `sync-collection` ni `getctag`**: solo el cliente los usa. Sin ellos DAVx5 e iOS
  vuelven a listar toda la libreta en cada sincronizacion. Anadirlos por fuera obliga a interceptar el REPORT y a
  parsear XML de todos modos, con dos manejadores de protocolo en uno.
* **Sin acotar la entrada**: no limita el cuerpo de una peticion, ni el tamano ni las propiedades de un vCard, ni el
  numero de `href` de un multiget. Aqui todo eso es entrada de un tercero y debe acotarse donde entra.
* **API todavia inestable** (v0.x, un solo mantenedor, sin publicacion en un ano): una dependencia de protocolo que se
  reescribe entre versiones es deuda en el servicio con mas superficie publica no autenticada por JWT.
* Lo que aporta (PROPFIND, multiget, query) es el subconjunto que se necesita de todos modos y es pequeno.

Se implementa el subconjunto que usan los clientes: `OPTIONS`, `PROPFIND` (profundidad 0 y 1; `infinity` se rechaza con
403 `propfind-finite-depth`), `REPORT` (`addressbook-query`, `addressbook-multiget`, `sync-collection`), `GET` y `HEAD`,
`PUT` con `If-Match` e `If-None-Match`, `DELETE` y `MKCOL` de libreta (simple y extendido, RFC 5689). Sin dependencias
nuevas. No se implementan `PROPPATCH`, `COPY`, `MOVE`, `LOCK` (405), la recuperacion parcial de `address-data`
(siempre se devuelve el vCard entero) ni `param-filter` (se rechaza con `supported-filter`, no se ignora). El XML se
escribe con `encoding/xml` (todo texto sale escapado) y la respuesta se serializa entera antes de escribir el 207.

### Seguridad de la entrada

* Cuerpo maximo por peticion: `MAIL_DAV_MAX_REQUEST_BYTES` para XML y `MAIL_DAV_MAX_VCARD_BYTES` para un PUT
  (`http.MaxBytesReader`: se corta al llegar al tope sin leerlo entero); `href` de un multiget acotados al maximo de
  contactos.
* vCard validado y acotado (`domain.ParseVCard`): UTF-8, sin caracteres de control ni CR suelto, una sola tarjeta sin
  anidar, VERSION 3.0 o 4.0, UID obligatorio y unico por libreta, nombres de propiedad validos, numero de
  propiedades acotado. Se guarda **tal cual** y su etag es el SHA-256 de esos bytes; ni se normaliza ni se convierte.
* Sin XXE: `encoding/xml` no resuelve entidades externas ni de DTD (una entidad desconocida es un error 400) y los
  cuerpos se decodifican en estructuras sin recursion. Probado con entidades externas, "billion laughs" y anidamiento.
* Sin traversal: el path se separa por segmentos y cada uno se decodifica; se rechazan `.`, `..`, `%2F`, `\`, NUL y
  controles, y nombres de recurso (`[A-Za-z0-9._@=+~-]`, terminados en `.vcf`) y de libreta (`[a-z0-9-]`) por
  lista blanca. El usuario de la URL debe ser el del buzon autenticado: cualquier otro es 404.
* Ninguna contrasena en registros (probado con un observador de `zap`); `Cache-Control: no-store`.
* Lo que la peticion puede pedir tambien esta acotado (ver "Endurecimiento tras la revision adversaria"): un prop de
  mas de 128 propiedades es 400 y las repetidas se responden una vez, un href repetido de un multiget se responde una
  vez, un listado que no pide `address-data` ni `calendar-data` no lee los objetos, lo que una respuesta lleva de los
  objetos tiene tope (`MAIL_DAV_MAX_RESPONSE_BYTES`, 507 `number-of-matches-within-limits`) y el espacio de un buzon
  tambien (`MAIL_DAV_MAX_MAILBOX_BYTES`, 507 `quota-not-exceeded`).

### Aislamiento

Tres capas, la ultima independiente del codigo: (1) la empresa sale de la identidad que devuelve `mail-auth`, nunca de
la peticion, y elige la base; (2) cada consulta filtra por `tenant_id` y `mailbox_id`; (3) politicas RLS en las tres
tablas (`migrations/tenant/canonical/mail-dav/02_service_role.sql`) comparan esas columnas con la empresa y el buzon de
la sesion (`app.current_tenant_id` y `app.current_user_id`, que en este servicio lleva el id **del buzon**, no de un
usuario de la plataforma). Aplican al rol de login `mail_svc_mail_dav`, que no es dueno de las tablas (solo DML). Una
clave foranea compuesta impide que un contacto apunte a la libreta de otro buzon. Probado contra Postgres real con ese
rol: buzon de otra empresa, buzon de la misma empresa, consulta sin filtro, escritura como otro buzon, sesion vacia
(fail-closed), y una mutacion (RLS apagada en una transaccion) que demuestra que la prueba detectaria una politica
ausente. Sin credencial propia (desarrollo) el servicio corre como dueno y rigen solo los filtros de las consultas.

### Conciliacion de buzones borrados (V, 2026-09-21)

El consumidor de `mail.mailbox.deleted` solo ve los eventos que el stream `MAIL_DIRECTORY` aun conserva, y una peticion
en vuelo puede escribir despues del evento. Un barrido periodico (`pkg/mailreconcile`, el mismo de `mail-migration`;
diseno, contrato y garantias en `docs/adr/0002`, "Conciliacion de buzones borrados") compara los `mailbox_id` que
`mail_dav` guarda con los buzones que existen en `mail-directory` (`POST /internal/mail-directory/mailboxes/existence`,
acotado por empresa) y retira los de los que no existen con el mismo caso de uso que el consumidor (`PurgeMailbox`: por id
de buzon, por empresa, idempotente, con la identidad acotada de una peticion).

* **Enumeracion.** Las politicas de fila por buzon impiden a cualquier sesion del servicio listar de que buzones hay
  datos, asi que la migracion `04_reconcile.sql` define `mail_dav.stale_mailbox_ids(empresa, antes_de, despues_de,
  limite)`: `SECURITY DEFINER` con `search_path` fijo, solo `EXECUTE` para `mail_dav_service`, solo lee, filtra por empresa,
  devuelve unicamente ids de buzon con libretas o calendarios cuyo elemento mas antiguo es anterior a `antes_de` (la gracia)
  y pagina en orden con un tope de 1000. No expone contenido.
* **Ventana de gracia.** `MAIL_DAV_RECONCILE_GRACE` (24 h, de 10 min a 30 dias): un buzon con datos mas recientes que la
  gracia no se toca, ni aunque el directorio no lo conozca todavia. Un dato en vuelo que llega despues del evento no
  impide que el buzon sea candidato (cuenta el mas antiguo) y se retira con el resto.
* **Ejecucion y configuracion.** Cerrojo de lider (`db.TryLeaderLock`, clave propia) y primera pasada un minuto despues de
  arrancar; `MAIL_DAV_RECONCILE_INTERVAL` (6 h, `0` la desactiva) y `MAIL_DAV_RECONCILE_MAX_PURGES_PER_TENANT` (100 buzones
  por empresa y pasada). Necesita `MAIL_DIRECTORY_URL` (y `ORGANIZATION_URL` con varias celdas), la misma configuracion de
  celdas que el resto de servicios; un valor invalido impide arrancar y `MAIL_DAV_RECONCILE_INTERVAL=0` la apaga sin mas
  configuracion. Metricas `mailbox_reconcile_*` y registro solo con ids.
* Probado: unitarias del caso de uso, de `pkg/mailreconcile` y del cliente; integracion contra Postgres real con el rol de
  servicio (la enumeracion solo devuelve buzones de la empresa pedida y mas viejos que la gracia, por libretas y por
  calendarios, y pagina; el barrido retira el buzon borrado con su calendario y respeta al vivo, al recreado con el mismo
  nombre, al recien creado y a otra empresa). Mutaciones comprobadas: quitar la comparacion con la gracia o el filtro de
  empresa de la funcion hace fallar esas pruebas.

### Borrado de los datos de un buzon

Al borrar un buzon, `mail-directory` publica `mail.mailbox.deleted` (payload `tenant_id`, `id`, `username`, ...) por su
outbox, y `mail-dav` lo consume (`internal/adapters/nats`, durable `mail-dav-mailbox-deleted` sobre el stream
`MAIL_DIRECTORY`, que el propio consumidor declara con `EnsureStream`) y borra las libretas y los calendarios del buzon; los
contactos, los eventos y los registros de cambios caen por la clave foranea `ON DELETE CASCADE`.

* **Por id, no por nombre.** El borrado usa el `id` del buzon del evento. Un buzon recreado con el mismo nombre tiene
  otro id y conserva sus contactos; un evento sin `id` valido no borra nada. Tampoco se cruzan empresas: la empresa
  sale del `tenant_id` del evento (y debe coincidir con la del sobre), elige la base y es la del borrado, que ademas
  corre con la misma identidad acotada (empresa y buzon en la sesion) que una peticion, de modo que las politicas
  RLS valen tambien aqui.
* **Idempotente y sin orden.** Borrar lo que no existe no es un error: un evento repetido, o el de un buzon que
  nunca guardo contactos, deja las cosas igual. No hay evento de "creacion" que esperar: las libretas nacen del
  primer acceso del buzon.
* **Fallos.** Sin ack un evento se reentrega y, agotadas sus entregas, va a `EVENTS_DLQ` (alerta del grupo eventos).
  Un evento sin `tenant_id` e `id` coherentes tampoco se confirma, para que quede a la vista en la DLQ en lugar de
  perderse; una empresa que ya no figura en el registro se confirma (sus datos se fueron con ella).
* **Sin NATS** el servicio arranca y sirve igual, y reintenta la suscripcion; el durable recoge lo pendiente en
  cuanto suscribe. No hay configuracion nueva.
* Ventana conocida: una peticion DAV que ya autentico cuando se borra el buzon y escribe despues de procesado el
  evento (milisegundos frente a la latencia de la outbox y de NATS) dejaria una libreta o un calendario huerfanos, y lo
  borrado antes de que existiera el consumidor o mas alla de la retencion del stream no llega nunca. Lo retira el
  barrido de conciliacion (seccion "Conciliacion de buzones borrados", justo antes).
* Probado: unitarias del caso de uso y del consumidor (evento repetido, buzon inexistente, otro buzon y otra empresa
  intactos, buzon recreado con el mismo nombre intacto, identificadores incoherentes), integracion contra Postgres
  real (con el rol de servicio y como dueno de las tablas, para que los filtros se prueben sin la politica) y una
  mutacion por filtro (sin `tenant_id`, sin `mailbox_id`) que hace fallar la prueba.

### Sincronizacion

`ctag` y `sync-token` salen de `addressbooks.sync_seq`, que sube en cada cambio (con la libreta bloqueada, sin huecos ni
repetidos). El token es `urn:mail-dav:sync:<id de la libreta>:<secuencia>`: ligado a la libreta, de modo que una libreta
borrada y recreada con el mismo nombre no acepta el token de la anterior. `collection_changes` se poda a los ultimos
`MAIL_DAV_CHANGES_RETAINED` cambios y `changes_floor` marca el limite: un token anterior, posterior o ajeno responde 403
`valid-sync-token` y el cliente vuelve a listar. Reenviar el mismo contenido no es un cambio.

### Rutas y descubrimiento

`/api/v1/dav/` (`MAIL_DAV_BASE_PATH`, que debe ser el prefijo de `routes.json`; una prueba lo cruza),
`principals/<buzon>/`, `addressbooks/<buzon>/`, `addressbooks/<buzon>/<libreta>/<recurso>.vcf`, `calendars/<buzon>/` y
`calendars/<buzon>/<calendario>/<recurso>.ics`. El principal anuncia `addressbook-home-set` y `calendar-home-set`. La libreta por
defecto (`contacts`, nombre `MAIL_DAV_DEFAULT_ADDRESSBOOK_NAME`) se crea al listar el home de un buzon sin libretas
(si borra todas, reaparece al siguiente descubrimiento); el calendario por defecto (`calendar`, nombre
`MAIL_DAV_DEFAULT_CALENDAR_NAME`) igual con el home de calendarios. El gateway declara en `routes.json` el prefijo
(`self_authenticated`, con los metodos WebDAV que admite: `PROPFIND`, `REPORT`, `MKCOL`, `MKCALENDAR`) y `well_known`
(`/.well-known/carddav` y `/.well-known/caldav` -> 301 al prefijo). chi no conoce los metodos WebDAV: se registran y una guardia previa a todo
enrutado los deja pasar solo hacia un prefijo que los declara (en cualquier otro sitio, el RBAC los clasificaria como
lecturas), con la misma ruta con la que chi enruta.

### Autenticacion y fuerza bruta

`mail-auth` acepta `service: "dav"` (flag `dav_access`, contrasena principal o de aplicacion) y, solo a `dav`, devuelve
`username`, `tenant_id` y `mailbox_id`. `mail-dav` verifica cada peticion contra `mail-auth`, con una cache de
aciertos de `MAIL_DAV_AUTH_CACHE_TTL` (10 segundos por defecto, 0 la apaga; ver "Endurecimiento tras la revision
adversaria"): apagar `dav_access`, desactivar una contrasena de aplicacion o cambiar la contrasena se aplica como
mucho pasado ese plazo. Trata un fallo de `mail-auth` como 503, no como 401, para que el cliente no descarte su
contrasena. El freno por (buzon, IP) y por IP es el de `mail-auth` (Redis), el mismo que
protege IMAP; el gateway aplica ademas su cupo general por IP y `mail-dav` el suyo (`MAIL_DAV_RATE_LIMIT_PER_MIN`). No
se usa el cupo estricto de autenticacion del gateway: cada peticion DAV lleva credenciales y un cliente legitimo hace
muchas. Con varias celdas la celda de cada buzon sale del indice de dominios de `organization` y `MAIL_AUTH_CELL_URLS`
da el `mail-auth` de cada una; con una sola, todo va a `MAIL_AUTH_URL`. Un dominio desconocido va a la celda base, que
responde como a una contrasena mala. **Basic solo es admisible porque el proxy de borde termina TLS** (la conexion con
`mail-auth` tambien es HTTPS verificado, sin proxy ni redirecciones).

## Lo decidido al implementar (CalDAV)

### Alcance de la primera version

Calendarios por buzon (uno por defecto, `calendar`, y los que el cliente cree con `MKCALENDAR` o con `MKCOL`
extendido de tipo calendar) con eventos `VEVENT`: `OPTIONS` (`DAV: 1, 3, addressbook, calendar-access`), `PROPFIND`
(`calendar-home-set`, `supported-calendar-component-set`, `supported-calendar-data`, `max-resource-size`, `getctag`,
`sync-token`, `getetag`, `resourcetype` con `calendar`), `REPORT` (`calendar-query`, `calendar-multiget`,
`sync-collection`), `GET`/`HEAD`, `PUT` con `If-Match` e `If-None-Match`, `DELETE` de eventos y de calendarios, y
`MKCALENDAR`. Reutiliza sin cambios la autenticacion Basic contra `mail-auth` (servicio `dav`), el aislamiento por
(`tenant_id`, `mailbox_id`) con RLS, la guardia de metodos del gateway y el consumidor de `mail.mailbox.deleted`.

**Fuera, y dicho:** las **tareas** (`VTODO`) y los diarios (`VJOURNAL`, `VFREEBUSY`) no se admiten (un `PUT` con ellos
es 403 `supported-calendar-component`, y un `MKCALENDAR` que pide solo `VTODO` tambien): guardarlas obliga a otro
modelo de indice (una tarea no tiene `DTEND` y su tabla de solape en RFC 4791 9.9 es otra) y a otro filtro. Las
alarmas (`VALARM`) se guardan dentro del evento tal cual y se devuelven, pero el servidor no las dispara ni las consulta.
`free-busy-query`, la planificacion (iTIP/iMIP, `schedule-inbox`, `CALDAV:schedule-*`), la comparticion, `PROPPATCH`,
`calendar-color`/`calendar-order` (se ignoran al crear un calendario, como el resto de propiedades que el servidor no
guarda) y la recuperacion parcial (`comp` en `calendar-data`, se devuelve el objeto entero) no estan.

### iCalendar y recurrencias: parser propio acotado, sin libreria

Se evaluaron las librerias maduras de Go y sus licencias se leyeron en su `LICENSE` de GitHub (2026-09-21):
`emersion/go-ical` (MIT, Simon Ser, 2020), `teambition/rrule-go` (MIT, Teambition, 2017-2023) y `arran4/golang-ical`
(Apache 2.0). Ninguna se descarta por licencia. La evaluacion funcional fue por su descripcion publica y su API, no
por una lectura completa de su codigo, y la decision descansa menos en lo que ellas hagan que en lo que hay que
escribir de todos modos, con el mismo criterio que `go-webdav` para CardDAV:

* Un iCalendar es entrada de un tercero y hay que **acotarlo donde entra** (bytes, lineas, anidamiento, UTF-8, UID)
  y validarlo con las precondiciones de RFC 4791 (5.3.2.1: un solo UID, sin `METHOD`, componentes admitidos). Eso
  exige un recorrido estructural propio de todos modos; una libreria que construye el arbol entero no ahorra esa
  validacion y agrega otro modelo de objetos entre el texto guardado y lo que se decide.
* La expansion de una regla de recurrencia debe tener **presupuesto** (un evento hostil no puede ocupar la CPU),
  distinguir "no se pudo decidir" de "no cae" (para devolver el evento en el primer caso) y saltar los periodos
  anteriores al rango (una serie diaria de 2015 no se recorre entera para decidir un dia de 2032). Ese contrato es
  de este servicio y se escribe sobre la expansion, no sobre un iterador ajeno.
* Un `TZID` que no es de la base IANA (los nombres propios de Outlook) se resuelve con el `VTIMEZONE` que envio el
  cliente, que necesita evaluar las reglas `STANDARD`/`DAYLIGHT`: con una libreria de recurrencias habria que escribir
  igualmente ese codigo, y con la expansion propia es la misma que ya existe.
* Un servicio con la mayor superficie publica no autenticada por JWT no gana nada con una dependencia de protocolo
  mas; lo que una libreria da (analizar `DTSTART`, `RRULE`, `DURATION`) es pequeno.

Se implementa en `internal/domain`: `ical.go` (estructura y validacion), `ictime.go` (fechas, `DURATION`, zonas),
`rrule.go` (reglas y expansion) y `calendar.go` (indice, `time-range` y filtros). Sin dependencias nuevas (la base
de zonas horarias se embebe con `time/tzdata`, de la biblioteca estandar, porque la imagen es `scratch`).

### Modelo de datos y aislamiento

`migrations/tenant/canonical/mail-dav/03_caldav.sql`: `calendars` (como `addressbooks`), `events` y
`calendar_changes`, con las mismas claves foraneas compuestas `(id, tenant_id, mailbox_id)`, la politica RLS
`mailbox_isolation` y los permisos del grupo `mail_dav_service`. `collection_changes` **no se reutiliza**: su clave
foranea compuesta apunta a `addressbooks` y es la que impide que un cambio cuelgue de la coleccion de otro buzon;
sacarla para servir a dos tipos de coleccion quitaria esa garantia a lo que ya funciona. Se reutiliza el modelo (secuencia
por coleccion, suelo de poda, token `urn:mail-dav:sync:<coleccion>:<seq>`, `MAIL_DAV_CHANGES_RETAINED`) y el codigo:
el repositorio de Postgres tiene un solo camino de coleccion (alta, baja, escritura condicional con bloqueo, registro
de cambios) parametrizado por las tablas de cada tipo. Un calendario y una libreta pueden llamarse igual y sus
cambios no se cruzan (probado).

`events` guarda el iCalendar tal cual (`ical`, con su `etag`, el SHA-256 de esos bytes) y los campos que hacen falta
para listarlo y para descartar por tiempo sin leerlo: `uid` (unico por calendario), `summary`, `first_start` (el
inicio de la primera aparicion) y `last_end` (el fin de la ultima; **nulo si la recurrencia no tiene fin conocido**).
La recurrencia no se indexa expandida: `last_end` nulo quiere decir "puede aparecer en cualquier fecha posterior". Un
indice `(calendar_id, first_start)` sirve al descarte.

### Validacion de la entrada

Como en CardDAV, acotada donde entra: cuerpo maximo `MAIL_DAV_MAX_EVENT_BYTES` (`http.MaxBytesReader`, se corta al
llegar al tope; 403 `max-resource-size`), como maximo `MAIL_DAV_MAX_EVENT_PROPERTIES` lineas, anidamiento maximo de
tres niveles (`VCALENDAR` > `VEVENT` > `VALARM`, o `VTIMEZONE` > `STANDARD`), UTF-8 sin caracteres de control ni CR
suelto, `VERSION:2.0`, un solo `VCALENDAR`, sin `METHOD` (403 `valid-calendar-object-resource`), `VEVENT` de un mismo
`UID` con a lo sumo uno sin `RECURRENCE-ID`, `DTSTART` obligatorio, `DTEND` y `DURATION` no a la vez, `DTEND` no
anterior a `DTSTART`, `RRULE` bien formada y acotada (rangos y longitud de cada lista), a lo sumo 2000 `RDATE`/`EXDATE`,
`VTIMEZONE` completos. Los errores de `PUT` usan las precondiciones de RFC 4791: `valid-calendar-data`,
`valid-calendar-object-resource`, `supported-calendar-component`, `max-resource-size`, `no-uid-conflict` (409, con el
`href` del que ya lo usa) y `supported-calendar-data` (415, tipo distinto de `text/calendar` o charset distinto de
UTF-8). `PUT` sobre un calendario que no existe es 409. Sin XXE (mismo decodificador de XML acotado y sin recursion:
cada nivel de `comp-filter` que se admite es un tipo aparte), sin traversal (lista blanca de nombres, terminados en
`.ics`) y el usuario de la URL debe ser el del buzon autenticado.

### Zonas horarias

El objeto se guarda con su `VTIMEZONE` tal como lo envio el cliente. Para indexar y para decidir un rango, un `TZID`
se resuelve primero como zona de la base IANA (solo con la forma de un nombre de zona: nunca llega al sistema de
ficheros con otra) y, si no lo es, con el `VTIMEZONE` del mismo objeto (`STANDARD`/`DAYLIGHT` con su `RRULE`). Sin
ninguno de los dos la hora se toma como UTC; la hora flotante y el dia completo tambien se toman como UTC (el
`C:timezone` de un `calendar-query` no se aplica). La resolucion de una hora ambigua o inexistente en un cambio de
hora puede diferir una hora de la del cliente en ese instante; solo afecta a un evento pegado al borde de un rango.

### `calendar-query`: filtros y decision del `time-range`

Se admite `comp-filter` `VCALENDAR` > `VEVENT` (y `VTODO`, `VJOURNAL`, `VFREEBUSY`, de los que la coleccion no
guarda ninguno: el filtro se evalua, y `is-not-defined` los admite todos) con `time-range`, `is-not-defined` y
`prop-filter` (`is-not-defined` o `text-match` con `i;ascii-casemap`, `i;unicode-casemap` o `i;octet`, `negate-condition`
y `test` `anyof`/`allof`). Lo que no se evalua **se rechaza con `supported-filter`** (o `supported-collation`), nunca se
ignora: `param-filter`, `time-range` dentro de un `prop-filter`, `comp-filter` de tercer nivel (`VALARM`), componentes
que no son de calendario, elementos desconocidos, mas de un `comp-filter` de primer nivel. Un `calendar-data` con
`expand`, `limit-recurrence-set` o `limit-freebusy-set` es 403 `supported-calendar-data`: el servidor no expande, y
devolver la serie sin expandir a un cliente que la pide expandida es peor que decirle que no. Un `time-range`
mal formado (no UTC, sin extremos, fin anterior al inicio) es 400.

**Como se decide un `time-range` con recurrencias** (la unica expansion del servidor): la base descarta por
`first_start < fin` y `last_end >= inicio` (o `last_end` nulo) sin leer el objeto; sobre lo que queda se relee el
iCalendar y se aplica la tabla de RFC 4791 9.9 a cada aparicion: `DTSTART`, `RDATE` y las de la `RRULE` (`DAILY`,
`WEEKLY`, `MONTHLY` y `YEARLY` con `INTERVAL`, `COUNT`, `UNTIL`, `WKST`, `BYMONTH`, `BYMONTHDAY`, `BYYEARDAY`, `BYDAY`
con ordinales, `BYSETPOS`, `BYHOUR`, `BYMINUTE` y `BYSECOND`), menos las `EXDATE` y las que una sobrescritura
(`RECURRENCE-ID`) reemplaza, mas la propia sobrescritura. La expansion se detiene al pasar el fin del rango, salta los
periodos anteriores al inicio (salvo con `COUNT`, que hay que contar desde el principio) y tiene un **presupuesto**
(periodos recorridos mas apariciones generadas): `MAIL_DAV_MAX_RECURRENCE_WORK` por evento y
`MAIL_DAV_MAX_QUERY_RECURRENCE_WORK` por consulta. **Cuando no se puede decidir el evento se devuelve**: presupuesto
agotado, frecuencias menores que un dia, `BYWEEKNO`, partes de extensiones (`RSCALE`), `RDATE` con periodos o
`RECURRENCE-ID` con `RANGE`. Un evento de mas lo descarta el cliente al expandir por su cuenta; uno omitido es un
dato perdido a la vista del usuario. Es la decision que justifica que la expansion sea acotada y no exacta.
El `prop-filter` de un evento con sobrescrituras se evalua sobre cualquiera de sus componentes, y el `time-range`
sobre el conjunto de apariciones: es un poco mas laxo que evaluar cada instancia por separado, y mas laxo solo
significa devolver de mas.

### Sincronizacion, `MKCALENDAR` y descubrimiento

`ctag` y `sync-token` salen de `calendars.sync_seq`, con las mismas reglas que las libretas (token ligado al
calendario, poda y `changes_floor`, 403 `valid-sync-token`). `MKCALENDAR` (y `MKCOL` con `resourcetype` `calendar`)
crea el calendario con el nombre y la descripcion pedidos; repetirlo es 405, pasar el limite es 507
`quota-not-exceeded`. El principal responde `calendar-home-set` ademas de `addressbook-home-set`, de modo que un mismo
principal sirve a los dos tipos de cliente, y `/.well-known/caldav` redirige al prefijo como el de CardDAV. La ficha
del buzon muestra la misma URL del servidor para contactos y calendarios (`MAIL_DAV_PUBLIC_URL`): el cliente descubre
las libretas y los calendarios a partir de ella.

### Limites de CalDAV

Por buzon, del operador (los de CardDAV siguen igual): `MAIL_DAV_MAX_EVENT_BYTES` (256 KiB, hasta 4 MiB), `MAIL_DAV_MAX_EVENT_PROPERTIES`
(1000), `MAIL_DAV_MAX_EVENTS_PER_MAILBOX` (20000), `MAIL_DAV_MAX_CALENDARS_PER_MAILBOX` (10), y los dos presupuestos de
expansion. Sin configuracion nueva el servicio arranca con esos valores.

### Probado

Unitarias de dominio (validacion, decenas de casos de rechazo, la tabla de solape de la RFC, la expansion de reglas contra
fechas calculadas a mano, `VTIMEZONE` propio frente a la base IANA, presupuesto y reglas hostiles), de aplicacion, de
protocolo con cuerpos escritos a mano al estilo de DAVx5, iOS (`MKCALENDAR` con propiedades de Apple) y Thunderbird
(`calendar-query` por rango), y de integracion contra Postgres real: ciclo de vida, descarte por indices, cambios y
poda, 20 altas simultaneas contra el limite, aislamiento entre buzones y empresas con el rol de servicio y como dueno
de las tablas, y mutaciones revertidas (quitar el filtro de buzon de la busqueda de coleccion y el del borrado por buzon
hace fallar las pruebas; con la politica apagada la misma consulta si veria los eventos ajenos).

## Endurecimiento tras la revision adversaria (2026-09-21)

Revision como atacante autenticado (dueno de una contrasena de DAV, o de una empresa cliente) y como atacante sin
credenciales, de la superficie completa: parser XML, vCard, iCalendar y RRULE, repositorio, `mail-auth`, gateway y
consumidor de bajas. Cada hallazgo se demostro con una prueba que fallaba antes del arreglo (las cifras son las
medidas antes de arreglarlo, en la maquina de desarrollo).

### Lo que estaba mal y como se arreglo

| Hallazgo | Severidad | Prueba y medida | Arreglo |
|---|---|---|---|
| Al releer un objeto guardado para una consulta se recalculaba su indice: la expansion de una recurrencia con presupuesto de 1 048 576 unidades, por cada candidato y en cada `calendar-query` | Alta | Un `RRULE:FREQ=DAILY;COUNT=400000` cuesta 64 ms y 1,2 millones de reservas por evento al releerlo; con 20 000 eventos de ese tipo un solo `REPORT` gasta unos 21 minutos de CPU | `ParseStoredCalendarObject` no calcula el indice (ya esta en la base) |
| Sin tope de contrasenas de aplicacion por buzon, y `mail-auth` comparaba cada intento fallido con TODAS | Alta (afecta a todas las empresas de la celda) | Un administrador de empresa crea miles y cada intento de entrar cuesta miles de bcrypt en el `mail-auth` que tambien atiende a Dovecot | `mail-directory` no deja pasar de 25 por buzon (409 `ErrMaxAppPasswordsReached`) y `mail-auth` lee como mucho 50 |
| Lecturas sin tope: PROPFIND de profundidad 1, `sync-collection`, multiget y consultas cargaban en memoria toda la libreta (o el calendario) CON sus objetos, se pidieran o no | Media a alta | 10 000 contactos de 256 KiB son 2,5 GiB por peticion; un multiget con el mismo `href` repetido multiplicaba el contenido por lo que cupiera en el cuerpo | Un listado sin `address-data`/`calendar-data` no lee los objetos (solo su tamano, `octet_length`, que no descomprime); con datos se corta al pasar `MAIL_DAV_MAX_RESPONSE_BYTES` (507 `number-of-matches-within-limits`); `addressbook-query` y `calendar-query` recorren de uno en uno y solo conservan las coincidencias; los `href` repetidos (o escritos de otra forma) se responden una vez |
| El prop de una peticion se repetia en la respuesta de cada recurso | Media | Un cuerpo de 250 KiB pide 60 000 propiedades de cada contacto | Mas de 128 propiedades es 400; las repetidas se responden una vez |
| Espacio sin tope por buzon | Media | 10 000 contactos y 20 000 eventos de 256 KiB son 7,5 GiB por buzon en la base compartida | `MAIL_DAV_MAX_MAILBOX_BYTES` (64 MiB por defecto, contactos y eventos por separado), comprobado en el alta y en cada modificacion bajo el cerrojo del buzon; reducir un objeto nunca se rechaza |
| El desdoblado de lineas de continuacion era cuadratico | Media | Un vCard de 250 KiB de continuaciones tarda 0,58 s y reserva 2,2 GiB; se repetia por objeto en cada consulta | Cada linea logica se une una sola vez |
| El tiempo de respuesta a una contrasena mala distinguia buzones: uno con contrasenas de aplicacion tardaba (1+N) comparaciones frente a 1 de uno inexistente | Media (enumeracion de direcciones) | 241 ms frente a 40 ms con cinco contrasenas de aplicacion | Toda contrasena que no es la principal cuesta dos rondas: la segunda compara las de aplicacion a la vez (o un hash ficticio) |
| Una verificacion de bcrypt y una fila en `mail.sasl_logins` (mas la actualizacion de `last_used_at`) por peticion HTTP | Media | 20 peticiones de un cliente dejaban 20 filas, en una tabla sin retencion | Cache de aciertos en `mail-dav`, tope de verificaciones simultaneas y un registro por cliente y ventana de 5 minutos para los protocolos que verifican cada peticion |
| Sin tope de peticiones simultaneas ni plazo: pool de diez conexiones por empresa, consultas que seguian tras el `WriteTimeout` | Media | Peticiones acumuladas esperando conexion | `MAIL_DAV_MAX_INFLIGHT` (503 con `Retry-After`), `MAIL_DAV_REQUEST_TIMEOUT` (cancela las consultas) y el recorrido de las consultas comprueba el contexto |
| Escritura y baja de los datos de un buzon tomaban los cerrojos en orden distinto | Baja | Cuatro escritores contra la baja fallan en la primera ronda con `deadlock detected` (40P01): Postgres aborta una de las dos | La escritura toma el cerrojo del buzon antes que el de la coleccion, como la baja |
| `TZID` con forma de nombre de zona pero inexistente: se buscaba de nuevo en cada aparicion | Baja | 22 microsegundos por aparicion; una serie de 15 000 apariciones, 330 ms | Cache acotada (1024 nombres) de zonas inexistentes |

### Configuracion nueva (todo arranca igual sin ella)

`MAIL_DAV_MAX_MAILBOX_BYTES` (64 MiB), `MAIL_DAV_MAX_RESPONSE_BYTES` (16 MiB; un vCard o un evento de tamano maximo debe
caber en los dos, o el servicio no arranca), `MAIL_DAV_MAX_INFLIGHT` (64), `MAIL_DAV_REQUEST_TIMEOUT` (25 s),
`MAIL_DAV_AUTH_CACHE_TTL` (10 s, 0 la apaga) y `MAIL_DAV_AUTH_MAX_CONCURRENT` (16). Migraciones: ninguna.

### La cache de verificaciones y lo que cuesta

Solo se recuerda un acierto, con una clave HMAC (con una clave aleatoria que solo vive en el proceso) del buzon y la
contrasena: la contrasena no queda en memoria y un rechazo nunca se recuerda, de modo que corregirla entra a la
siguiente peticion. El precio es que apagar `dav_access`, desactivar una contrasena de aplicacion, cambiarla o dar de
baja el buzon se aplica como mucho a los 10 segundos (`MAIL_DAV_AUTH_CACHE_TTL`; con 0 vuelve a ser inmediato y a costar
un bcrypt por peticion). Un acierto recordado no pasa por el freno de `mail-auth`, que solo protege de lo que falla.
`ops/e2e/mail.sh` arranca `mail-dav` con 2 segundos y espera 3 tras cada revocacion.

### Lo que se reviso y estaba bien (con prueba)

* XML: `encoding/xml` no resuelve entidades (externas, `billion laughs` y DTD dan 400), los cuerpos se decodifican en
  estructuras sin recursion y el anidamiento profundo se salta de forma iterativa; el cuerpo esta acotado y los
  timeouts del servidor (15 s de lectura, 5 s de cabeceras) frenan el `slowloris`.
* Expresiones regulares: solo `regexp` de Go (tiempo lineal), sin retroceso: no hay ReDoS.
* Los analizadores (iCalendar con su expansion de recurrencias, vCard y RRULE) se probaron con fuzzing nativo
  (`internal/domain/fuzz_test.go`: unos 3 millones de ejecuciones del de iCalendar, con un tope de 250 ms por entrada, y
  medio millon a tres millones de los otros): ni un panico ni una entrada lenta. `go test -fuzz` los vuelve a lanzar.
* Aislamiento: la ruta se decodifica por segmentos (sin `..`, `%2F`, `\`, NUL ni controles), el usuario de la URL debe
  ser el del buzon autenticado, un token de sincronizacion de otra coleccion es 403, cada consulta filtra por empresa
  y buzon y las politicas RLS las repiten (probado con el rol de servicio y como dueno de las tablas).
* `sync-collection`: la secuencia y el registro de cambios se escriben en la misma transaccion con la coleccion
  bloqueada (`FOR UPDATE`), un cambio y su numero son atomicos y las lecturas nunca ven un cambio posterior al token; el
  registro se poda en cada escritura a `MAIL_DAV_CHANGES_RETAINED` (no crece sin limite).
* Concurrencia: `If-Match` e `If-None-Match` se evaluan con la coleccion bloqueada; el limite de objetos, bajo el
  cerrojo del buzon (probado con 20 altas simultaneas).
* Gateway: un metodo de extension solo llega a un prefijo que lo declara, la IP del visitante que llega a `mail-dav` no
  la fija el cliente (`X-Real-IP` y `X-Forwarded-For` de un cliente de Internet se descartan, probado), el descubrimiento
  redirige a una ruta constante (no hay redireccion abierta) y el cupo por IP del gateway alcanza a `/api/v1/dav`.
* Ninguna credencial Basic llega a los registros (probado con un observador de `zap`), y no hay sesion que fijar.
* Volumen (Postgres real, 50 000 contactos y 50 000 eventos en un buzon y otros 100 000 alrededor): listado de la libreta
  sin datos 110 ms en la base, multiget de 50 contactos 4 ms, `calendar-query` de un dia 5 ms (usa `idx_mail_dav_events_range`),
  alta con comprobacion de espacio y de conteo 26 a 38 ms, `sync-collection` de 1000 cambios 62 ms.

### Recomendaciones (no implementadas)

* **Paginar `sync-collection`** con 507 y un token de continuacion (RFC 6578, 3.6). No se hizo: sin datos una respuesta es
  de metadatos (unos 600 bytes por recurso) y con datos la acota `MAIL_DAV_MAX_RESPONSE_BYTES`; la sincronizacion inicial
  paginada exige un token con cursor.
* **Retencion de `mail.sasl_logins`** (de `mail-directory` o `mail-auth`): la tabla no se poda nunca; con el registro por
  ventana crece mucho menos, pero sigue creciendo.
* **Cobrar el presupuesto de expansion por dias evaluados**: una unidad de trabajo de una regla `YEARLY` evalua unos 366
  dias; `FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30` cuesta 12,6 ms por evento y una consulta puede llegar a 1,3 s de CPU con
  `MAIL_DAV_MAX_QUERY_RECURRENCE_WORK` por defecto.
* **Tope de cuerpo en el gateway para `dav`** (hoy lo pone `mail-dav`, y el gateway reenvia en streaming con su
  `ReadTimeout`); requiere un campo en `routes.json`.
* **Indice `(addressbook_id, resource_name)` para ordenar** el listado de contactos: hoy Postgres ordena en memoria
  (110 ms con 50 000).
* **Un buzon con contrasenas de aplicacion creadas antes del tope** sigue funcionando (las lee `mail-auth` hasta 50); las
  que pasen de 25 conviene revocarlas.

## Alternativas descartadas

* **Embeber Radicale, Baikal u otro servidor DAV en Python o PHP**: reintroduce lo que se quito de mailcow y una
  segunda pila que operar y parchear; su modelo de usuarios y ficheros no encaja con la base por empresa.
* **Solo un cliente web de contactos**: no atiende al que necesita sincronizar con el movil, que es el motivo.
* **`go-webdav`**: ver arriba.
* **Cachear la verificacion en `mail-dav` sin plazo o sin acotarla**: se descarto en la primera version y se retomo tras
  la revision adversaria, con un TTL corto y explicito (10 segundos, configurable, solo aciertos, clave HMAC con una
  clave que vive en el proceso, tamano acotado): sin ella cada peticion de un cliente que sincroniza era un bcrypt y una
  fila en `mail.sasl_logins`. Retrasa hasta ese plazo la revocacion de una contrasena o de `dav_access`.

## Consecuencias y riesgos

* Es el servicio con mas superficie publica no autenticada por JWT: Basic sobre Internet exige TLS siempre (el
  borde ya lo termina), frenos por IP y buzon, y limites estrictos de cuerpo y de objetos.
* Los clientes DAV son muy dispares (iOS y Outlook con extensiones propias); **falta probar contra al menos DAVx5,
  Thunderbird e iOS** antes de darlo por bueno. Cloudflare, si esta delante del borde, debe dejar pasar `PROPFIND`,
  `REPORT`, `MKCOL` y `MKCALENDAR`: hay que comprobarlo.
* Un cambio de modelo de datos de vCard e iCalendar es dificil de revertir: los objetos se guardan tal cual los
  envio el cliente y se validan, no se normalizan.
* Cada verificacion que no acierta la cache cuesta un bcrypt en `mail-auth` (que tambien atiende a Dovecot): la cache de
  aciertos, el tope de verificaciones simultaneas (`MAIL_DAV_AUTH_MAX_CONCURRENT`), el cupo por IP y el freno de
  `mail-auth` (por buzon e IP, antes del bcrypt) lo acotan.
* `calendar-query` con `time-range` relee y evalua uno a uno los eventos que el indice no descarta (sin acumularlos:
  solo se conservan las coincidencias); una serie sin fin siempre es candidata y cuesta su expansion (acotada por
  presupuesto, y devuelta si no alcanza). Con muchas series sin fin por buzon la consulta gasta el presupuesto y
  devuelve de mas: las metricas dirian si conviene indexar mas (por ejemplo, almacenar la regla y la proxima
  aparicion).
* `addressbook-query` recorre uno a uno los contactos de la libreta (acotados por buzon, hasta
  `MAIL_DAV_MAX_CONTACTS_PER_MAILBOX`, y por espacio, `MAIL_DAV_MAX_MAILBOX_BYTES`); con los limites por defecto es
  barato, con limites muy altos habria que indexar.

## Pendiente

* **Comparticion entre buzones** (`shares`, solo lectura o escritura), de libretas y de calendarios, y la libreta de
  solo lectura "Directorio de la empresa" generada desde `mail-directory`.
* **Planificacion por CalDAV** (RFC 6638: `schedule-inbox`/`outbox`, `CALDAV:schedule-*`): las invitaciones iMIP las
  envia y procesa el webmail (seccion "Invitaciones, disponibilidad y citas"); un cliente CalDAV que crea una reunion
  la guarda y la sincroniza, pero el servidor no envia su invitacion.
* **Alarmas** (`VALARM`): se guardan y se devuelven; el servidor no las dispara y no se pueden consultar.
* **Tareas** (`VTODO`) y diarios: fuera de la primera version (ver arriba).
* `free-busy-query`, `PROPPATCH` (renombrar o pintar un calendario o una libreta desde el cliente),
  `calendar-color`, `calendar-timezone`, recuperacion parcial de `calendar-data` y `address-data`, `expand` y
  `limit-recurrence-set`, y `param-filter`.
* Expansion exacta de lo que hoy se devuelve sin decidir (frecuencias menores que un dia, `BYWEEKNO`, `RDATE` con
  periodos, `RECURRENCE-ID` con `RANGE`) si las metricas muestran que se devuelve demasiado de mas.
* **Limites como derechos del plan** de `billing` (hoy son de operador, por variable de entorno).
* Prueba con DAVx5, Thunderbird e iOS reales, y `make e2e-mail` con las secciones nuevas (CardDAV y CalDAV): estan
  escritas y sin ejecutar.
* Eventos de auditoria (`dav.*`) por la outbox si la auditoria de contactos y eventos personales se exige.
