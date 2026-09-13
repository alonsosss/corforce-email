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
| `go_*`, `process_*` | Memoria, goroutines, arranques del proceso (detecta reinicios en bucle). |

La identidad del servicio **no** viaja dentro de la metrica: la aporta el recolector desde
la definicion del objetivo. Es la convencion de Prometheus y evita que la etiqueta se
duplique como `exported_service`.

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

## El stack

```bash
docker compose -f docker-compose.observability.yml up -d
```

- **Prometheus** (`127.0.0.1:9090`): recolecta y evalua las alertas.
- **Grafana** (`127.0.0.1:3000`): tablero "Plataforma" aprovisionado.
- **node-exporter**: CPU, memoria y disco del servidor.

Ninguno se publica hacia internet: se consultan por tunel SSH
(`ssh -L 3000:127.0.0.1:3000 deploy@<host>`). `GRAFANA_ADMIN_PASSWORD` es obligatoria y sale
del almacen de secretos.

Va en un compose aparte a proposito: se actualiza o se apaga sin tocar los servicios del
plataforma, y una caida del monitoreo no puede arrastrar al producto. Se une a la red del
despliegue principal como red **externa** (`APP_NETWORK`, por defecto `mail_mail-internal`),
de donde salen dos reglas de operacion:

- **Orden de apagado.** El stack de observabilidad se baja primero. Con Prometheus arriba,
  un `docker compose down` del despliegue principal falla al borrar la red porque sigue en
  uso; usa `docker compose stop` o baja antes el monitoreo.
- **Si cambia el nombre del proyecto Compose** (por defecto `app`, del directorio de
  despliegue `/opt/core-force/app`), hay que fijar `APP_NETWORK` acorde o Prometheus no
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

Las reglas viven en `ops/observability/prometheus/rules/plataforma.yml` y cubren cinco
familias: disponibilidad (servicio caido, reinicios en bucle), trafico (errores 5xx,
latencia), base de datos (pool al limite, esperas), seguridad (pico de denegaciones RBAC) y
host (disco, memoria, CPU).

### Entrega de las alertas

Una alerta que nadie recibe no sirve. Las alertas disparadas llegan al **centro de
notificaciones de la propia plataforma**, a los usuarios del tenant de plataforma (los que operan el
producto). El vigia vive en el servicio `notification`
(`internal/app/platform_alerts.go`) y se enciende con `PROMETHEUS_URL` +
`PLATFORM_TENANT_ID`; sin ambas queda apagado, que es lo que ocurre en desarrollo.

Decisiones de diseno:

- **Se consulta a Prometheus, no se recibe un webhook.** No abre ningun endpoint nuevo, no
  exige compartir un secreto con el sistema de monitoreo y deja la direccion de la confianza
  donde debe estar: la plataforma pregunta, nadie escribe en ella desde fuera.
- **Se avisa una sola vez por problema.** Una alerta sigue disparada mientras dure la causa y
  el vigia mira cada minuto; la huella (etiquetas + instante en que empezo) se guarda en
  `notification.platform_alerts` y actua de candado. Un problema que se resuelve y vuelve
  trae huella nueva y si avisa otra vez.
- **Las alertas en espera (`pending`) se ignoran**: son justo las que suelen resolverse
  solas dentro de su ventana `for`.

**Limitacion asumida:** el aviso viaja por la propia plataforma, asi que una caida total se
lleva por delante al mensajero. Cubre lo que ocurre a diario (un servicio caido, un disco al
limite, un pool agotado, un pico de denegaciones) mientras el resto sigue en pie. Para la
caida total hace falta un canal externo (correo por SES SMTP, Telegram), que requiere
credenciales que la plataforma todavia no tiene; el punto de contacto se configura entonces
en Grafana sin tocar codigo.

## Lo que todavia no hay

- **Logs centralizados.** Los logs rotan por contenedor (20 MB x 5, comprimidos) y se leen
  con `docker logs`. Agregarlos exige o bien montar el socket de Docker en un recolector
  (privilegio que no compensa) o bien enviarlos a CloudWatch con el driver `awslogs`, lo que
  requiere ampliar la politica del rol de la instancia. Decision pendiente.
- **Trazas distribuidas.** Con `X-Request-ID` ya se puede seguir una peticion entre
  servicios en los logs; no hay OpenTelemetry.
- **Metricas de negocio** (comprobantes emitidos, cobranzas del dia). Hoy se consultan por
  API; llevarlas a series temporales es el paso natural despues de este.
