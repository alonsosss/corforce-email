# Puesta en marcha de Amazon SES en produccion

Estado a 2026-09-22. Este documento es el plan y el registro de estado de la salida por SES
(correo transaccional, avisos de la plataforma y marketing). Las reglas permanentes estan en
`docs/Operacion_Despliegue.md` (seccion de despliegue, "Salida por Amazon SES"); aqui solo va lo
que falta y en que orden.

## 1. Punto de partida

### Verificado en AWS (informe de quien administra la cuenta, 2026-09-22)

Region `us-east-1`. El id de la cuenta no se escribe aqui (el repositorio es publico): lo imprimen `setup-ses.sh` y `verificar-ses.sh`.

| Pieza | Estado |
|---|---|
| Identidad `avisos.core-force.com` | Verificada, DKIM de 2048 bits, los tres CNAME en Cloudflare sin proxy |
| MAIL FROM `bounce.avisos.core-force.com` | Verificado, con su MX y SPF en Cloudflare. DMARC se alinea por DKIM y por SPF |
| SPF y DMARC de `avisos.core-force.com` | Los gestiona la plataforma (`domain-service`); no se tocaron |
| Conjunto `core-force-transactional` | Creado a mano, TLS obligatorio, conjunto por defecto del dominio |
| Topic `ses-avisos-core-force-events` | Creado a mano; recibe rebotes, quejas, rechazos y errores de plantilla. **Sin suscriptor** |
| Supresion de la cuenta | Activa para rebotes y quejas |
| Acceso de produccion | **Pendiente**: caso nuevo "SES Production Access - us-east-1" en AWS Support (la solicitud anterior se cerro en julio sin respuesta) |
| Usuario IAM de envio | **No existe**: crear usuarios con claves lo hace quien administra la cuenta |
| Planes de SES, VDM, metricas de reputacion en CloudWatch, IP dedicada | Apagados a proposito (coste) |

### Verificado en el codigo y en el servidor

* `transactional` ya recibe los eventos en `POST /api/v1/public/transactional/ses-events`,
  confirma la suscripcion de SNS, verifica la firma y **solo** acepta el topic de
  `SES_EVENTS_TOPIC_ARN`. En produccion la ruta responde hoy
  `403 events topic not configured` (comprobado el 2026-09-22): esta viva y esperando el ARN.
* Un rebote permanente o una queja dan de alta la direccion en `suppression` antes de guardar el
  evento; si `suppression` no responde, se devuelve 503 y SNS reintenta
  (`services/transactional/internal/app/ingest.go`).
* Cada envio lleva las etiquetas `tenant_id` y `message_id`: por ellas se atribuye cada evento.
* `transactional` no arranca sin dos conjuntos distintos (`SES_CONFIG_SET_TRANSACTIONAL` y
  `SES_CONFIG_SET_MARKETING`). El `.env` espera `cfm-transactional` y `cfm-marketing`.
* El remitente de la plataforma es `no-reply@avisos.core-force.com`, con
  `PLATFORM_TENANT_ID` fijado; lo unico que falta para que salga son las credenciales de SES.
* La pila `ops/aws/ses-mail.yaml` (`ops/aws/setup-ses.sh`) crea los dos conjuntos, un topic
  cifrado con clave KMS propia al que solo publican esos dos conjuntos, y la suscripcion HTTPS.

### Lo que no encaja

1. Lo creado a mano no es lo que el codigo espera: otro nombre de conjunto, ningun conjunto de
   marketing, un topic sin suscriptor y sin la politica que limita quien publica.
2. La politica IAM propuesta en el informe solo cubre el conjunto creado a mano y la identidad
   de avisos: no cubre marketing ni los dominios de las empresas, y concede `SendRawEmail`, que
   el servicio no usa.
3. Con el conjunto por defecto del dominio apuntando al creado a mano, lo que salga sin conjunto
   publica sus eventos en un topic que nadie escucha.
4. Las credenciales locales usan el usuario `core-force-deploy-local`, un resto del ERP: el
   nombre propio es `core-force-mail-deploy-local` (`setup-iam.sh`, y `make clean-copy` no admite
   otro prefijo). Ese usuario no puede leer SES, SNS ni CloudFormation.

## 2. Decision

Se adopta la pila del repositorio y se retira lo creado a mano. Razones: es reproducible y
revisable, crea el conjunto de marketing que el servicio exige, cifra el topic y limita quien
publica en el, y un segundo camino de eventos sin escuchar es exactamente el fallo silencioso
que la plataforma debe evitar. Lo verificado (DKIM, MAIL FROM, registros de Cloudflare) no
depende del conjunto ni del topic y se conserva tal cual.

La politica TLS del transaccional queda en `OPTIONAL` (por defecto de la pila): SES intenta TLS
siempre, y un codigo de acceso o una factura no deben perderse porque el servidor del
destinatario no lo ofrezca. Se cambia con `SES_TRANSACTIONAL_TLS=REQUIRE` si se decide lo
contrario.

## 3. Plan

Cada paso dice quien lo hace. "Codigo" es este repositorio; "Cuenta" es quien tiene
administrador de AWS (CloudShell); "Servidor" es la sesion que despliega.

### Paso 1. Codigo (hecho en este cambio)

* `ops/aws/setup-iam.sh`: usuario de envio `core-force-mail-ses` con la politica `ses-envio`
  (solo `ses:SendEmail`, y desde 3-G tambien `ses:SendRawEmail` para el relay SMTP; cualquier identidad verificada de la cuenta, solo por
  `cfm-transactional` y `cfm-marketing`); la politica `observacion` pasa a leer SES, SNS y la
  pila; la parte del rol de instancia se omite cuando el rol no existe (servidor fuera de EC2).
* `ops/aws/setup-ses.sh`: `SES_DEFAULT_SET_IDENTITIES` apunta el conjunto por defecto de esas
  identidades al transaccional de la pila, y el guion nombra los conjuntos que no gestiona.
* `ops/aws/verificar-ses.sh`: comprobacion de solo lectura de todo lo anterior, con los valores
  `SES_*` que van al servidor.
* `transactional`: un evento autentico de nuestro topic sin las etiquetas del servicio se
  responde con 200 y se ignora (antes, 400 y reintentos de SNS).

### Paso 2. Cuenta: IAM

```bash
ops/aws/setup-iam.sh --check     # revisar
ops/aws/setup-iam.sh             # aplicar
aws iam create-access-key --user-name core-force-mail-ses
```

Las dos claves de `create-access-key` van **directas** al almacen de secretos como
`SES_ACCESS_KEY_ID` y `SES_SECRET_ACCESS_KEY` (`ops/security/secrets/add-secret.sh`, solo para
`transactional` segun `reparto.tsv`). Nunca a un `.env`, a un chat ni a un correo. En el servidor:

```bash
VALOR=... ops/security/secrets/add-secret.sh SES_ACCESS_KEY_ID --apply
VALOR=... ops/security/secrets/add-secret.sh SES_SECRET_ACCESS_KEY --apply
```

Con el almacen en OpenBao (`docs/adr/0011-almacen-de-secretos-openbao.md`) el alta es la misma;
ademas queda version de cada cambio (`python3 ops/security/secrets/openbao.py versiones`) y
registro de cada lectura.

Si ademas se quiere que el operador lea el estado desde su PC, crear el usuario
`core-force-mail-deploy-local` (lo hace el mismo guion), darle claves y retirar
`core-force-deploy-local`.

### Paso 3. Servidor: fijar el topic antes de crear la pila

El ARN del topic es determinista (`cfm-<ambiente>-ses-events`), asi que se fija antes de que
exista: cuando la pila cree la suscripcion, `transactional` ya reconoce el topic y la confirma
en el primer intento.

```text
SES_EVENTS_TOPIC_ARN=arn:aws:sns:us-east-1:<cuenta>:cfm-prod-ses-events
SES_CONFIG_SET_TRANSACTIONAL=cfm-transactional
SES_CONFIG_SET_MARKETING=cfm-marketing
SES_REGION=us-east-1
```

Desplegar la imagen de `transactional` de este cambio con esos valores y recrear el
contenedor. Sin claves de SES todavia no envia nada; solo queda escuchando.

### Paso 4. Cuenta: pila de SES

```bash
export AWS_REGION=us-east-1
export SES_EVENTS_URL=https://email.core-force.com/api/v1/public/transactional/ses-events
export SES_DEFAULT_SET_IDENTITIES=avisos.core-force.com
ops/aws/setup-ses.sh prod --check
ops/aws/setup-ses.sh prod
```

La suscripcion se confirma sola en segundos (`transactional` ya conoce el topic por el paso 3).
Si quedara `PendingConfirmation` (el servicio no estaba arriba, el ARN no coincidia), se corrige
la causa y se pide otra confirmacion con
`aws sns subscribe --topic-arn <arn> --protocol https --notification-endpoint "$SES_EVENTS_URL"`.

Despues, retirar lo creado a mano cuando ninguna identidad lo use como conjunto por defecto:

```bash
aws sesv2 delete-configuration-set --configuration-set-name core-force-transactional
aws sns delete-topic --topic-arn arn:aws:sns:us-east-1:<cuenta>:ses-avisos-core-force-events
```

### Paso 5. Servidor: credenciales

1. Antes de dar las claves: revisar la cola de `transactional`. Lo aceptado sin credenciales se
   reintento hasta `MaxSendAttempts` (8) y deberia estar como fallido; lo que siga pendiente
   saldra en cuanto haya credenciales (en modo de pruebas, SES lo rechaza si el destinatario no
   esta verificado).
2. Las claves del paso 2 en el almacen de secretos y recrear `transactional`.
3. `ops/aws/verificar-ses.sh prod avisos.core-force.com` sin fallos (el modo de pruebas sale como
   aviso) y confirmar que los cuatro valores que imprime coinciden con los del paso 3.

### Paso 6. Prueba de punta a punta en modo de pruebas

SES admite el simulador aunque la cuenta no tenga acceso de produccion. Enviar por la plataforma
(no por la consola: sin las etiquetas el evento se ignora) desde la empresa de plataforma:

| Destinatario | Resultado esperado |
|---|---|
| `success@simulator.amazonses.com` | Mensaje `delivered`, evento `Delivery` |
| `bounce@simulator.amazonses.com` | Evento `Bounce` permanente y alta en `suppression` con `hard_bounce` |
| `complaint@simulator.amazonses.com` | Evento `Complaint` y alta en `suppression` con `complaint` |
| `suppressionlist@simulator.amazonses.com` | Rechazo por la lista de supresion de la cuenta |

Despues, borrar en `suppression` (`DELETE /api/v1/suppression/{id}`) las entradas del simulador para que la prueba se pueda
repetir. Con esto queda demostrado lo que se declaro en el caso de AWS: rebotes y quejas se
procesan solos.

### Paso 7. Al llegar el acceso de produccion

1. `verificar-ses.sh` deja de avisar del modo de pruebas; anotar la cuota asignada y ajustar
   `SES_MAX_SEND_RATE` y `SES_MAX_SEND_RATE_MARKETING` para no superarla.
2. Poner `AUDIT_ANCHOR_RUA` (el ancla externa de auditoria por correo esperaba a SES).
3. Primer envio real a un buzon propio fuera de la plataforma y comprobar cabeceras
   (DKIM `pass`, SPF `pass` con `bounce.avisos.core-force.com`, DMARC `pass`).
4. Si AWS pide algo mas, se contesta en el mismo caso de soporte.

### Paso 8. Dominios de las empresas

Codigo hecho (2026-09-23): `domain-service` da de alta en SES cada dominio de envio al
verificarse, con BYODKIM (la misma clave DKIM y el mismo selector que ya publica el cliente, asi
que no hay registros nuevos de DKIM), MAIL FROM `bounce.<dominio>` y el conjunto transaccional, y
pide el MX y el SPF de ese MAIL FROM. `transactional` solo envia desde un dominio que SES verifico.
Las reglas estan en `docs/Operacion_Despliegue.md` ("Identidades de SES"). Para activarlo en
produccion, quien administra la cuenta:

1. `ops/aws/setup-iam.sh --check` y despues sin `--check`: crea `core-force-mail-ses-identidades`
   con `core-force-mail-ses-identidades`.
2. `aws iam create-access-key --user-name core-force-mail-ses-identidades` directo al almacen
   (`SES_IDENTITIES_ACCESS_KEY_ID`, `SES_IDENTITIES_SECRET_ACCESS_KEY`).
3. Desplegar las migraciones, `domain-service` y `transactional`, en ese orden.

## 4. Costes

Sin plan de SES: 0,10 USD por cada 1000 correos y 0,12 USD por GB de adjuntos. Los eventos por
SNS a HTTPS son practicamente gratis (se publican todos los tipos de eventos, no solo rebotes y
quejas, pero a este volumen sigue siendo despreciable). La clave KMS del topic cuesta 1 USD al
mes mas las peticiones. No se activan los planes nuevos de SES, ni VDM, ni IP dedicada, ni
metricas de reputacion en CloudWatch mientras el volumen no lo justifique.

## 5. Registro de estado

| Paso | Estado |
|---|---|
| 1. Codigo | Hecho |
| 2. IAM | Hecho (2026-09-23): `core-force-mail-ses` con `core-force-mail-ses-envio`; su clave se creo y se guardo en el almacen en un solo paso (nunca paso por disco ni por la conversacion) |
| 3. Topic fijado en el servidor | Hecho (2026-09-23): `SES_EVENTS_TOPIC_ARN` en el `.env` de produccion, `transactional` y `web` en `cd74ee9` |
| 4. Pila de SES | Hecho (2026-09-23): `cfm-prod-ses-mail`, suscripcion confirmada sola, `avisos.core-force.com` con `cfm-transactional` por defecto; retirados `core-force-transactional` y `ses-avisos-core-force-events`. `verificar-ses.sh prod avisos.core-force.com`: todo en orden salvo el aviso del modo de pruebas |
| 5. Credenciales en el servidor | Hecho (2026-09-23): las dos claves en el almacen, `transactional` recreado, `verify-scope contenedores` OK. Tasas a la cuota del modo de pruebas (1 por segundo): `SES_MAX_SEND_RATE=1` y `SES_MAX_SEND_RATE_MARKETING=1` |
| 6. Prueba con el simulador | Hecho (2026-09-23), por `/internal/send-email` desde la empresa de plataforma: `success@` quedo `delivered`, `bounce@` `bounced` y alta en `suppression` con `hard_bounce`, `complaint@` `complained` y alta con `complaint`. Las dos entradas del simulador se dejaron en `suppression` como constancia |
| 7. Acceso de produccion | Esperando a AWS Support. Al llegar: subir `SES_MAX_SEND_RATE` a la cuota asignada, poner `AUDIT_ANCHOR_RUA` y hacer el envio real del paso 7 |
| 8. Dominios de empresa en SES | Codigo hecho (2026-09-23): `domain-service` (identidades BYODKIM, MAIL FROM, estado en `07_ses_identities.sql`) y `transactional` (`sending_ready`, `06_sending_ready.sql`). Falta en produccion: usuario IAM y claves (paso 8) y desplegar |

Queda en la cuenta `my-first-configuration-set`, conjunto por defecto de `core-force.com` y de una
direccion personal. No es de esta plataforma (`core-force.com` lo usa el SES del ERP) y no se
toca: llevarlo a `cfm-transactional` mandaria los eventos del ERP a este topic, donde se
ignorarian por no llevar las etiquetas del servicio.

Cada mensaje muestra dos eventos `send`: el que registra `transactional` al entregarlo a SES
(`source: api`) y el `Send` que publica SES. Las estadisticas cuentan por estado del mensaje,
no por eventos, asi que no se duplica ningun total.
