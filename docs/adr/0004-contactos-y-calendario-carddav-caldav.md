# ADR 0004: contactos y calendario personales por CardDAV y CalDAV

## Estado

**Parcialmente implementado: CardDAV** (2026-09-21). El responsable del producto autorizo construirlo. Hecho: el
servicio `mail-dav` con CardDAV (contactos personales de cada buzon, sincronizables con iOS, Thunderbird y DAVx5),
su autenticacion contra `mail-auth`, el flag `dav_access` del buzon, los datos de conexion en la ficha del buzon y
las pruebas. **Pendiente**: CalDAV (calendarios y eventos), comparticion entre buzones, la libreta de solo lectura
"Directorio de la empresa" y limites como derechos del plan de `billing` (secciones "Lo decidido al implementar" y
"Pendiente"). El borrado de los contactos de un buzon borrado esta hecho (2026-09-21, ver "Borrado de los datos de
un buzon"). Cierra el diseno de
`Plan_Estrategico_Mejoras_Correo.md`, C3 fase 2. La fase 1 (libreta compartida de la empresa en el webmail) ya
estaba hecha y no depende de esto.

Lo verificado y lo que no: el codigo compila y pasa las pruebas unitarias, las de integracion contra Postgres real y
`make checks`; los cuerpos de peticion de DAVx5, iOS y Thunderbird de las pruebas estan escritos a mano segun las RFC
(no capturados de un cliente). **No se ha probado con un cliente real ni con `make e2e-mail`** (la seccion de
`ops/e2e/mail.sh` esta escrita y sin ejecutar): el ADR original exige DAVx5, Thunderbird e iOS antes de darlo por bueno.

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
2. **CalDAV despues** (calendarios y eventos iCalendar, tareas opcionales), con `sync-collection` (RFC 6578) y
   recurrencias sin expandir en el servidor: se guardan y se devuelven, el cliente las expande. **Pendiente.**

El almacenamiento es propio (Postgres) y el protocolo lo sirve el propio servicio (ver "Libreria WebDAV").

### Autenticacion

Los clientes DAV usan HTTP Basic, no sesion ni JWT. Se autentican contra `mail-auth` con un servicio nuevo `dav`
(`ProtocolDAV`, con flag propio `dav_access` en el buzon y en la contrasena de aplicacion, como `sieve_access`),
que admite **contrasena de aplicacion** y la principal. `mail-dav` no valida credenciales: pregunta a `mail-auth`
y recibe el buzon y su empresa. Freno por buzon e IP, como el webmail. Cada peticion se atribuye a un buzon y solo
ve sus colecciones (las que se compartan con el son la fase de comparticion, pendiente).

El gateway sigue siendo la unica entrada: `mail-dav` se declara con un prefijo `self_authenticated` en
`services/gateway/routes.json`, igual que el webmail, y el gateway no valida JWT para el. Las rutas de
descubrimiento (`/.well-known/carddav`, y `/.well-known/caldav` cuando exista CalDAV) responden con la redireccion al
prefijo.

### Datos

`mail_dav.addressbooks`, `contacts` (vCard como texto validado y campos indexados: uid, nombre, correos),
`collection_changes` (para `sync-collection`); despues `calendars`, `events` (iCalendar, uid, inicio, fin,
recurrencia, `etag`) y `shares` (compartir con otro buzon de la empresa, solo lectura o escritura). Limites por
buzon (tamano de un objeto, numero de objetos y colecciones); como derechos del plan de `billing` queda pendiente.
Los objetos importados se validan y se acotan: un vCard o un iCalendar es entrada de un tercero.

### Encaje con lo que ya hay

* La libreta compartida de la fase 1 (directorio de buzones activos de la empresa) se ofrecera como una libreta
  de solo lectura "Directorio de la empresa" de cada buzon, sin duplicar datos: se genera desde `mail-directory`.
  Pendiente.
* El webmail no cambia: sigue con su libreta y su selector. Una pantalla de contactos y calendario en el webmail
  es una fase posterior y opcional.

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

### Borrado de los datos de un buzon

Al borrar un buzon, `mail-directory` publica `mail.mailbox.deleted` (payload `tenant_id`, `id`, `username`, ...) por su
outbox, y `mail-dav` lo consume (`internal/adapters/nats`, durable `mail-dav-mailbox-deleted` sobre el stream
`MAIL_DIRECTORY`, que el propio consumidor declara con `EnsureStream`) y borra las libretas del buzon; los contactos
y el registro de cambios caen por la clave foranea `ON DELETE CASCADE`.

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
  evento (milisegundos frente a la latencia de la outbox y de NATS) dejaria una libreta huerfana; el barrido de
  conciliacion de "Pendiente" la retiraria.
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
`principals/<buzon>/`, `addressbooks/<buzon>/` y `addressbooks/<buzon>/<libreta>/<recurso>.vcf`. La libreta por
defecto (`contacts`, nombre `MAIL_DAV_DEFAULT_ADDRESSBOOK_NAME`) se crea al listar el home de un buzon sin libretas
(si borra todas, reaparece al siguiente descubrimiento). El gateway declara en `routes.json` el prefijo
(`self_authenticated`, con los metodos WebDAV que admite: `PROPFIND`, `REPORT`, `MKCOL`) y `well_known`
(`/.well-known/carddav` -> 301 al prefijo). chi no conoce los metodos WebDAV: se registran y una guardia previa a todo
enrutado los deja pasar solo hacia un prefijo que los declara (en cualquier otro sitio, el RBAC los clasificaria como
lecturas), con la misma ruta con la que chi enruta.

### Autenticacion y fuerza bruta

`mail-auth` acepta `service: "dav"` (flag `dav_access`, contrasena principal o de aplicacion) y, solo a `dav`, devuelve
`username`, `tenant_id` y `mailbox_id`. `mail-dav` verifica cada peticion (sin cache: apagar `dav_access` o desactivar
una contrasena de aplicacion se aplica en la siguiente) y trata un fallo de `mail-auth` como 503, no como 401, para que
el cliente no descarte su contrasena. El freno por (buzon, IP) y por IP es el de `mail-auth` (Redis), el mismo que
protege IMAP; el gateway aplica ademas su cupo general por IP y `mail-dav` el suyo (`MAIL_DAV_RATE_LIMIT_PER_MIN`). No
se usa el cupo estricto de autenticacion del gateway: cada peticion DAV lleva credenciales y un cliente legitimo hace
muchas. Con varias celdas la celda de cada buzon sale del indice de dominios de `organization` y `MAIL_AUTH_CELL_URLS`
da el `mail-auth` de cada una; con una sola, todo va a `MAIL_AUTH_URL`. Un dominio desconocido va a la celda base, que
responde como a una contrasena mala. **Basic solo es admisible porque el proxy de borde termina TLS** (la conexion con
`mail-auth` tambien es HTTPS verificado, sin proxy ni redirecciones).

## Alternativas descartadas

* **Embeber Radicale, Baikal u otro servidor DAV en Python o PHP**: reintroduce lo que se quito de mailcow y una
  segunda pila que operar y parchear; su modelo de usuarios y ficheros no encaja con la base por empresa.
* **Solo un cliente web de contactos**: no atiende al que necesita sincronizar con el movil, que es el motivo.
* **`go-webdav`**: ver arriba.
* **Cachear la verificacion en `mail-dav`**: ahorraria un bcrypt por peticion, pero retrasaria la revocacion de una
  contrasena o de `dav_access`. Mejora futura si las metricas lo piden, con un TTL corto y explicito.

## Consecuencias y riesgos

* Es el servicio con mas superficie publica no autenticada por JWT: Basic sobre Internet exige TLS siempre (el
  borde ya lo termina), frenos por IP y buzon, y limites estrictos de cuerpo y de objetos.
* Los clientes DAV son muy dispares (iOS y Outlook con extensiones propias); **falta probar contra al menos DAVx5,
  Thunderbird e iOS** antes de darlo por bueno. Cloudflare, si esta delante del borde, debe dejar pasar `PROPFIND`,
  `REPORT` y `MKCOL`: hay que comprobarlo.
* Un cambio de modelo de datos de vCard e iCalendar es dificil de revertir: los objetos se guardan tal cual los
  envio el cliente y se validan, no se normalizan.
* Cada peticion cuesta una verificacion de bcrypt en `mail-auth`. Un cliente que sincroniza a menudo con muchas
  peticiones cortas lo notara; el cupo por IP lo acota.
* `addressbook-query` filtra en memoria los contactos de la libreta (acotados por buzon, hasta
  `MAIL_DAV_MAX_CONTACTS_PER_MAILBOX`); con el limite por defecto es barato, con limites muy altos habria que indexar.

## Pendiente

* **CalDAV** (fase 2 del ADR): `calendars`, `events`, iCalendar, `well-known/caldav`.
* **Comparticion entre buzones** (`shares`, solo lectura o escritura) y la libreta de solo lectura "Directorio de la
  empresa" generada desde `mail-directory`.
* **Limites como derechos del plan** de `billing` (hoy son de operador, por variable de entorno).
* **Barrido de conciliacion de buzones borrados**: el consumidor de `mail.mailbox.deleted` solo ve los eventos que
  el stream `MAIL_DIRECTORY` aun conserva; lo borrado antes de que existiera el consumidor, o mas alla de la
  retencion del stream, no se retira. Un barrido que cruce los `mailbox_id` de `mail_dav` con `mail-directory` lo
  cubriria.
* `PROPPATCH` (renombrar una libreta desde el cliente), recuperacion parcial de `address-data` y `param-filter`.
* Prueba con DAVx5, Thunderbird e iOS reales, y `make e2e-mail` con la seccion nueva.
* Eventos de auditoria (`dav.*`) por la outbox si la auditoria de contactos personales se exige.
