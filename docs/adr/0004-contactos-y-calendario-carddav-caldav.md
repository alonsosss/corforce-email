# ADR 0004: contactos y calendario personales por CardDAV y CalDAV

## Estado

Propuesto (2026-09-21). Cierra el diseno de `Plan_Estrategico_Mejoras_Correo.md`, C3 fase 2. La fase 1 (libreta
compartida de la empresa en el webmail) ya esta hecha y no depende de esto. La decision de construirlo la toma el
responsable del producto: es un servicio nuevo de tamano L a XL y solo tiene sentido si un cliente real lo pide.

## Contexto

mailcow entrega contactos y calendario con SOGo (SOGo, PHP, nginx y MySQL, que aqui no se copiaron). Sin ellos,
el correo de esta plataforma se usa con el calendario y los contactos de otro proveedor. Los clientes de
escritorio y movil hablan CardDAV (RFC 6352) y CalDAV (RFC 4791) sobre WebDAV (RFC 4918); ActiveSync queda
descartado en el plan (protocolo propietario y caro).

## Decision

Un servicio nuevo `mail-dav`, en Go, molde hexagonal, **plano de empresa** (los contactos y eventos son datos de la
empresa, van en `mail_tenant_<slug>`, con `tenant_id`, RLS y sin claves foraneas entre esquemas). Fases:

1. **CardDAV primero** (libretas y contactos vCard 3.0 y 4.0), porque es la mitad barata y valida el resto:
   autenticacion, ruta publica, sincronizacion por `ctag` y `etag`, cliente real (iOS, Thunderbird, DAVx5).
2. **CalDAV despues** (calendarios y eventos iCalendar, tareas opcionales), con `sync-collection` (RFC 6578) y
   recurrencias sin expandir en el servidor: se guardan y se devuelven, el cliente las expande.

Sobre WebDAV se usa una libreria Go madura (`github.com/emersion/go-webdav`, licencia MIT) en lugar de escribir
el protocolo a mano; se verifica su licencia y su mantenimiento al empezar, y se fija la version. El almacenamiento
es propio (Postgres), detras de la interfaz de backend que esa libreria define.

### Autenticacion

Los clientes DAV usan HTTP Basic, no sesion ni JWT. Se autentican contra `mail-auth` con un servicio nuevo `dav`
(`ProtocolDAV`, con flag propio `dav_access` en el buzon y en la contrasena de aplicacion, como `sieve_access`),
que admite **contrasena de aplicacion** y la principal. `mail-dav` no valida credenciales: pregunta a `mail-auth`
y recibe el buzon y su empresa. Freno por buzon e IP, como el webmail. Cada peticion se atribuye a un buzon y solo
ve sus colecciones y las que se compartieron con el.

El gateway sigue siendo la unica entrada: `mail-dav` se declara con un prefijo `self_authenticated` en
`services/gateway/routes.json`, igual que el webmail, y el gateway no valida JWT para el. Las rutas de
descubrimiento (`/.well-known/carddav` y `/.well-known/caldav`) responden con la redireccion al prefijo.

### Datos

`mail_dav.addressbooks`, `contacts` (vCard como texto validado y campos indexados: uid, nombre, correos),
`calendars`, `events` (iCalendar, uid, inicio, fin, recurrencia, `etag`), `collection_changes` (para
`sync-collection`), y `shares` (compartir con otro buzon de la empresa, solo lectura o escritura). Limites por
buzon (tamano de un objeto, numero de objetos y colecciones) como derechos del plan de `billing`. Los objetos
importados se validan y se acotan: un vCard o un iCalendar es entrada de un tercero.

### Encaje con lo que ya hay

* La libreta compartida de la fase 1 (directorio de buzones activos de la empresa) se ofrece como una libreta
  de solo lectura "Directorio de la empresa" de cada buzon, sin duplicar datos: se genera desde `mail-directory`.
* El webmail no cambia: sigue con su libreta y su selector. Una pantalla de contactos y calendario en el webmail
  es una fase posterior y opcional.

## Alternativas descartadas

* **Embeber Radicale, Baikal u otro servidor DAV en Python o PHP**: reintroduce lo que se quito de mailcow y una
  segunda pila que operar y parchear; su modelo de usuarios y ficheros no encaja con la base por empresa.
* **Solo un cliente web de contactos**: no atiende al que necesita sincronizar con el movil, que es el motivo.

## Consecuencias y riesgos

* Es el servicio con mas superficie publica no autenticada por JWT: Basic sobre Internet exige TLS siempre (el
  borde ya lo termina), frenos por IP y buzon, y limites estrictos de cuerpo y de objetos.
* Los clientes DAV son muy dispares (iOS y Outlook con extensiones propias); se prueba contra al menos DAVx5,
  Thunderbird e iOS antes de darlo por bueno.
* Un cambio de modelo de datos de vCard e iCalendar es dificil de revertir: los objetos se guardan tal cual los
  envio el cliente y se validan, no se normalizan.

## Que falta para empezar

Decision explicita del responsable del producto (segun el plan, no se empieza sin ella) y un cliente que lo pida.
