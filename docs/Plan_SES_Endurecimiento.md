# Endurecimiento de la salida por SES

Estado a 2026-09-23. Sigue a `docs/Plan_SES_Produccion.md` (puesta en marcha, cerrada salvo el
acceso de produccion) y sale de la auditoria del mismo dia. Cada fase dice que se cambia, por que,
como se prueba y que queda fuera. La seccion 8 es el registro de estado.

## 1. Lo que encontro la auditoria

| # | Hallazgo | Gravedad real |
|---|---|---|
| 1 | La suscripcion SNS usa la politica por defecto: 3 reintentos separados 20 s y ninguna cola para lo que falla. Un corte de algo mas de un minuto (despliegue, reinicio) pierde los eventos de ese rato | Media. La reputacion de la cuenta no corre peligro: la lista de supresion **de la cuenta** esta activa, SES no vuelve a entregar a una direccion suprimida y esos envios no cuentan en su tasa de rebotes; el siguiente envio a esa direccion devuelve un rebote `OnAccountSuppressionList` que si llega. Lo que se pierde es exactitud: la lista de la empresa tarda en enterarse y quedan mensajes en `sent` sin su `delivery` |
| 2 | Ninguna alerta mira la salida: ni CloudWatch ni Prometheus. `transactional` no publica metricas propias | Alta. Una pausa de la cuenta, un rechazo sistematico o eventos que dejan de llegar pasan en silencio; un correo de recuperacion de contrasena falla sin que nadie lo vea |
| 3 | La ruta publica de eventos pasa por el limitador general por IP del gateway (600/min) | Media, crece con el volumen: con marketing, SNS entrega desde pocas IP y recibiria 429 |
| 3b | El verificador de SNS acepta el certificado de cualquier region y cualquier nombre `SimpleNotificationService-*.pem`, y solo guarda en cache los aciertos | Media (seguridad): cualquiera que conozca el ARN del topic fuerza una descarga saliente por peticion cambiando el nombre |
| 4 | Los dominios de las empresas no se dan de alta en SES | Alta para el producto: solo envia `avisos.core-force.com` |
| 5 | Marketing reescribe los enlaces a `awstrack.me` | Baja hoy (no hay marketing en marcha); afecta entregabilidad y marca |
| 6 | Usuario IAM `core-force-deploy-local` con clave activa (resto del ERP) | Baja; hay que confirmar si el ERP lo usa antes de retirarlo |
| 7 | Sesion de la cuenta raiz abierta en la PC del operador | Baja; se cierra al terminar |
| 8 | `avisos.core-force.com` sin MX: una respuesta a un aviso se pierde | Baja; decision de producto |

## 2. Fase A: que ningun evento se pierda por un corte

### A1. Politica de entrega de la suscripcion (`ops/aws/ses-mail.yaml`)

SNS admite para HTTPS hasta 3600 s de reintentos en total (limite duro). Se usa casi entero:

| Fase | Reintentos | Espera | Tiempo |
|---|---|---|---|
| Previa | 3 | 10 s | 30 s |
| Exponencial | 10 | de 10 s a 300 s | unos 930 s |
| Posterior | 7 | 300 s | 2100 s |
| **Total** | **20** | | **unos 51 min** |

Y `throttlePolicy.maxReceivesPerSecond` (parametro `EventsMaxReceivesPerSecond`, 50 por defecto):
SNS no entrega mas rapido de lo que `transactional` absorbe; lo que excede espera en SNS en vez de
convertirse en errores. Sube con la cuota de SES sin tocar codigo.

Se descarta una cola SQS de mensajes fallidos: seria infraestructura nueva de AWS (el proyecto solo
conserva SES), y la supresion de la cuenta ya cubre la reputacion. La alerta de la fase B avisa si
los eventos dejan de llegar mientras hay envios.

### A2. Limitador propio para los webhooks de proveedores (`gateway`)

Nueva marca en `routes.json`: `"limit": "webhook"` en una ruta publica. Esas rutas salen del
limitador general y pasan por uno propio por IP, `WEBHOOK_RATE_LIMIT_PER_MIN` (6000 por defecto,
de 60 a 600000), compartido entre replicas por Redis como los demas. No se quita el limite: la ruta
sigue siendo anonima. El gateway se niega a arrancar si la marca tiene otro valor o va en una ruta
que no es publica.

### A3. Verificador de SNS

* El certificado solo se acepta de la region del topic fijado (`SES_EVENTS_TOPIC_ARN`): con el topic
  en `us-east-1`, un `SigningCertURL` de otra region se rechaza sin descargar nada.
* Las descargas fallidas se recuerdan 5 minutos por URL y la cache es acotada (64 entradas): nombres
  inventados no fuerzan salidas de red en bucle ni hacen crecer la memoria.

## 3. Fase B: ver la salida

### B1. Metricas de `transactional` (`internal/adapters/prometheus`, como `reputation`)

Etiquetas de conjuntos cerrados, nunca datos de fuera:

* `transactional_send_attempts_total{class, result}`, por intento (tambien los reintentos): `sent`,
  `transient`, `permanent`, `throttled`, `paused`.
* `transactional_ses_events_total{type}`: `send`, `delivery`, `bounce`, `complaint`, `reject`, ...
* `transactional_ses_events_rejected_total{reason}`: `topic`, `signature`, `unreadable`, `untagged`,
  `tenant_mismatch`.
* Estado de la cuenta, leido cada 5 minutos (`SES_ACCOUNT_MONITOR_INTERVAL`; 0 lo apaga) por un
  vigilante en cada replica, sin cerrojo: son dos lecturas baratas y las alertas toman el maximo:
  `transactional_ses_account_sending_enabled`, `_production_access`, `_max_24h_send`,
  `_sent_last_24h`, `_max_send_rate`, y `transactional_ses_reputation_{bounce,complaint}_rate`
  (metricas `Reputation.BounceRate` y `Reputation.ComplaintRate` de CloudWatch, que SES publica sin
  coste). `transactional_ses_account_last_success_timestamp_seconds` dice si el vigilante funciona.

El vigilante necesita `ses:GetAccount` y `cloudwatch:GetMetricData` (sin recurso acotable). Se
anaden a la politica del usuario de envio en `setup-iam.sh`, en una sentencia aparte de solo lectura.

### B2. Alertas (`ops/observability/prometheus/rules/`)

| Alerta | Condicion | Por que |
|---|---|---|
| `EnvioSESPausado` | `sending_enabled == 0` 5 min | La cuenta no envia nada |
| `RebotesSESAltos` / `RebotesSESCriticos` | tasa >= 2 % / >= 4 % | AWS revisa la cuenta al 5 % y la pausa al 10 % |
| `QuejasSESAltas` / `QuejasSESCriticas` | tasa >= 0,05 % / >= 0,08 % | AWS revisa al 0,1 % y pausa al 0,5 % |
| `CuotaSESCasiAgotada` | enviados 24 h >= 80 % de la cuota | Lo que no cabe se reintenta y llega tarde |
| `EnviosSESFallando` | permanentes o transitorios > 10 % de los intentos en 15 min | Rechazo sistematico (identidad, credencial) |
| `EventosSESSinLlegar` | hubo envios en 2 h y ningun evento en 2 h | Suscripcion caida o endpoint inalcanzable |
| `EventosSESRechazados` | rechazos de firma o de topic sostenidos | Ataque o topic mal fijado |
| `VigilanteSESSinDatos` | ultimo exito hace mas de 20 min | Credencial o permiso roto |

Se descarta una alarma de CloudWatch aparte: exigiria otro canal de avisos en AWS, y si el servidor
esta caido tampoco se envia nada.

## 4. Fase C: los dominios de las empresas en SES

`domain-service` ya genera y custodia la clave DKIM de cada dominio (RSA 2048, selector `cfm<fecha>`)
y pide publicarla. SES admite firmar con una clave propia (BYODKIM), asi que:

1. Al verificarse un dominio `sending` o `both`, `domain-service` crea la identidad en SES
   (`CreateEmailIdentity` con `DkimSigningAttributes`: la misma clave y el mismo selector),
   configura MAIL FROM `bounce.<dominio>` y el conjunto por defecto `cfm-transactional`.
2. Pide, ademas, el MX y el SPF de `bounce.<dominio>`; con el proveedor DNS automatico los publica.
3. El barrido consulta la identidad hasta que SES la da por verificada; solo entonces el dominio es
   apto para enviar (`transactional.sending_domains`).
4. Rotacion de la clave: `PutEmailIdentityDkimSigningAttributes` con la nueva. Baja del dominio o paso
   a `corporate`: `DeleteEmailIdentity`.

Credencial propia (`core-force-mail-ses-identidades`), no la de envio: `ses:CreateEmailIdentity`,
`GetEmailIdentity`, `DeleteEmailIdentity`, `PutEmailIdentityDkimSigningAttributes`,
`PutEmailIdentityMailFromAttributes`, `PutEmailIdentityConfigurationSetAttributes`. Migracion aditiva
en `migrations/tenant/canonical/domain-service/` con el estado de SES del dominio. Nunca se da de alta
un dominio de la plataforma que no sea de la empresa de plataforma (regla ya existente).

## 5. Fase D: dominio de seguimiento propio

`clics.core-force.com` en el borde (`edge-proxy`) con certificado propio, reenviando a
`r.us-east-1.awstrack.me`, y `SES_TRACKING_DOMAIN=clics.core-force.com` en la pila (HTTPS obligatorio).
Solo afecta a marketing.

**En curso (2026-09-23):** el bloque del borde esta hecho (`selfhosted/edge/templates/tracking.conf.template`,
activado por `EDGE_TRACKING_HOST`; reenvia con el `Host` del visitante, sin su IP ni sus cookies, verifica el
certificado de AWS y solo admite GET y HEAD), probado contra `r.us-east-1.awstrack.me`. Queda en el servidor:
DNS, `ADDITIONAL_SAN`, verificar el subdominio en SES y `SES_TRACKING_DOMAIN`. Motivo del aplazamiento inicial: El certificado del borde lo emite el `acme` de
`deploy/mail`, el mismo que usan Postfix y Dovecot: anadir el nombre obliga a reemitirlo y a
desplegar los motores uno a uno con `make e2e-mail`. Es un riesgo sobre el correo corporativo para
una funcion que hoy no se usa (no hay campanas). Se hace al activar el marketing, en este orden:
registro A `clics` en Cloudflare sin proxy; el nombre en el `ADDITIONAL_SAN` de `acme`; un bloque
`server` en `selfhosted/edge/templates` activado por `EDGE_TRACKING_HOST` (vacio lo desactiva)
que reenvia con `proxy_ssl_server_name on` y `Host r.<region>.awstrack.me`; y
`SES_TRACKING_DOMAIN` en `setup-ses.sh`. Hecho el 2026-09-24: todo el orden aplicado y el conjunto
`cfm-marketing` reescribe los enlaces a `clics.core-force.com` con HTTPS obligatorio.

## 6. Fase E: limpieza

* `core-force-deploy-local`: mirar su ultimo uso (`get-access-key-last-used`). Si solo lo usa esta PC,
  darle clave a `core-force-mail-deploy-local`, cambiar el perfil local y retirar la vieja; si lo usa el
  ERP, se queda y se documenta.
  **Resultado (2026-09-23):** es el usuario de despliegue del ERP (publica en sus repositorios
  `core-force/*` de ECR y abre sesiones SSM; ultimo uso el mismo dia). **No se retira.** Esta
  plataforma deja de usarlo: `core-force-mail-deploy-local` tiene clave propia en el perfil local
  `core-force-mail`, y con el `verificar-ses.sh` ya funciona sin la cuenta raiz.
* Cerrar la sesion de la cuenta raiz (`aws logout --profile secrets`) al terminar las fases que la
  necesitan (A1, B1 y C tocan la cuenta).
* `avisos.core-force.com`: decision del usuario (ver seccion 7).

## 7. Decisiones que no son tecnicas

* Respuestas a los avisos: dejar `no-reply@` (hoy) o poner `Reply-To` a un buzon de soporte.
* Cuando llegue el acceso de produccion: la cuota que asigne AWS fija `SES_MAX_SEND_RATE` y el
  reparto con `SES_MAX_SEND_RATE_MARKETING`.

## 8. Registro de estado

| Fase | Estado |
|---|---|
| A1 Politica de entrega | Hecho y desplegado (2026-09-23): aplicada en la pila de prod; `EffectiveDeliveryPolicy` con 20 reintentos y 50 entregas por segundo, suscripcion confirmada |
| A2 Limitador de webhooks | Hecho (2026-09-23): `"limit": "webhook"` y `WEBHOOK_RATE_LIMIT_PER_MIN`, con pruebas |
| A3 Verificador de SNS | Hecho (2026-09-23): region del topic y cache de fallos acotada, con pruebas |
| B1 Metricas y vigilante | Hecho (2026-09-23): puerto `Metrics`, adaptador Prometheus, vigilante con GetAccount y CloudWatch; politica `ses-envio` ampliada y aplicada |
| B2 Alertas | Hecho (2026-09-23): grupo `salida-ses` con 10 alertas y sus pruebas de promtool; cargado en el Prometheus de produccion y recogiendo las metricas de `transactional` |
| C Dominios de empresa en SES | Hecho y desplegado (2026-09-23, `5674f2d`): migraciones aplicadas en las dos empresas, usuario `core-force-mail-ses-identidades` con su clave en OpenBao, `domain-service` adopto y etiqueto `avisos.core-force.com` (conserva su DKIM, `sending_ready` verdadero); `core-force.com` del ERP intacto. Simulador tras el despliegue: `success@` entregado, `bounce@` rechazado por la lista de supresion |
| D Dominio de seguimiento | Aplazada hasta activar marketing (seccion 5) |
| E Limpieza | Hecho salvo cerrar la sesion raiz (al terminar C) y la decision de respuestas (seccion 7) |
