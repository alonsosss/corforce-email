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

**Access token**: solo en memoria, en `window.__cpSession` (propiedad no enumerable). No
puede ser una variable de módulo: cada micro-frontend carga su propia copia de
`@cp/api-client` y tendría una sesión distinta por remoto. Tras recargar, la sesión se
restaura pidiendo un token nuevo con la cookie.

**Modo cookie es opt-in** (`cookie_auth: true` o cabecera `X-Auth-Mode: cookie`): los
clientes que no son navegador —extensión de reuniones, escáner de escritorio— siguen
recibiendo el refresh token en el JSON.

`AUTH_COOKIE_SECURE` controla el atributo `Secure` y su valor por defecto es **true**. No
atarlo a `ENVIRONMENT`: el despliegue real corre con `ENVIRONMENT=development` y la cookie
habría salido sin `Secure` sin que nadie lo notara.

### Al tocar el cliente HTTP hay que reconstruir los 21 micro-frontends

Cada `fe-*` empaqueta su copia de `@cp/api-client`. Uno solo con código viejo (que aún lea
`localStorage`) recibe 401 en todas sus llamadas. Reconstruir solo el shell deja media
aplicación sin sesión.

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
repetida termina bloqueando algo sin explicación.

### Los scripts que Cloudflare inyecta en el borde

Cloudflare inserta un script inline para su detección de bots (`challenge-platform`, JS
Detections). Cuando la política declara un nonce, Cloudflare **marca con él** sus scripts
inyectados, así que se ejecutan sin que la política tenga que admitir ningún otro inline.

Antes del nonce ese script quedaba bloqueado: no era un problema de seguridad (la CSP
hacía su trabajo), pero Cloudflare perdía una señal antibot y, el día que se activara un
Managed Challenge, el desafío habría fallado para usuarios legítimos.

Un hash en la CSP no sirve: el script cambia entre cargas.
