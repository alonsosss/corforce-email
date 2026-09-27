# Plan maestro: protección frente a bots, agentes y modelos de lenguaje

Aplica a **todas** las plataformas del grupo: Core Force Mail (esta), el ERP (`admin.core-force.com`) y
las que vengan. Sale de la auditoría de seguridad del 2026-09-27 y de una pregunta concreta: cómo
hacen Adidas y Temu para que un navegador controlado por un agente no pueda usar su web, y qué de
eso vale aquí.

## 0. El principio que ordena todo lo demás

**No existe una señal técnica que identifique a un modelo de lenguaje.** Un modelo llama a través de
un `curl`, de una librería HTTP o de un navegador real; el servidor ve un cliente, no una
inteligencia. Con credenciales válidas, el que llama **es** ese usuario. Todo producto que prometa
"bloquear a las IA" bloquea en realidad una de estas tres cosas, y este plan las trata por separado:

| Qué se bloquea de verdad | Cómo | Límite honesto |
|---|---|---|
| **Rastreadores declarados** (GPTBot, ClaudeBot, Google-Extended, PerplexityBot, CCBot, Bytespider…) | Por su `User-Agent` y por `robots.txt` | Solo a los que se identifican |
| **Comportamiento de máquina**: navegador automatizado, cliente sin JavaScript, huella de red que no es la de un navegador, ritmo inhumano | Detección en el navegador, reto de navegador, huella TLS/HTTP en el borde, límites de tasa | Un agente con navegador real "sigiloso" y ritmo humano pasa |
| **Alcance de una credencial** y **lo que hace con ella** | Mínimo privilegio, step-up, MFA, detección de sondeo, cupos de exportación, auditoría y revocación | No distingue al dueño de su agente: limita el daño y lo delata, no lo impide |

Y tres cosas que **no** haremos, aunque otros las hagan:

* **Contenido falso o vacío a quien parece bot** (el "baneo en la sombra" de Temu). En una plataforma
  de correo y de gestión, darle datos falsos a una sesión legítima mal clasificada es un incidente
  peor que el que evita. Aquí se bloquea con un mensaje claro y una referencia, o no se bloquea.
* **Retos o CAPTCHA delante de las APIs.** Las integraciones (el ERP, los relays) entran con credencial;
  se protegen con alcance, cupos y detección, nunca con un reto que solo resuelve un navegador.
* **Depender de ocultar los endpoints.** El código de la web los lista; cualquiera con acceso los tiene.
  La seguridad está en que cada endpoint compruebe quién, qué y cuánto.

## 1. Modelo de amenaza

| Actor | Qué quiere | Qué tiene | Capas que lo paran |
|---|---|---|---|
| A. Rastreador de entrenamiento | Copiar lo público (landing pages, páginas de citas, formularios) | Nada | 1 |
| B. Agente con navegador, sin credenciales | Explorar la web, probar el login, raspar lo público | Un navegador automatizado o un cliente HTTP | 0, 1, 2 |
| C. Agente o script **con credenciales** (prestadas, robadas o del propio usuario) | Sacar datos de la empresa: contactos, correos, clientes; probar endpoints | Una sesión o una clave de API válidas | 3, 4, 5 |
| D. Volumen: raspado masivo, fuerza bruta, agotamiento | Tirar el servicio o adivinar credenciales | Muchas IP | 0, 3, 5 |

El actor **C es el único que importa de verdad** en estas plataformas: los datos valen más que la
web pública. Por eso la mayor parte del plan va a las capas 3, 4 y 5, aunque las visibles sean las
primeras.

## 2. Arquitectura por capas

Cada capa es independiente: si una falla, las demás siguen. Cada una tiene su fichero de
configuración versionado, sus métricas y su alerta; ninguna lleva umbrales, listas ni dominios en
el código.

### Capa 0. El borde: Cloudflare delante de todo lo HTTP

Es lo más parecido a lo que usan Adidas (Akamai) y Temu (motor propio), sin pagarlo: huella de red
(TLS, HTTP/2), reputación de IP, reto de navegador cuando duda, WAF gestionado y límites de tasa
antes de tocar el servidor.

* **Qué pasa por Cloudflare:** solo HTTP: la consola, el webmail, las páginas públicas y las APIs.
* **Qué no pasa nunca:** SMTP, IMAP, POP3 y Sieve. Cloudflare no transporta correo; esos puertos van
  directos a la IP del servidor como hoy, con el cortafuegos de la celda (`netfilter`).
* **Reglas como código** (Terraform o el API de Cloudflare desde `ops/security/edge/`), con revisión
  en el repositorio y prueba de que cada regla existe antes de desplegar:
  * WAF gestionado (OWASP) en modo bloqueo para todo.
  * Bot Fight Mode (o Super Bot Fight Mode si el plan lo trae): reto a bots probables, bloqueo a
    bots confirmados, **excepción explícita** para `/api/v1/*` con credencial y para los webhooks
    firmados (`/api/v1/public/transactional/ses-events*`), que se protegen con su firma y su cupo.
  * Límites de tasa en el borde por IP: login y recuperación de contraseña (10 por minuto), formularios
    públicos y citas (20 por minuto), resto (el mismo 600 que hoy tiene el gateway).
  * Reto gestionado (no CAPTCHA con imágenes) en las páginas públicas cuando la puntuación de bot es
    baja; Turnstile invisible en el envío de formularios públicos y en la reserva de citas.
  * Cabecera de origen: el servidor solo acepta HTTP desde las redes de Cloudflare (lista que ya
    mantiene `ops/security/edge-cloudflare-ips.sh`) y desde la propia máquina; un cliente que salte el
    borde y ataque la IP directa recibe 403 del proxy de borde.
* **Estado actual:** el ERP ya está proxied (el comodín `*.core-force.com` apunta a Cloudflare); Core
  Force Mail **no**: `email.core-force.com` va directo a `89.58.10.80`. El borde ya sabe exigir Cloudflare
  (`EDGE_REQUIRE_CLOUDFLARE`, hoy `false` en producción, con la lista de rangos y la IP real por
  `CF-Connecting-IP` probadas en `test-selfhosted-profile.sh`). Ponerlo detrás es un registro DNS propio
  proxied (el comodín no sirve), `EDGE_REQUIRE_CLOUDFLARE=true` y `TRUSTED_PROXY_CIDRS` con los rangos.

### Capa 1. Rastreadores declarados

* **Una sola lista** de agentes de rastreo de IA, en `selfhosted/edge/ai-crawlers.txt` (un
  `User-Agent` por línea, con fuente y fecha; los buscadores no van), montada en el proxy de borde
  como `cloudflare-ips.txt`. De ella salen:
  * El `map` de nginx `$edge_bot_ia` (`entrypoint.d/15-bots.sh` lo genera al arrancar, escapando y
    validando cada nombre; una línea rara o una lista vacía impiden arrancar) y el **403** en toda ruta
    del host público, sin llegar al gateway. `robots.txt` es una petición; esto es el bloqueo.
  * `web/public/robots.txt`: un grupo con `Disallow: /` para cada agente de la lista, más
    `Google-Extended` y `Applebot-Extended` (fichas de exclusión del entrenamiento que no son agentes).
    No se genera: una prueba (`web/src/pwa/robots.test.ts`) exige que cada agente de la lista tenga su
    grupo y que ningún buscador esté en la lista, así que las dos no pueden divergir.
* Pruebas: la de la web (arriba) y, en `ops/scaffold/test-selfhosted-profile.sh`, GPTBot recibe 403 en
  una landing page y Googlebot 200 en el borde real.

### Capa 2. El navegador: detectar automatización en el cliente

Es exactamente lo que delató al MCP de Chrome en Adidas y Temu: Chrome pone
`navigator.webdriver = true` cuando lo controla una herramienta, y hay más rastros (variables del
protocolo DevTools, `HeadlessChrome` en el agente, ausencia de plugins y de idiomas, ventana de 0×0,
`permissions.query` incoherente con `Notification.permission`).

* **Dónde corre:** `web/src/security/automation.ts`, un módulo sin dependencias que `main.tsx` ejecuta
  **antes** de montar la aplicación: consola, webmail, login y páginas de citas (están en la misma
  aplicación). Va en el propio bundle, así que la CSP con nonce y `strict-dynamic` no cambia. **No
  corre en las landing pages ni en los formularios incrustados**: se sirven con una CSP sin scripts a
  propósito, y meter JavaScript ahí abriría más de lo que cierra; a esas rutas las cubren las capas 0
  y 1.
* **Qué hace:** puntúa las señales (`webdriver`, agente sin cabeza y rastros de chromedriver o Selenium
  valen 3 y bastan solas; ventana 0×0 vale 2; sin idiomas y Chrome de escritorio sin plugins valen 1 y
  solo suman entre varias; umbral 3). Por encima del umbral no monta la aplicación ni registra el service
  worker: muestra una página de bloqueo con un texto claro, una **referencia** aleatoria de 12 caracteres
  (como el "Reference Error" de Akamai, pero sin codificar nada) y cómo pedir revisión. La referencia,
  la ruta y las señales van a `POST /api/v1/public/security/automation-detected`, que atiende el
  **propio gateway** con el cupo estricto por IP: valida cada campo contra una lista cerrada, cuenta
  (`gateway_automation_detected_total`, `gateway_automation_signals_total{signal}`) y lo escribe en su
  registro con la IP y el agente. No es un evento de auditoría: la detección corre antes del inicio de
  sesión y no hay empresa a la que atribuirlo; cuando el panel de seguridad (fase 4) lo necesite,
  el bloqueo de una sesión ya identificada sí lo será.
* **Qué no hace:** no bloquea a los lectores de pantalla ni a los navegadores viejos: las señales de
  accesibilidad no puntúan, y sin JavaScript la aplicación no funciona de todos modos.
* **Límite honesto:** las herramientas "sigilosas" borran estas señales. Esta capa atrapa la
  automatización corriente (Playwright, Puppeteer, Selenium, el MCP de Chrome, `curl`) y a los agentes
  que usan el navegador del propio usuario con el modo de depuración; no a un atacante que se esfuerce.
  Para eso están las capas 3 a 5.
* Pruebas: unitarias del detector con entornos reales (Chrome, Firefox, Safari y móvil de una persona
  no se bloquean; `webdriver`, `HeadlessChrome` y `$cdc_` sí; las débiles solo entre varias), del
  receptor del gateway (422 a todo lo que no es suyo, nada rechazado cuenta) y de la alerta
  `RafagaDeNavegadoresAutomatizados` (más de 20 en 15 minutos). Comprobado a mano con el MCP de Chrome
  DevTools contra la web compilada: ve la página de bloqueo y no el login. Una prueba automática con
  Playwright en CI queda para la fase 7, con los paquetes reutilizables.

### Capa 3. La credencial: alcance, ritmo y sondeo

Aquí se juega lo importante. Un agente con una sesión o una clave válidas puede hacer **lo que su
dueño puede hacer, ni más ni menos**; el trabajo es que eso sea poco, sea lento si abusa, y se note.

**3.1 Alcance (ya está, se mantiene como regla):** permisos por módulo, recurso y acción con fallo
cerrado; familias de claves de API con rutas disjuntas (envío, aprovisionamiento); step-up con segundo
factor en las acciones críticas; MFA exigido por política; token de acceso de 5 minutos solo en
memoria; auditoría con cadena de hashes.

**3.2 Detección de sondeo (nuevo, la pieza central).** Un humano en la consola no produce decenas de
401, 403 y 404 en pocos minutos; un escáner, sí. El gateway ya cuenta las denegaciones RBAC
(`rbac_denials_total`, alerta `PicoDeDenegacionesRBAC`) pero **no las rutas inexistentes ni las
credenciales rechazadas por identidad**. Se añade en el gateway, reutilizando el limitador compartido
en Redis que ya sirve a los cupos (`pkg/middleware.NewSharedRateLimiter`):

* Un contador por **identidad** de respuestas 401, 403, 404 y 405, en ventanas de 5 minutos. La
  identidad es el **token que presenta la petición** (el de sesión o la clave de API, resumido) y,
  si no trae ninguno, su IP; con token se cuenta **también** por IP, así que renovar el token no
  reinicia el conteo. Es por token y no por usuario porque el guardia corre por delante de la
  autenticación: solo así ve las rutas que no existen, que el enrutador despacha sin pasar por los
  grupos autenticados. Un guardia interno, ya con la sesión verificada, anota la empresa y el
  usuario para el evento.
* Umbrales en el entorno (`PROBE_THRESHOLD_TOKEN`, `PROBE_THRESHOLD_IP`, `PROBE_WINDOW_MIN`,
  `PROBE_BLOCK_MIN`), con valores de partida de 30 y 60 en 5 minutos y bloqueo de 15 minutos, y
  `PROBE_MODE` (`enforce` bloquea; `observe` solo cuenta y registra, para calibrar sin cortar a nadie).
  El conteo y los bloqueos viven en el Redis de la plataforma, comunes a las réplicas, y caen a la
  memoria de cada réplica si no responde: nunca se deja de contar ni se bloquea por error.
* Al llegar al umbral: **bloqueo temporal de esa identidad** con 429 y código `PROBE_DETECTED` (con
  `Retry-After`), métrica `gateway_probe_blocks_total{identity,mode}` y `gateway_probe_rejections_total`,
  registro con la empresa, el usuario o la clave, la IP y la última ruta, y, si la petición traía
  sesión verificada, el evento `gateway.security.probe` (`user.probe_detected`) que `audit` convierte
  en el evento de seguridad `endpoint_probe` de esa empresa, visible en su pantalla de eventos de
  seguridad. El aviso por correo al administrador queda para la fase 4, con el panel.
* Alerta `SondeoDeEndpoints` a plataforma con cada identidad que llega al umbral (dice tipo y modo).
* Excepción explícita y probada: `/api/v1/auth/*` no cuenta (un 401 allí es una contraseña mal
  escrita y ya tiene su cupo estricto y su bitácora). Un 404 de un recurso que existió y se borró
  cuenta como uno más: treinta en cinco minutos no los produce una persona.

**3.3 Límite por credencial, no solo por IP.** Un cliente con una credencial válida detrás de varias
IP sumaba cupos. Toda petición autenticada pasa ahora por `SESSION_RATE_LIMIT_PER_MIN` (300 por
minuto, por usuario de la sesión o por clave de API, compartido entre réplicas en Redis), además del
general por IP (600) y del propio de las claves de API.

**3.4 Revocación desde donde se ve el problema.** El evento de sondeo enlaza con la sesión o la
clave; junto al aviso, un botón "cerrar esta sesión" o "revocar esta clave" (la revocación por
`tokens_valid_from` y la de claves ya existen; falta el enlace desde el evento).

### Capa 4. Los datos: que sacarlos en masa sea una acción, no un efecto

Aunque una credencial pase todo lo anterior, llevarse la base de contactos tiene que ser visible y
limitado.

* **Exportar es un permiso propio** (`<módulo>.<recurso>.export`) con step-up y auditoría, distinto de
  leer. Ya lo es en parte (listas de contactos); se generaliza a todo lo que se pueda descargar.
* **Cupos de exportación por empresa y día** (`EXPORT_DAILY_LIMIT`), con aviso al administrador al
  agotarse.
* **Paginación con tope** en todos los listados (ya: `max_per_page` en cada servicio) y **sin** un
  parámetro que devuelva "todo".
* **Marca de agua** en las exportaciones: el fichero lleva el id de la exportación y quién la pidió
  en sus metadatos, y la exportación queda registrada con su hash. Si una lista aparece fuera, se sabe
  de qué exportación salió.
* Datos sensibles fuera del listado: los listados no llevan cuerpos, variables ni adjuntos (ya es así
  en el transaccional); se mantiene como regla de revisión.

### Capa 5. Ver y responder

* **Panel "Seguridad" por empresa**, junto a las sesiones que ya existen: eventos de automatización
  detectada, sondeos bloqueados, exportaciones, claves probadas; cada uno con su referencia y la acción
  de revocar.
* **Métricas y alertas** de cada capa en Prometheus, con prueba de `promtool` como el resto
  (`make check-alertas`).
* **Procedimiento escrito** en `docs/Operacion_Despliegue.md`: qué hacer ante cada alerta, cómo levantar
  un bloqueo a un cliente legítimo (una referencia, una acción) y cómo añadir un agente a la lista.

## 3. Aplicación por plataforma

### Core Force Mail (este repositorio)

| Capa | Dónde | Estado hoy |
|---|---|---|
| 0 | Registro DNS propio proxied para `email.core-force.com`; reglas en `ops/security/edge/`; `TRUSTED_PROXY_CIDRS` | Directo a la IP; el proxy de borde propio ya limita por IP |
| 1 | `web/public/robots.txt` generado; `map` en `selfhosted/edge-proxy` | `robots.txt` solo permite `/p/` y `/citas/`; sin bloqueo de agentes |
| 2 | `web/src/security/automation.ts`, montado en `main.tsx` y en el shell de las páginas públicas; ruta pública de referencia en `routes.json` | No existe |
| 3.2 | `services/gateway/probe.go` sobre el limitador compartido; evento por outbox; alerta | Solo `rbac_denials_total` |
| 3.3 | `services/gateway/ratelimit.go` | Cupo por clave de API ya; por sesión no |
| 3.4 | Página de sesiones y de claves | Revocación existe; falta el enlace desde el evento |
| 4 | `contacts` (exportar listas), `webmail` (exportar buzón), `analytics` (informes) | Exportar existe en contactos con permiso; sin cupo diario ni marca de agua |
| 5 | `web/src/pages/security/`, `identity.security_events` | Existe la pantalla de eventos de seguridad de identidad; se amplía |

### El ERP (`admin.core-force.com`)

Ya está detrás de Cloudflare, así que la capa 0 es configuración, no obra: WAF gestionado, Bot Fight
Mode con excepción para su API, límites de tasa en login y Turnstile en su formulario de acceso. Las
capas 1 y 2 se copian tal cual (misma lista, mismo módulo de detección, adaptado a su framework). La
capa 3 exige lo mismo que aquí: contador de sondeo por credencial en su puerta de entrada, y que sus
claves tengan alcance por familia. Su plan concreto va en su repositorio; este documento es el
contrato.

**Regla compartida que ya duele:** el token de Cloudflare que renueva los certificados de esta
plataforma es el mismo que usa el ERP. Cada plataforma tiene su propio token, con alcance a su zona y
a lo que necesita (`DNS:Edit` para `acme`; `Zone WAF:Edit` para las reglas del borde), y ninguno se
comparte. Rotar uno no toca al otro.

### Plataformas futuras

Lo reutilizable se empaqueta una vez y se importa:

* `pkg/botshield` (Go): el contador de sondeo, el bloqueo por identidad y la métrica, sobre la
  interfaz `RateLimitStore` que ya existe. Cualquier gateway en Go lo monta con un middleware.
* `web/src/security/automation.ts` publicado como paquete interno para las webs.
* `ops/security/bots/` (lista de agentes, reglas del borde como código, pruebas) como carpeta que se
  sincroniza entre repositorios.
* Este documento como contrato: una plataforma nueva no sale a producción sin las capas 0, 1, 3.2 y
  3.3, que son las baratas y las que paran a los actores B, C y D.

## 4. Datos, contratos y lo que no se hardcodea

* Umbrales, ventanas, duraciones y cupos: variables de entorno documentadas en `.env.example`, con
  mínimo y máximo comprobados al arrancar.
* La lista de agentes: fichero versionado, nunca literales en el código.
* Los dominios y zonas: configuración (`PUBLIC_BASE_URL`, la zona en las reglas del borde); el
  control de copia limpia ya rechaza el dominio literal en el código.
* Eventos: `access.probe.detected`, `access.automation.detected`, `<módulo>.export.requested`, con el
  envelope `events.Event` por outbox, un dueño por subject y consumidores idempotentes.
* Nada de esto guarda credenciales ni sus hashes en los eventos: identidad por id de usuario, id de
  clave o id de sesión.

## 5. Pruebas que tienen que existir antes de dar cada capa por hecha

| Capa | Prueba |
|---|---|
| 1 | Cada agente de la lista recibe 403 en `/` y en `/p/x/y`; `Googlebot` y un Chrome normal reciben 200 |
| 2 | Playwright normal ve la página de bloqueo y el gateway registra la referencia; con las señales borradas ve el login (el límite queda documentado por la propia prueba) |
| 3.2 | Una sesión que pide 30 rutas inexistentes en 5 minutos recibe 429 `PROBE_DETECTED` en la 31, el evento llega a `audit`, el administrador recibe el aviso, y **el ERP con su clave enviando correo válido no se ve afectado** (prueba de no regresión obligatoria) |
| 3.3 | Dos IP con la misma clave comparten el cupo |
| 4 | Exportar sin el permiso propio es 403; con él pero sin step-up es `STEP_UP_REQUIRED`; el fichero lleva la marca; la exportación número `EXPORT_DAILY_LIMIT+1` del día es 429 |
| 5 | Cada alerta nueva tiene su prueba de `promtool` que dispara y que no dispara |
| Todas | `make e2e` recorre un sondeo completo con binarios reales |

## 6. Fases

| Fase | Contenido | Capas | Esfuerzo | Criterio de cierre |
|---|---|---|---|---|
| 1 | Lista de agentes, `robots.txt` generado, bloqueo en el proxy de borde | 1 | Horas | Pruebas de la capa 1 en CI; desplegado |
| 2 | Detector de automatización con página de bloqueo y referencia; ruta de registro | 2 | 1 día | Playwright bloqueado en CI; evento visible en Seguridad |
| 3 | Sondeo por identidad en el gateway, bloqueo temporal, evento, aviso al administrador, alerta; cupo por sesión | 3.2, 3.3 | 2 días | Prueba de no regresión del ERP; alerta con `promtool`; desplegado |
| 4 | Revocación desde el evento; panel Seguridad por empresa | 3.4, 5 | 1 día | Un administrador cierra la sesión sospechosa desde el aviso |
| 5 | Exportación como acción: permiso, step-up, cupo, marca de agua | 4 | 2 días | Pruebas de la capa 4 |
| 6 | Cloudflare delante de la web con reglas como código; token propio por plataforma | 0 | 1 día más pruebas | `email.core-force.com` proxied, ERP con clave enviando, IMAP y SMTP intactos, `TRUSTED_PROXY_CIDRS` correcto |
| 7 | Paquetes reutilizables (`pkg/botshield`, módulo web, carpeta `ops/security/bots`) y aplicación al ERP | Todas | 2 días aquí, más el ERP | El ERP pasa las mismas pruebas |

Las fases 1 a 3 son las que paran a los actores A, B y D y delatan al C; se hacen primero y seguidas.
La 6 es la única con riesgo operativo (cambia por dónde entra el tráfico web) y va con ventana y
vuelta atrás preparada: el registro DNS se despoxiea y todo vuelve a como hoy.

## 7. Riesgos y cómo se acotan

* **Falsos positivos en la capa 2** (un navegador raro, una extensión de accesibilidad): la página de
  bloqueo da la referencia y un correo de contacto; el umbral se calibra con las referencias recibidas
  la primera semana antes de endurecerlo; nunca se bloquea por una sola señal.
* **Bloquear al ERP por sondeo** (una integración con una ruta mal escrita produce 404 en serie): la
  prueba de no regresión, umbrales por familia de credencial, y el aviso al administrador con la ruta
  concreta para corregirla; el bloqueo es temporal y la clave no se revoca sola.
* **Cloudflare delante rompe la IP real**: `TRUSTED_PROXY_CIDRS` con las redes de Cloudflare y la
  prueba de que el gateway ve la IP del cliente y no la del borde (ya existe la de `forwarded.go`).
* **Coste de mantener la lista de agentes**: la lista es corta, cambia poco, y una prueba avisa si un
  agente conocido deja de estar (se compara contra una fuente pública en la CI semanal, como ya se hace
  con mailcow).
* **Sensación de seguridad falsa**: este documento dice en cada capa lo que **no** para. Ninguna capa
  sustituye a las credenciales cortas, al MFA y a la rotación de lo expuesto.

## 8. Cómo se mide

* `gateway_probe_blocks_total` y `gateway_automation_detected_total` por empresa y tipo de identidad:
  cero falsos positivos confirmados tras la calibración, y cada bloqueo real con su evento y su aviso.
* Tiempo entre el primer 404 de un sondeo y el bloqueo: menos de 5 minutos.
* Tiempo entre la alerta y la revocación por el administrador: medible desde el panel.
* Exportaciones: todas con permiso, step-up, registro y marca; ninguna fuera de cupo.

## 9. Estado

| Fecha | Qué |
|---|---|
| 2026-09-27 | Plan escrito tras la auditoría. Capas 3.1 y parte de 4 y 5 ya existen (permisos, step-up, MFA, auditoría, sesiones, eventos de seguridad, cupos por IP y por clave). |
| 2026-09-27 | Fase 1 hecha: lista `selfhosted/edge/ai-crawlers.txt` (43 agentes), mapa y 403 en el borde, `robots.txt` con un grupo por agente y la prueba que los ata. |
| 2026-09-27 | Fase 2 hecha: detector en `web/src/security/`, página de bloqueo con referencia, receptor en el gateway con métricas y alerta. Verificado con el MCP de Chrome DevTools. |
| 2026-09-27 | Fase 3 hecha (3.2 y 3.3): guardia de sondeo en el gateway (`probe.go`), Redis con respaldo en memoria, `PROBE_*`, evento `gateway.security.probe` → `endpoint_probe` en `audit`, alerta `SondeoDeEndpoints`; cupo por credencial `SESSION_RATE_LIMIT_PER_MIN`. Pendiente 3.4 (revocar desde el evento), con el panel de la fase 4. |
