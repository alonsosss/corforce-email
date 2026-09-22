# ADR 0009: visor de registros sobre Loki y lectura del antispam, solo para el superadmin

Estado: aceptado (2026-09-21). Cierra C5 del `Plan_Estrategico_Mejoras_Correo.md` (seccion 5.2 y
fila C5 de la seccion 10).

## Contexto

mailcow ofrece al operador dos cosas que aqui faltaban: leer los registros de los motores sin entrar
al servidor y ver la interfaz de Rspamd (cuanto analizo, con que veredicto, que aprendio). La pila de
observabilidad ya corre en produccion (`docker-compose.observability.yml`: Prometheus, Grafana, Loki,
Alertmanager, promtail) en la misma red Docker que la plataforma, y `mail-security` ya habla con el
controller de Rspamd para entrenar el clasificador desde la cuarentena (`/learnspam`, `/learnham`, con
`RSPAMD_CONTROLLER_PASSWORD`).

Las dos capacidades son privilegiadas: los registros de Postfix llevan remitente y destinatario de
cada mensaje de todas las empresas de la celda, y el historial de Rspamd lleva ademas el asunto. El
plan pedia no hacerlo a la ligera: un ADR con las alternativas y una revision de seguridad.

## Decision

### 1. Visor de registros: servicio nuevo `observability` en el plano de control

Un servicio pequeno con el molde hexagonal (`services/observability`), sin base de datos ni bus,
que consulta Loki en nombre del superadmin. Vive en el plano de control porque los registros son de
toda la maquina (motores de correo y servicios Go de todos los planos), no de una empresa ni de una
celda; el gateway lo enruta con el prefijo `observability` y el modulo de permisos `observability`.
No va en el gateway (es un router y no aloja logica) ni en `organization` (empresas y celdas: la
consulta de registros es otro dominio, y `audit` estaba en obra en paralelo). Un servicio propio
tambien deja sitio a lo que viene despues del mismo tipo (consultas a Prometheus para un panel en
la web) sin mezclarlo con nada.

Contrato, `GET /api/v1/observability/logs`:

| Parametro | Regla |
|---|---|
| `service` | Uno de la lista blanca fija de `internal/domain/logs.go` (nombres de servicio de compose que promtail pone en la etiqueta `servicio`: los motores de `deploy/mail/docker-compose.mail.yml` y los servicios Go de `docker-compose.yml`). Comparacion exacta. `GET /logs/services` la sirve a la web, que nunca la conoce de antemano. Una prueba la contrasta con los dos ficheros de compose: un servicio Go nuevo que no entre rompe `go test`. |
| `q` | Texto libre, opcional: UTF-8 valido, sin saltos de linea ni caracteres de control, 200 runas como mucho. Se convierte en UN filtro de linea literal `\|= "<texto>"`, escapado con `strconv.Quote`: la gramatica de las cadenas de LogQL es la de Go (Loki las decodifica con `strutil.Unquote`), de modo que comillas y barras quedan escapadas y ninguna secuencia del usuario cierra la cadena ni anade otra etapa. La consulta se construye entera en Go a partir de estos campos; nunca se acepta LogQL del cliente. |
| `since`, `until` | RFC 3339. Por defecto la ultima hora hasta ahora; ventana maxima de 24 h; `until` no puede estar en el futuro (5 min de tolerancia de reloj). |
| `limit` | 1 a 500 (por defecto 100). |
| `direction` | `backward` (por defecto) o `forward`. |

Contra Loki: `GET /loki/api/v1/query_range` con plazo de 10 s, sin reintentos y respuesta leida
como mucho hasta 4 MiB (mas, se rechaza). Se devuelven las lineas con su instante y las etiquetas
del flujo (`contenedor`, `plano`, `flujo`), ordenadas por este servicio segun la direccion y
recortadas al limite.

Control de acceso, tres capas: el gateway gatea el modulo; el handler exige el rol `superadmin`
(`RequireRoles`) y el permiso de plataforma `observability/logs/read` (`037_observability_permissions.sql`,
alcance `platform`: ningun rol de empresa lo recibe); el caso de uso vuelve a exigir el operador
(`ErrPlatformOnly`). Limite de 30 consultas por minuto y usuario (`LimitPerUser`, en memoria: el
servicio no usa Redis y corre con una replica) por debajo del limite general por IP del servicio.

Cada consulta deja una linea de registro con quien la hizo, el servicio, la ventana, el limite, la
direccion, si llevaba filtro y cuantas lineas devolvio, y una metrica
`observability_log_queries_total{service,outcome}`. **Nunca el texto buscado**: lo normal es buscar
una direccion de correo de un cliente, y anotarla convertiria el registro del visor en un segundo
registro de datos personales fuera de la retencion de Loki. Las pruebas del caso de uso lo verifican.

Configuracion: `LOKI_URL` (`config.ServiceURL`). Vacia, el servicio arranca igual y el visor responde
`503 NOT_CONFIGURED`; el perfil autoalojado la fija a `http://loki:3100`. Loki nunca publica puerto:
solo se alcanza desde la red compartida.

### 2. Lectura del antispam: dos rutas de plataforma en `mail-security`

`mail-security` ya tiene la contrasena del controller y el precedente exacto de "capacidad
privilegiada de toda la celda, solo superadmin, sin contenido de mensajes": el gestor de la cola de
Postfix (`queue_usecase.go`, `033`). Se anaden dos rutas de plataforma, las dos `GET`:

* `GET /api/v1/mail-security/rspamd/stats`: `GET /stat` del controller. Mensajes analizados,
  aprendidos, spam y ham, veredictos, conexiones, ficheros del clasificador bayesiano, hashes fuzzy y
  un resumen de los tiempos de analisis (media y maximo en ms).
* `GET /api/v1/mail-security/rspamd/history?limit=`: `GET /history` del controller (`history_redis`,
  1000 filas), de la mas reciente a la mas antigua, 50 por defecto y 200 como mucho. Cada fila lleva
  el sobre (remitente, destinatarios, IP, usuario SASL), el asunto (recortado a 200 runas), la
  puntuacion y el umbral, el veredicto, los simbolos con su peso, el tamano y el tiempo de analisis.
  **Nunca el cuerpo** (el controller tampoco lo guarda) ni las opciones de los simbolos (llevan URLs
  y fragmentos del mensaje y disparan el tamano).

Se muestran el asunto y las direcciones: son datos de toda la celda que el superadmin ya ve en la
cola de Postfix (remitente y destinatarios) y en la cuarentena (asunto y mensaje entero), y sin ellos
el historial no sirve para lo que existe (saber que mensaje recibio que veredicto y por que).
Permiso nuevo `mail_security/rspamd/read` (`036_mail_security_rspamd_permissions.sql`, alcance
`platform`); el caso de uso vuelve a exigir el operador; cada lectura queda en el registro con
quien la hizo. La contrasena viaja solo en la cabecera `Password`, nunca en la URL, y ningun error la
repite; sin `RSPAMD_CONTROLLER_PASSWORD` las rutas responden `503 NOT_CONFIGURED` y el servicio
arranca igual. No existe ninguna ruta de escritura ni de configuracion del controller
(`/saveactions`, `/savesymbols`, `/scan`, `/learn*` fuera de los que la cuarentena ya usaba):
el adaptador solo expone `LearnSpam`, `LearnHam`, `Stats` e `History`.

### 3. Web

Dos pantallas de plataforma para el rol `superadmin`, sin modulo de menu (como la cola):
`/platform/logs` (selector de servicio alimentado por el API, texto, ventana, orden y limite;
resultados en monoespaciado con instante, pintados como texto: nada se interpreta como HTML) y
`/platform/rspamd` (pestanas de estadisticas e historial).

## Alternativas descartadas

* **Proxy transparente a la interfaz de Rspamd** (mailcow enruta `/rspamd/` a `rspamd:11334` tras
  su sesion PHP). Descartado: expone el controller entero, incluidas las rutas que cambian pesos,
  acciones y mapas y las que entrenan, con la contrasena del controller como unica barrera y bajo la
  sesion de la plataforma. Aqui solo hay dos lecturas con contrato propio y acotado.
* **Grafana embebido (iframe) como visor de registros.** Descartado: otra autenticacion (la de
  Grafana, que hoy solo se alcanza por tunel SSH y no publica hacia internet), otra superficie con
  sus propias vulnerabilidades y su propia gestion de usuarios, y el "Explore" de Grafana admite
  LogQL libre.
* **Aceptar LogQL del cliente** (o un subconjunto "seguro" por lista negra). Descartado: LogQL tiene
  etapas de parseo (`| json`, `| logfmt`, `| line_format`) con las que un filtro de texto se
  convierte en una consulta arbitraria sobre cualquier flujo, y una lista negra de operadores es una
  carrera perdida. Un unico filtro literal construido en Go, con el selector de flujo tomado de una
  lista blanca fija, es lo unico que se necesita para lo que el visor existe.
* **Poner el visor en el gateway o en `organization`.** Descartado: el gateway es un router y no
  aloja logica de negocio; `organization` es empresas y celdas, y mezclar ahi un cliente de Loki
  acopla dos dominios sin relacion.

## Proteccion de datos

* Las dos capacidades son de **toda la celda** (los registros, de toda la maquina), como la cola de
  Postfix: solo el superadmin, que ya ve la cola y la cuarentena de la celda. Ningun rol de empresa
  recibe los permisos (alcance `platform`).
* **Nunca se devuelve el cuerpo de un mensaje.** Los registros de Postfix, Dovecot y Rspamd no lo
  llevan; el historial del controller tampoco lo guarda. Revision de los servicios Go (2026-09-21,
  al disenar este ADR, buscando en `services/` y `pkg/` todo campo de `zap` que pudiera llevar un
  cuerpo: `ByteString`, `body`, `raw`, `content`, `html`, `text`, `message`): ningun servicio
  registra cuerpos de mensajes; lo que se escribe en `zap` son identificadores, direcciones, estados
  y errores. El webmail y la cuarentena registran el id del mensaje, no su contenido. La unica
  excepcion es `pkg/events` (`logUnmarshalFailure`): los primeros 48 bytes de un mensaje del bus que
  no decodifica como evento, que nunca es un correo. Si algun dia un servicio registrara cuerpos, el
  visor los mostraria: la regla "sin cuerpos en los registros" es una condicion de este ADR.
* El texto buscado no se registra ni se mide (arriba). Las lineas devueltas si pueden llevar
  direcciones de correo de clientes: es la razon de que la ventana, el limite y el cupo por usuario
  esten acotados y de que cada consulta quede a nombre de quien la hizo.
* Los registros en Loki tienen la retencion de `ops/observability/loki/loki.yml`; el visor no
  guarda nada.

## Consecuencias

* Servicio nuevo con sus touchpoints: `routes.json`, `docker-compose.yml` (sin base ni bus),
  `docker-compose.selfhosted.yml` (`LOKI_URL` por defecto), `docker-compose.images*.yml`,
  `targets.json` (generado), `reparto.tsv` (solo `INTERNAL_GATEWAY_TOKEN`), `test-ratio-floors.txt`.
* Dos migraciones del registro (`036`, `037`), idempotentes y solo aditivas.
* Para que la lectura de Rspamd funcione en produccion hace falta la contrasena del controller en los
  dos lados: `worker-controller-password.inc` de Rspamd (`password = "<hash de rspamadm pw>";`,
  fichero ignorado por git en `deploy/mail/rspamd/override.d/`) y `RSPAMD_CONTROLLER_PASSWORD` en
  `mail-security`. Hoy esa variable se lee del `.env` (asi venia); moverla al almacen de secretos es
  una mejora aparte (`Operacion_Despliegue.md`).
* `make e2e` comprueba la lista blanca, el 503 sin Loki y el 403 del administrador de empresa;
  `make e2e-mail` lee estadisticas e historial reales tras los envios de la prueba, con la contrasena
  del controller generada en cada ejecucion. Las dos secciones estan escritas y pendientes de
  ejecutar por quien opera (P).

## Pruebas

* `services/observability/internal/domain`: casos de inyeccion (comillas, barras, `}`, `|=`,
  saltos de linea, unicode, selector completo) que demuestran que el LogQL resultante sigue siendo
  un selector fijo mas un unico literal que decodifica al texto original; ventana, limite y
  direccion; lista blanca contrastada con los dos ficheros de compose.
* `services/observability/internal/adapters/loki`: Loki falso (`httptest`) que captura la consulta
  exacta y sus parametros; 5xx y corte como indisponibilidad, 4xx y respuesta ilegible o mayor que
  4 MiB como rechazo.
* `services/observability/internal/app` y `main_test.go`: solo superadmin (403 a `tenant_admin` y
  sin rol), 503 sin Loki, validacion antes de consultar, registro sin el texto buscado, metricas por
  desenlace, cupo por usuario.
* `services/mail-security/internal/adapters/rspamd`: controller falso que comprueba la contrasena en
  la cabecera y nunca en la URL ni en los errores, 401/403 como `NOT_CONFIGURED`, respuesta acotada,
  asunto recortado, destinatarios como lista o como cadena, simbolos sin opciones.
* `services/mail-security/internal/app` y `main_test.go`: solo el operador, orden y tope del
  historial, rutas de plataforma con celda destino (15 en total).
* `web/`: vitest de las dos pantallas y del cliente del API.
