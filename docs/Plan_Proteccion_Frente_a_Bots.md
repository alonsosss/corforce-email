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
  Force Mail **no**: `email.core-force.com` va directo a `89.58.10.80`. Ponerlo detrás exige un registro
  DNS propio proxied (el comodín no sirve) y repasar `TRUSTED_PROXY_CIDRS` para que la IP real llegue
  al gateway.

### Capa 1. Rastreadores declarados

* **Una sola lista** de agentes de rastreo de IA, en `ops/security/bots/ai-crawlers.txt` (un
  `User-Agent` por línea, con fecha y fuente), versionada en este repositorio y copiada a los demás
  con `make sync-bot-list`. De ella se generan:
  * `robots.txt` de cada plataforma: `Disallow: /` para cada agente de la lista, y lo que ya dice hoy
    (`Allow: /p/`, `Allow: /citas/`) para los buscadores.
  * Un `map` de nginx en el proxy de borde que responde **403** a esos agentes en toda ruta, sin llegar
    al gateway. `robots.txt` es una petición; esto es el bloqueo.
* Prueba en CI: cada agente de la lista recibe 403 en la raíz y en `/p/`, y `Googlebot` no.

### Capa 2. El navegador: detectar automatización en el cliente

Es exactamente lo que delató al MCP de Chrome en Adidas y Temu: Chrome pone
`navigator.webdriver = true` cuando lo controla una herramienta, y hay más rastros (variables del
protocolo DevTools, `HeadlessChrome` en el agente, ausencia de plugins y de idiomas, ventana de 0×0,
`permissions.query` incoherente con `Notification.permission`).

* **Dónde corre:** un módulo pequeño, sin dependencias, que se ejecuta **antes** de montar la aplicación
  en la consola, el webmail, el login y las páginas públicas (landing, citas, formularios). Como la CSP
  usa nonce y `strict-dynamic`, va como módulo del propio bundle (no como script inline), así no toca
  la política.
* **Qué hace:** calcula una puntuación con las señales anteriores. Por encima del umbral no monta la
  aplicación: muestra una página de bloqueo con un texto claro, una **referencia** (hash corto del
  instante, la IP y la ruta, como el "Reference Error" de Akamai) y cómo pedir revisión. La referencia se
  envía al gateway (`POST /api/v1/public/security/automation-detected`, con su propio cupo) para que
  quede en el registro de eventos de seguridad y en las métricas.
* **Qué no hace:** no bloquea a los lectores de pantalla ni a los navegadores viejos: las señales de
  accesibilidad no puntúan, y sin JavaScript la aplicación no funciona de todos modos.
* **Límite honesto:** las herramientas "sigilosas" borran estas señales. Esta capa atrapa la
  automatización corriente (Playwright, Puppeteer, Selenium, el MCP de Chrome, `curl`) y a los agentes
  que usan el navegador del propio usuario con el modo de depuración; no a un atacante que se esfuerce.
  Para eso están las capas 3 a 5.
* Prueba en CI: un Playwright normal contra la web ve la página de bloqueo; el mismo Playwright con las
  señales borradas ve el login (documenta el límite en vez de fingir que no existe).

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

* Un contador por **identidad** (usuario, clave de API o sesión; y por IP cuando no hay identidad) de
  respuestas 401, 403, 404 y 405, en ventanas de 5 minutos.
* Umbrales en el entorno (`PROBE_THRESHOLD_USER`, `PROBE_THRESHOLD_API_KEY`, `PROBE_THRESHOLD_IP`,
  `PROBE_BLOCK_DURATION`), con valores de partida de 30, 30 y 60 en 5 minutos y bloqueo de 15 minutos.
* Al superar el umbral: **bloqueo temporal de esa identidad** con 429 y código
  `PROBE_DETECTED` (a la IP sin identidad, 429 igual), métrica `gateway_probe_blocks_total{identity_kind}`,
  evento `access.probe.detected` por la outbox del gateway hacia `audit` y hacia el registro de
  eventos de seguridad de `identity`, y **aviso al administrador de la empresa** por el correo del sistema
  (con la referencia, la ruta y la hora, nunca la credencial).
* Alerta `SondeoDeEndpoints` a plataforma cuando hay bloqueos sostenidos o de varias empresas a la vez
  (barrido generalizado).
* Excepciones explícitas y probadas: las rutas que devuelven 404 por diseño (un recurso que ya no
  existe, una página pública sin publicar) no cuentan; cuentan las rutas que **no existen en la tabla**
  del gateway y los métodos no admitidos.

**3.3 Límite por credencial, no solo por IP.** Hoy un cliente con una clave válida detrás de varias
IP suma cupos. El limitador compartido ya sabe contar por clave (`AllowKey`): se aplica a las claves
de API y a las sesiones con un cupo propio (`API_KEY_RATE_LIMIT_PER_MIN`, hoy solo para claves; y
`SESSION_RATE_LIMIT_PER_MIN`, nuevo).

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
| 2026-09-27 | Plan escrito tras la auditoría. Capas 3.1 y parte de 4 y 5 ya existen (permisos, step-up, MFA, auditoría, sesiones, eventos de seguridad, cupos por IP y por clave). Nada de las fases 1 a 7 empezado. |
