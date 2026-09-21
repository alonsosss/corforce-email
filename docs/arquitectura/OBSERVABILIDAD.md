# Observabilidad

Como se ve el estado de la plataforma: que se mide, donde se consulta y que avisa.

Antes de esto, un problema en produccion solo se podia diagnosticar leyendo los logs de
cada contenedor por separado. Con mas de cien contenedores en una sola maquina eso no
escala, y hay fallos que los logs no muestran: un pool de conexiones agotado no imprime
errores, imprime silencio y lentitud.

## Que se mide

Cada servicio expone `/metrics` en su propio puerto, en formato Prometheus:

| Metrica | Que dice |
|---|---|
| `http_requests_total{method,route,status}` | Trafico y errores por ruta. La etiqueta `route` es el PATRON (`/loans/{id}/payments`), nunca la URL concreta. |
| `http_request_duration_seconds` | Latencia; permite percentiles por servicio y por ruta. |
| `http_requests_in_flight` | Peticiones simultaneas en curso. |
| `pgx_pool_connections{pool,state}` | Conexiones por base de datos y estado (en uso, libres, total). |
| `pgx_pool_max_connections` | Tope del pool, para leer el uso como porcentaje. |
| `pgx_pool_acquire_waits_total` | Veces que una peticion espero conexion libre: el aviso temprano del agotamiento. |
| `rbac_denials_total{module,action,enforced}` | Escrituras denegadas por el control de acceso (solo en el gateway). |
| `rate_limit_degraded_total{limiter}` | Decisiones de un limitador compartido (`gateway:api`, `gateway:auth`) tomadas en memoria del proceso porque Redis no respondia: mientras crece, cada replica aplica su propio cupo (`docs/arquitectura/CSP-Y-SESION.md`). |
| `cell_routing_failures_total{cell_service,reason}`, `cell_call_failures_total{cell_service,reason}`, `cell_target_refusals_total`, `cell_membership_refusals_total`, `cell_resolution_stale_total` | Enrutado por celda: peticiones que el gateway o domain-service no enviaron a ninguna celda (`cell_service` es el servicio de celda de destino), celdas destino rechazadas, empresas que una instancia rechazo y resoluciones servidas con la ultima celda conocida (`Modelo_de_Datos_y_Celdas.md`, 5.4). Nacen a cero al arrancar. |
| `mail_security_dkim_reconcile_removals_total{reason}`, `mail_security_dkim_reconcile_unresolved_total`, `mail_security_dkim_reconcile_last_success_timestamp_seconds` | Repaso de las claves DKIM de los motores en mail-security: dominios a los que retiro las claves (`not_served`, `tenant_gone`), dominios que conservo porque organization no dio su empresa, e instante de su ultima pasada completa (0 si ninguna desde el arranque). Los contadores nacen a cero. |
| `mail_security_dovecot_revocations_total{action}`, `mail_security_dovecot_revocation_failures_total{reason}` | Revocacion en Dovecot desde mail-security (`deploy/mail/README.md`): buzones cuya credencial retiro por un evento del directorio (`flush`, cache de autenticacion vaciada; `kick`, ademas sesiones cerradas) y fallos que esperan la reentrega del evento (`unreachable`, `rejected`, `command`, `directory`). Nacen a cero. |
| `events_dead_lettered_total{stream,consumer,reason}`, `events_dead_letter_failures_total{stream,consumer,reason}`, `events_dlq_messages` | Eventos que un consumidor durable abandono (`pkg/events`): copiados en `EVENTS_DLQ` (`max_deliveries`, agoto sus 20 entregas; `undecodable`, no es un evento legible), abandonados sin poder copiarlos, y mensajes que guarda `EVENTS_DLQ`. Los contadores nacen a cero al suscribirse cada consumidor; la profundidad se pregunta a JetStream en cada recoleccion y falta si no responde (Eventos, abajo). |
| `outbox_events_exhausted_total` | Eventos de la outbox (`pkg/outbox`) que agotaron sus intentos sin llegar a JetStream y ya no se reintentan (payload ilegible o el bus rechazo cada intento). Es la perdida que antes solo dejaba una linea de registro; avisa `EventosDeOutboxAgotados`. Nace a cero. |
| `go_*`, `process_*` | Memoria, goroutines, arranques del proceso (detecta reinicios en bucle). |

La identidad del servicio **no** viaja dentro de la metrica: la aporta el recolector desde
la definicion del objetivo. Es la convencion de Prometheus y evita que la etiqueta se
duplique como `exported_service`. Una metrica que nombra otro servicio lo hace con una etiqueta
propia: `cell_service` en las de celdas y `auth_service` en `mail_auth_attempts_total` (el
servicio de Dovecot que pregunta, `deploy/mail/README.md`).

## Como queda instrumentado un servicio

Solo por arrancar con el servidor comun. `pkg/server.New` envuelve el router del servicio
con `pkg/observability.WithOps`, que antepone `/healthz` y `/metrics` y mide todo lo demas.
Un microservicio nuevo creado con `make new-service` queda medido sin escribir una linea.

Los tres servicios que no usan `pkg/server` (el gateway y `ecommerce-builder` construyen su
propio `http.Server`) llaman a `observability.WithOps` directamente. Los ocho servicios
Python (FastAPI) exponen `/metrics` con `prometheus-fastapi-instrumentator`; sus series de
trafico llevan la etiqueta `handler` en vez de `route`, asi que los tableros por servicio
funcionan igual y el desglose por ruta tiene forma propia.

Las rutas operativas quedan **fuera** de la cadena de middlewares del servicio: ni el
recolector ni el healthcheck del contenedor tienen token de gateway. Solo son alcanzables
desde la red interna de Docker y desde el loopback del host; el gateway nunca las expone
porque solo reenvia `/api/v1`.

## Healthcheck del contenedor

Las imagenes de los servicios Go son `FROM scratch`: no hay shell ni `curl` con los que
Docker pueda sondearlas. El propio binario sabe sondearse:

```dockerfile
HEALTHCHECK CMD ["/lending", "--healthcheck", "8093"]
```

La comprobacion se resuelve en un `init()` de `pkg/observability`, antes de `main`, para que
el proceso del healthcheck no abra conexiones a la base ni se suscriba a NATS.

## Disponibilidad: `/readyz`

`/healthz` dice que el proceso responde y nada mas: es lo que usa el healthcheck del contenedor, porque reiniciar un
servicio no arregla una base caida. `/readyz` (misma ruta operativa, fuera de la cadena de middlewares y solo alcanzable
desde la red interna) dice si sus dependencias reales estan al alcance: responde 200 `{"status":"ready"}` o 503
`{"status":"unready"}` con `checks` por dependencia (`ok` o `fail`, nunca el texto del error: el de un driver puede llevar
host y usuario). Las dependencias las registran los paquetes que las abren, de modo que todo servicio las tiene sin
cablearlas: `postgres:<pool>` (un `Ping` de cada pool con nombre, el del registro y los de celda; `pkg/db`) y `nats`
(la conexion viva; `pkg/events`). Cada comprobacion tiene 2 s. Los pools por empresa no entran: una empresa con la base
rota no vuelve no disponible al servicio para las demas. Redis no esta registrado (lo usan el limitador y las sesiones, que
degradan por su cuenta; `LimitadorSinRedis` lo avisa).

## El stack

```bash
docker compose -f docker-compose.observability.yml up -d
```

- **Prometheus** (`127.0.0.1:9090`): recolecta y evalua las alertas.
- **Alertmanager** (solo red interna): las agrupa y las entrega por correo ("Entrega de las alertas").
- **Grafana** (`127.0.0.1:3000`): tableros "Plataforma" y "Salud de entrega del correo" (`entrega.json`: cola de
  Postfix, mensaje mas antiguo, falsos positivos del antispam, cuarentena y, desde Loki, entregas, diferidos, rebotes
  y rechazos de Postfix) aprovisionados; `ops/scaffold/check-dashboards.sh` exige que solo consulten metricas que existen.
- **node-exporter**: CPU, memoria y disco del servidor.

Ninguno se publica hacia internet: se consultan por tunel SSH
(`ssh -L 3000:127.0.0.1:3000 deploy@<host>`). `GRAFANA_ADMIN_PASSWORD` es obligatoria y sale
del almacen de secretos.

Va en un compose aparte a proposito: se actualiza o se apaga sin tocar los servicios del
plataforma, y una caida del monitoreo no puede arrastrar al producto. Se une a la red del
despliegue principal como red **externa** (`APP_NETWORK`, por defecto `app_mail-internal`),
de donde salen dos reglas de operacion:

- **Orden de apagado.** El stack de observabilidad se baja primero. Con Prometheus arriba,
  un `docker compose down` del despliegue principal falla al borrar la red porque sigue en
  uso; usa `docker compose stop` o baja antes el monitoreo.
- **Si cambia el nombre del proyecto Compose** (por defecto `app`, del directorio de
  despliegue `/opt/core-force-mail/app`), hay que fijar `APP_NETWORK` acorde o Prometheus no
  encontrara la red.

## Objetivos

La lista de objetivos (`ops/observability/prometheus/targets.json`) se **genera** desde
`docker-compose.yml` con `make gen-observability-targets`, y el CI falla si esta
desactualizada: un microservicio nuevo no puede quedarse sin vigilancia por olvido.

No se descubren los objetivos por el socket de Docker a proposito: montar ese socket en un
contenedor equivale a darle root del host.

**Gotcha del montaje:** el contenedor monta el DIRECTORIO `ops/observability/prometheus`,
no cada fichero. Un bind de fichero apunta a un inodo, y rsync (como casi cualquier editor)
reemplaza el fichero al actualizarlo: el contenedor se quedaria mirando el inodo viejo para
siempre. Con la lista de objetivos eso significaba que un microservicio nuevo **nunca**
entraba al monitoreo, y sin ningun error — simplemente no aparecia.

## Alertas

Las reglas viven en `ops/observability/prometheus/rules/plataforma.yml`, agrupadas por
familia: disponibilidad (servicio caido, reinicios en bucle), version desplegada (imagen
compilada en el servidor), trafico (errores 5xx, latencia), base de datos (pool al limite,
esperas), seguridad (pico de denegaciones RBAC), host (disco, memoria, CPU, robo de CPU, swap),
limites de peticiones (limitador sin
Redis), respaldos (abajo), celdas, claves DKIM, revocacion en Dovecot y eventos (abajo).

Una alerta mal escrita no falla: se queda callada. Las reglas nuevas llevan su prueba de
promtool en `ops/observability/prometheus/tests/<alerta>_test.yml`, que `make check-alertas`
(dentro de `make checks`) ejecuta con la misma imagen de Prometheus que produccion.

### Respaldos

Los trabajos de `ops/backup` no corren en un contenedor, asi que no aparecen en ningun panel: que
dejen de ejecutarse se ve igual que si corrieran. Publican su resultado en un fichero `.prom` que
node-exporter recoge (`ops/backup/report-metric.sh`), y lo que se vigila es la MARCA DE TIEMPO del
ultimo exito, no el codigo de salida: el caso peligroso no es el respaldo que falla -deja un error
en el journal- sino el que dejo de ejecutarse. Por eso cada trabajo tiene dos alertas: la de
antiguedad no puede sonar si la serie desaparece, porque no habria nada que comparar.

| Alerta | Cuando | Espera | Severidad | Por que |
|---|---|---|---|---|
| `RespaldoSinExitoReciente` | `core_force_job_last_success_timestamp_seconds{trabajo=~"respaldo\|respaldo_correo"}` con mas de 36 h | 15 min | critica | El respaldo corre de madrugada: 36 h son dos corridas perdidas, de las bases o de los buzones. |
| `RespaldoFallido` | `core_force_job_last_exit_code != 0` en respaldo, respaldo de correo o verificacion | 10 min | alta | Una base, un volumen, la copia externa o la verificacion fallaron; el journal de la unidad dice cual. |
| `RespaldoNoSeRegistra` | `absent(core_force_job_last_run_timestamp_seconds{trabajo="respaldo"})` | 36 h | critica | El trabajo no llega a ejecutarse: temporizador sin instalar o sin permiso de escribir la metrica. |
| `VerificacionDeRespaldoSinExitoReciente` | ultimo exito de `verificacion_respaldo` con mas de 10 dias | 1 h | alta | Mientras no pase, que los respaldos restauren es una suposicion. |
| `VerificacionDeRespaldoNoSeRegistra` | `absent` de su serie | 10 dias | alta | Igual que la anterior, sin serie que vigilar. |

El respaldo de correo (`respaldo_correo`) no tiene alerta de ausencia: un host sin los motores de
`deploy/mail` no lo ejecuta y sonaria para siempre.

### Celdas

Salvo `EmpresaEnCeldaAjena`, que la emiten las instancias de celda, las emiten el gateway y
domain-service. Las esperas separan lo que es un error de despliegue, que no se corrige solo y
avisa al primer caso, de lo que puede ser un corte breve de organization.

| Alerta | Cuando | Espera | Severidad | Por que |
|---|---|---|---|---|
| `EmpresaEnCeldaAjena` | sube `cell_membership_refusals_total{reason="foreign_tenant"}` | ninguna | alta | Una empresa llego a la instancia de otra celda: enrutado del gateway o URL de un servicio de celda mal desplegados. |
| `CeldaSinInstancia` | sube `not_served` en `cell_routing_failures_total` (gateway) o `cell_call_failures_total` (domain-service) | ninguna | alta | La celda de la empresa no tiene instancia declarada del servicio. Los dos crean al arrancar sus series a cero, una por servicio de celda y motivo que pueden producir, asi que el primer fallo da `increase()`. |
| `MapaDeCeldasDesalineado` | sube `cell_call_failures_total{reason="not_in_cell"}` | ninguna | alta | La instancia elegida por domain-service rechaza a la empresa: su mapa de celdas no casa con el `CELL_CODE` de la instancia. |
| `ResolucionDeCeldaFallida` | `cell_routing_failures_total{reason="unresolved"}` sube en cada ventana de 10 min | 15 min | alta | El gateway sirve la ultima celda conocida hasta una hora despues de caducar: solo fallan las empresas sin cache, y un corte de menos de cinco minutos no avisa. |
| `BarridoDeDominiosSinCelda` | `cell_call_failures_total{reason="unresolved"}` sube en cada ventana de 6 h 30 min | 7 h | media | domain-service llama en barridos (`DOMAIN_RECHECK_INTERVAL`, 6 h) y un barrido fallido se repite en el siguiente: avisa con dos seguidos. La ventana debe superar el intervalo y la espera, la ventana: domain-service no arranca con un intervalo de mas de 6 h (`maxRecheckInterval`), y alargarlo exige alargar las dos. |
| `CeldaDestinoSinSuperadmin` | sube `cell_target_refusals_total{reason="not_operator"}` | ninguna | alta | Ningun cliente legitimo manda `X-Target-Cell` sin el rol superadmin: alguien con una sesion valida tantea el aislamiento entre celdas. |

En estas reglas `service` es quien no pudo llamar (lo pone el recolector) y `cell_service`, a que
servicio de celda.

### Claves DKIM

El repaso de mail-security retira las claves DKIM que la regla ya impide al escribir y con los
eventos del directorio (`deploy/mail/README.md`). Pasa al arrancar y cada
`MAIL_DKIM_RECONCILE_INTERVAL` (15 min por defecto y como maximo), con un plazo de un intervalo por pasada. Nada
de esto corta el correo: las tres son de severidad media.

| Alerta | Cuando | Espera | Severidad | Por que |
|---|---|---|---|---|
| `ClavesDKIMRetiradasPorElRepaso` | sube `mail_security_dkim_reconcile_removals_total` en la ultima hora, por motivo | ninguna | media | Cada retirada es un camino que fallo: `not_served`, un dominio inactivo o fuera del directorio con claves (caida entre dos pasos, evento perdido); `tenant_gone`, un dominio activo de una empresa dada de baja, que sigue recibiendo. Las claves ya se retiraron: hay que investigarlo, no es una emergencia. |
| `RepasoDKIMDetenido` | mas de una hora (cuatro intervalos) sin pasada completa en ninguna replica del servicio; la que no completo ninguna cuenta desde su arranque | 15 min | media | Avisa cuando cuatro pasadas seguidas no terminaron. Mientras dure nadie retira lo que un camino fallido deje en los motores; publicar, retirar y los eventos del directorio siguen. |
| `RepasoDKIMSinOrganization` | sube `mail_security_dkim_reconcile_unresolved_total` en cada ventana de 20 min | 1 h | media | Sin respuesta de organization el repaso conserva las claves de esas empresas. Una pasada asi no avisa; cuatro seguidas si. organization caido ya lo avisa `ServicioCaido`: esta cubre que mail-security no llegue a el. |

Las dos ultimas cuentan con un intervalo de 15 min como mucho: mail-security no arranca con un
`MAIL_DKIM_RECONCILE_INTERVAL` mayor (`maxDKIMReconcileInterval`). Alargarlo exige alargar ese techo
y que el umbral de `RepasoDKIMDetenido` siga siendo cuatro intervalos y su espera uno, y la
ventana de `RepasoDKIMSinOrganization` mas de un intervalo y su espera cuatro.

### Revocacion en Dovecot

Con cada evento `mail.mailbox.*` mail-security vacia la cache de autenticacion de Dovecot del buzon
y, si ya no puede entrar o cambio su credencial, cierra sus sesiones (`deploy/mail/README.md`). Un
fallo deja el evento sin confirmar y JetStream lo reentrega cada 90 s; tras 20 entregas, unos 30
minutos, pasa a `EVENTS_DLQ`.

| Alerta | Cuando | Espera | Severidad | Por que |
|---|---|---|---|---|
| `RevocacionEnDovecotFallida` | sube `mail_security_dovecot_revocation_failures_total` en cada ventana de 5 min, por motivo | 10 min | alta | Un fallo suelto que la reentrega resuelve no avisa; unas siete reentregas fallidas si, antes de que el evento acabe en `EVENTS_DLQ`. Mientras dura, un buzon apagado o con la credencial cambiada conserva sus sesiones y entra con la credencial vieja hasta `auth_cache_ttl` (300 s). `rejected` es configuracion: `DOVEADM_API_KEY` distinta en los dos lados, una orden fuera de `doveadm_allowed_commands` o el certificado. |
| `ColaDePostfixAtascada` | el mensaje mas antiguo sin retener de la cola de Postfix (`mail_security_postfix_queue_oldest_arrival_timestamp_seconds`) lleva mas de 4 horas | 15 min | media | Postfix avisa al remitente del retraso a las 4 horas (`delay_warning_time`): un mensaje asi es un problema de entrega real. La pantalla Cola de correo (superadmin) da el motivo de cada uno: destino caido, lista negra, puerto 25 de salida bloqueado o rechazo del remoto. Los retenidos a mano no cuentan. |
| `GestorDeColaSinRespuesta` | sube `mail_security_postfix_queue_poll_failures_total` en cada ventana de 10 min | 15 min | media | El agente de la cola (8590 del contenedor de Postfix) no responde a `mail-security`: caido, `QUEUE_AGENT_API_KEY` distinta o certificado que no casa. Sin el, `ColaDePostfixAtascada` no ve la cola. Con el gestor desactivado no hay consultas y no avisa. |

### Eventos

Un consumidor durable (`pkg/events`) que no confirma un evento lo recibe otra vez cada 90 s. Tras 20
entregas sin confirmar, unos 30 minutos, o en la primera si el cuerpo no es un evento, lo copia en
`EVENTS_DLQ` (`dlq.<subject>`, con quien lo abandono y por que en cabeceras) y deja de recibirlo. Cada
mensaje abandonado es un efecto de negocio que no se aplico y que nada repite solo: una baja de buzon
que no llego a Dovecot, una clave DKIM sin retirar, una entrada de auditoria sin escribir. Se
reproduce a mano (`docs/Operacion_Despliegue.md`, seccion 10).

`stream` es el stream de origen y `consumer` el consumidor durable, los dos fijados por el codigo al
suscribirse; `reason` es `max_deliveries` o `undecodable`. Un consumidor tiene un solo subject de
filtro, asi que el subject no anade nada, y el concreto de cada mensaje queda en la copia y nunca en
una etiqueta: la cardinalidad la fija el numero de consumidores del codigo, dos motivos por cada uno.
`events_dlq_messages` la publica todo proceso con un consumidor durable, con el mismo valor porque la
cola es una sola: se lee con `max`.

| Alerta | Cuando | Espera | Severidad | Por que |
|---|---|---|---|---|
| `EventosAbandonadosEnDLQ` | sube `events_dead_lettered_total` en la ultima hora, por servicio, stream, consumidor y motivo | ninguna | alta | La reentrega ya dio media hora a los fallos pasajeros: esperar mas solo retrasa la reparacion, y el contador no baja, asi que no hay oscilacion que filtrar. Alta y no critica: es un efecto concreto sin aplicar, no una caida, y la copia se guarda 30 dias. |
| `EventosAbandonadosSinCopia` | sube `events_dead_letter_failures_total` en la ultima hora | ninguna | critica | La copia en `EVENTS_DLQ` fallo en la ultima entrega: el evento solo queda en su stream de origen, que lo borra a los 7 dias, y un JetStream que no escribe amenaza a todo el bus. |
| `DLQConMensajesSinRevisar` | `EVENTS_DLQ` con mensajes | 24 h | media | Cada abandono ya aviso al llegar: esta avisa de la reparacion pendiente, y de un abandono que las anteriores no vieron porque su proceso murio antes de que se recolectara el contador. Los mensajes salen de la cola al reproducirlos o descartarlos, o a los 30 dias. |

### Entrega de las alertas

Una alerta que nadie recibe no sirve. Prometheus evalua las reglas y envia las disparadas a **Alertmanager**
(contenedor `alertmanager` del mismo compose, sin puerto publicado), que las agrupa por `alertname` e `instance`
(espera de 30 s, reenvio cada 4 h mientras dure la causa) y las manda **por correo**, tambien al resolverse. No hay
servicio propio de notificaciones: el `notification` del ERP no se copio y las reglas que lo vigilaban se quitaron.
Las alertas en espera (`pending`) no se envian: son las que suelen resolverse solas dentro de su ventana `for`. La
decision y sus alternativas estan en `docs/adr/0005-entrega-de-alertas-con-alertmanager.md`.

- **Remitente**: un buzon de la propia plataforma (`ALERT_SMTP_USER`, con SMTP permitido) por el submission
  (`ALERT_SMTP_HOST`) con STARTTLS obligatorio y certificado verificado.
- **Destinatario**: `ALERT_EMAIL_TO`. El compose no arranca si falta.
- **Clave SMTP**: un fichero (`ALERT_SMTP_PASS_FILE`, 0644 dentro de un directorio 0700; el contenedor corre como
  nobody). Debe existir antes de levantar Alertmanager: si no, Docker crea un directorio en su lugar. Para rotarla,
  se reescribe el fichero y se reinicia `alertmanager`.
- **Plantilla**: `ops/observability/alertmanager/alertmanager.yml.tmpl` con `render.sh`, que rechaza valores con
  caracteres que no sean de una direccion. `make check-alertas` la renderiza y la valida con `amtool`.

**Limitacion asumida:** con la plataforma caida por completo, Postfix tambien lo esta y el aviso no sale.
`ALERT_EMAIL_TO` debe ser una direccion externa a los dominios de la plataforma para que el aviso llegue a un buzon
que no cae con ella; para la caida total hace falta ademas un canal que no dependa de este servidor (Telegram, un
servicio externo de latido), que requiere credenciales que hoy no hay.

## Lo que todavia no hay

- **Logs centralizados.** Los logs rotan por contenedor (20 MB x 5, comprimidos) y se leen
  con `docker logs`. Agregarlos exige o bien montar el socket de Docker en un recolector
  (privilegio que no compensa) o bien enviarlos a CloudWatch con el driver `awslogs`, lo que
  requiere ampliar la politica del rol de la instancia. Decision pendiente.
- **Trazas distribuidas.** Con `X-Request-ID` ya se puede seguir una peticion entre
  servicios en los logs; no hay OpenTelemetry.
- **Metricas de negocio** (comprobantes emitidos, cobranzas del dia). Hoy se consultan por
  API; llevarlas a series temporales es el paso natural despues de este.
