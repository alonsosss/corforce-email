# Sesión y Content Security Policy

Decisiones de seguridad del navegador y por qué son como son. Cambiar cualquiera de estas
piezas sin leer esto vuelve a abrir un agujero que ya estuvo abierto.

## La sesión no vive en el navegador

**Refresh token**: cookie `cf_rt` con `HttpOnly`, `Secure`, `SameSite=Strict` y
`Path=/api/v1/auth`. No viaja en peticiones de negocio (solo renovar y cerrar sesión) y
ningún script puede leerla. La emite `identity` en login, verificación MFA, renovación y
cambio de sede.

En modo cookie el refresh token **se omite del cuerpo** de la respuesta. Si siguiera en el
JSON, un XSS podría leerlo de la respuesta del login y toda la protección sería
decorativa.

**Access token**: solo en memoria, en una propiedad no enumerable de `window`
(`__cfSession`, `web/src/api/client.ts`), nunca en `localStorage` ni en `sessionStorage`. Tras recargar, la
sesión se restaura pidiendo un token nuevo con la cookie. El webmail no usa este token: su
sesión es la cookie `cf_wm` de su propio servicio.

**Modo cookie es opt-in** (`cookie_auth: true` o cabecera `X-Auth-Mode: cookie`): los
clientes que no son navegador siguen recibiendo el refresh token en el JSON.

`AUTH_COOKIE_SECURE` controla el atributo `Secure` y su valor por defecto es **true**. No
se ata a `ENVIRONMENT`: un `ENVIRONMENT` mal declarado en un servidor habría sacado la
cookie sin `Secure` sin que nadie lo notara. Por la misma razón, un servidor declara
`ENVIRONMENT=production` (o `staging`): con `development` los servicios admiten Redis en
claro (`docs/Operacion_Despliegue.md`, 1).

### Al tocar el cliente HTTP

El cliente HTTP vive en `web/src/api/client.ts`, dentro de la única aplicación web:
cambiarlo exige reconstruir y desplegar solo la imagen `web`.

## Firma del access token

`identity` firma con **EdDSA (Ed25519)** y una clave privada que solo él recibe
(`JWT_SIGNING_KEY`, almacén de secretos). `docker-compose.yml` la vacía en todos los demás
servicios y `ops/security/secrets/check-secrets.sh` falla si un servicio la hereda. La
cabecera lleva `kid` (la clave con que se firmó, `JWT_SIGNING_KID`) y `typ`. El gateway
verifica con las claves **públicas** de `JWT_PUBLIC_KEYS` (entradas `kid:clave` separadas
por comas, configuración no secreta): no tiene con qué firmar, así que un servicio
comprometido, los de celda incluidos, no puede forjar una sesión. Antes todos recibían
`JWT_SECRET` (HS256) y cualquiera de ellos podía emitir un token que el gateway aceptaba.
`JWT_SECRET` ya no existe.

El verificador (`pkg/auth.Verifier`) rechaza:

* cualquier `alg` distinto de `EdDSA`: `none`, HS256 (también el firmado con la clave
  pública como secreto) y el resto. El algoritmo está fijado en código, no se lee del token;
* un `kid` ausente o que no esté entre las públicas aceptadas, y una firma que no verifica
  con la clave de ese `kid`;
* un `typ` distinto del esperado: `at+jwt` (acceso), `mfa-challenge+jwt` (reto MFA) o
  `step-up+jwt`. Los tres salen de la misma clave. Con HS256 y sin tipo, el token de reto
  MFA, que se emite tras la contraseña y antes del segundo factor, pasaba el gateway como
  sesión;
* un emisor distinto de `core-force-mail`, `exp` ausente o vencido, y un token de acceso
  sin `uid` o sin `tid`.

Formatos: privada PKCS#8 DER y pública SubjectPublicKeyInfo DER, las dos en base64 estándar
de una línea (el almacén no admite saltos de línea); `kid` de 1 a 64 caracteres
`[A-Za-z0-9._-]`. `ops/security/jwt-keygen.sh` genera el par y un `kid` `AAAAMMDD-xxxxxxxx`.

Arranque, fallando cerrado: sin `JWT_PUBLIC_KEYS` el gateway no arranca. Sin clave de firma
identity tampoco, salvo con `ENVIRONMENT` declarado `development` o `test`, donde firma con
un par efímero y lo avisa con su clave pública. Si `JWT_PUBLIC_KEYS` está fijada, tiene que
publicar la clave de firma bajo su `kid`, o identity no arranca: el gateway rechazaría todo
lo que emite.

Rotación: el verificador acepta varias públicas a la vez y identity firma solo con la
vigente. Pasos en `docs/Operacion_Despliegue.md` (2).

**Paso de HS256 a EdDSA sin transición.** El gateway nunca acepta HS256, tampoco durante
el despliegue. El access token dura 5 minutos y el refresh es opaco en base de datos, sin
firma, así que no le afecta el cambio. El primer 401 lo resuelve el cliente: renueva una
vez y reintenta (`web/src/api/client.ts`), sin volver a iniciar sesión. Como mucho se
pierden un reto MFA a medias (se vuelve a escribir la contraseña) y un step-up vigente (se
reconfirma). Aceptar HS256 durante una ventana habría obligado a devolver al gateway el
secreto compartido que se quería retirar.

Probado en `pkg/auth`, `services/identity`, `services/gateway` y `make e2e`.

## CSP

Se fija en `pkg/middleware.SecureHeaders`. `script-src` usa **nonce por respuesta** y
`'strict-dynamic'`, sin `'unsafe-inline'` ni `'unsafe-eval'`. Se añaden `object-src 'none'`,
`base-uri 'self'` y `form-action 'self'`.

Con `'strict-dynamic'` el navegador **ignora la lista de orígenes**: no basta con que un
archivo se sirva desde el propio dominio, tiene que llevar el nonce que el servidor sorteó
para esa respuesta. La confianza se propaga del script marcado a los que él cargue, que es
lo que mantiene vivos los remotos federados (`remoteEntry.js` y sus imports dinámicos).

El gateway marca los `<script>` del documento con ese nonce (`stampCSPNonce`) y pide el
HTML sin comprimir **solo para el documento** —el navegador manda `text/html` en `Accept`—;
los assets siguen viajando comprimidos.

`style-src` conserva `'unsafe-inline'` a propósito: el sistema de diseño inyecta estilos y
un estilo no ejecuta código.

El gateway borra las cabeceras de seguridad que vengan del servicio de origen antes de
poner las suyas. Con dos políticas el navegador aplica la intersección, y una cabecera
repetida termina bloqueando algo sin explicación. Excepción: los prefijos
`self_authenticated` conservan la CSP del servicio (ver «Sesión del webmail»).

### Los scripts que Cloudflare inyecta en el borde

Cloudflare inserta un script inline para su detección de bots (`challenge-platform`, JS
Detections). Cuando la política declara un nonce, Cloudflare **marca con él** sus scripts
inyectados, así que se ejecutan sin que la política tenga que admitir ningún otro inline.

Antes del nonce ese script quedaba bloqueado: no era un problema de seguridad (la CSP
hacía su trabajo), pero Cloudflare perdía una señal antibot y, el día que se activara un
Managed Challenge, el desafío habría fallado para usuarios legítimos.

Un hash en la CSP no sirve: el script cambia entre cargas.

## Sesión del webmail

El webmail (`services/webmail`) no usa la sesión de la plataforma: sus usuarios son buzones
de correo, no usuarios de identity. Token opaco de 256 bits en la cookie `cf_wm` con
`HttpOnly`, `Secure` (misma variable `AUTH_COOKIE_SECURE`), `SameSite=Strict` y
`Path=/api/v1/webmail`. En Redis solo está su SHA-256, con la inactividad como TTL
(`WEBMAIL_SESSION_IDLE`) y una vida máxima (`WEBMAIL_SESSION_MAX`) que aplica el servicio
aunque haya actividad. El token se rota en cada inicio de sesión y nunca viaja en un
cuerpo. Toda escritura exige además un `Origin` de `CORS_ALLOWED_ORIGINS` o `API_ORIGIN`:
`SameSite=Strict` ya impide que otro sitio envíe la cookie, y el `Origin` cierra el paso a
los subdominios del mismo sitio.

La revocación es por buzón: una marca en Redis con el instante del cambio (contraseña,
desactivación, baja) invalida toda sesión abierta antes, aunque su clave siga ahí.

El gateway enruta el webmail como prefijo `self_authenticated` (sin JWT ni RBAC, con los
limitadores y las cabeceras del borde) y, solo para esos prefijos, **conserva la CSP del
servicio** junto a la suya: sus respuestas son datos del buzón y adjuntos, nunca la
aplicación, y su política (`default-src 'none'; sandbox`) es más estricta, así que la
intersección que aplica el navegador es la del servicio. Los adjuntos salen siempre con
`Content-Disposition: attachment`, `nosniff` y un tipo inofensivo (`application/octet-stream`
salvo imágenes de mapa de bits, PDF y texto plano).

El HTML de un mensaje llega ya saneado (bluemonday) dentro del JSON. La interfaz debe
pintarlo en un `<iframe sandbox>` sin `allow-scripts` ni `allow-same-origin`: el saneado es
la primera barrera y el aislamiento la segunda.

## Límites de peticiones del gateway

Dos limitadores por IP (`services/gateway/ratelimit.go`, `pkg/middleware/ratelimit.go`):

| Limitador | Rutas | Cupo por minuto |
|---|---|---|
| `gateway:api` | Todo `/api/v1` | `API_RATE_LIMIT_PER_MIN` (600) |
| `gateway:auth` | `POST /auth/login`, `/auth/mfa/challenge`, `/auth/forgot-password`, `/auth/reset-password`, `GET /auth/reset-password/policy` y las rutas `strict_limit` de `self_authenticated` (`POST /webmail/session`) | `AUTH_RATE_LIMIT_PER_MIN` (30) |

El cupo es **uno para todas las réplicas**: cuentan en el Redis de la plataforma
(`REDIS_*`, cifrado con `REDIS_TLS` fuera de desarrollo: `docs/Operacion_Despliegue.md`,
1). En memoria de cada proceso, con N réplicas un cliente obtenía N veces su cupo,
también el de autenticación, que es la barrera contra el barrido de cuentas desde una IP
(el bloqueo por cuenta de identity frena el ataque a una sola). El inicio de sesión de la
plataforma y el del webmail comparten el cupo estricto de la IP.

**Algoritmo**: ventana fija de un minuto anclada en la primera petición de cada IP. En
Redis es un script Lua atómico (`INCR`, y `PEXPIRE` solo si la clave no tiene caducidad)
sobre una clave por limitador e identidad, `rl:<limitador>:ip:<ip>`, que caduca sola con
su ventana. El final de la ventana lo marca el TTL de Redis, no el reloj de cada réplica.
Un usuario (`LimitPerUser`) va como `u:<sha256 truncado>` y cualquier identidad que no sea
una IP como `id:<sha256 truncado>`: fuera de la IP, nada llega en claro al almacén. Como toda
ventana fija, en el cruce de dos ventanas admite hasta el doble del cupo; se acepta porque
detrás están el bloqueo por cuenta de identity y el freno por (buzón, IP) de `mail-auth`.

**429**: `Retry-After` con los segundos que faltan para cerrar la ventana, redondeados hacia
arriba y nunca 0, igual decida Redis o la memoria.

**IP**: la que resuelve `CaptureClientIP` (`X-Real-IP` solo si la conexión llega desde
`TRUSTED_PROXY_CIDRS`). Ni `X-Real-IP`, ni `X-Forwarded-For`, ni una cabecera interna
enviada por el cliente cambian la clave (probado en `pkg/middleware` y en el gateway).

**Redis caído**: los dos limitadores caen a memoria con el mismo cupo por réplica. En el
estricto, nunca se deja pasar sin límite, pero tampoco se tumba el inicio de sesión.
La memoria cuenta siempre, también con Redis sano, así que al caer cada réplica sigue desde
lo que ya vio. Cada consulta espera a Redis como mucho 250 ms y, tras un fallo, no se le
vuelve a preguntar durante 5 s. Mientras dura, el cupo efectivo es N veces el configurado;
se ve en `rate_limit_degraded_total{limiter}` (decisiones tomadas en memoria) y en un aviso
del registro, como mucho uno por minuto y limitador, más otro cuando Redis vuelve.

**Los demás servicios** (`contacts`, `automations`, `scheduler`, `analytics`, `suppression`,
`mail-security`, `webmail`) mantienen su limitador en memoria por proceso. Todos están
detrás del gateway, que ya aplica el cupo común. Su límite es una segunda barrera contra el
abuso de quien ya tiene sesión, no la barrera contra la fuerza bruta. El freno de
credenciales del correo (`mail-auth`) ya vive en Redis, y el enlace público del doble
opt-in de `contacts` va firmado.
