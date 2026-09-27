# Plan: registro de envíos por empresa

Cada empresa ve, en su panel, qué correo salió, a quién, en qué estado quedó y, si no se entregó,
el motivo exacto que dio el servidor de destino. Hay dos caminos de salida y cada uno tiene su
registro:

| Camino | Qué lo recorre | Dónde queda | Estado |
|---|---|---|---|
| Transaccional | API, relay SMTP del ERP, campañas, avisos de la plataforma | `transactional.messages` y sus eventos de SES (base de la empresa) | El API ya existía; falta la pantalla |
| Corporativo | Webmail, Outlook o cualquier cliente con usuario de buzón, y el correo que entra a los buzones | Nada por mensaje: solo los registros de Postfix | Todo por hacer |

## 1. Pantalla "Envíos"

Una pantalla con dos pestañas, bajo "Envío":

* **Transaccional**: `GET /api/v1/transactional/messages` (estado, destinatario, remitente y
  fechas), detalle con `GET /messages/{id}` y la línea de tiempo con `GET /messages/{id}/events`,
  donde está el motivo del rebote o de la queja que devuelve SES. Permiso
  `transactional.messages.read`, que ya existe.
* **Corporativo**: el registro nuevo de la sección 2, permiso `mail_security.delivery_log.read`.

## 2. Registro de entregas del correo corporativo

**Origen.** Postfix ya escribe cada línea de su registro en JSON en Redis (`syslog-ng`, lista
`POSTFIX_MAILLOG`), pero esa lista es para mirar: se recorta a `LOG_LINES` y nadie la consume, así
que perder líneas es normal en ella. Se añade un destino más, la lista `POSTFIX_DELIVERY_LOG`, con
las mismas líneas y un solo lector.

**Lector.** `mail-security` (plano de la celda, el mismo Redis de los motores):

* Mueve cada línea con `BLMOVE` a una lista de trabajo, la procesa y solo entonces la borra. Si el
  servicio cae a mitad, al volver empieza por lo que quedó en la lista de trabajo. Si la base no
  responde, la línea se queda y se reintenta con espera creciente.
* Correlaciona por id de cola de Postfix: `client=` (usuario autenticado), `message-id=` y `from=`
  se guardan en un hash de Redis por id de cola, con caducidad de 7 días (más que la vida máxima de
  un mensaje en cola), y cada línea `to=... status=` produce un evento.
* También registra los rechazos: `milter-reject` (el antispam rechazó el mensaje) y
  `NOQUEUE: reject` (Postfix no lo aceptó), con su motivo.
* La empresa sale del dominio: el del remitente para el correo saliente (`outbound`) y el del
  destinatario para el entrante (`inbound`), con `mail.v_routing_domains` y los dominios alias. Un
  correo entre dos empresas de la celda queda en las dos, cada una con su dirección.
* Idempotente: cada evento lleva una huella de la línea original y la empresa; una línea repetida
  no crea una segunda fila.

**Dónde se guarda.** `mail_security.delivery_events` en la base de la celda, con `tenant_id`, RLS
para `mail_app` y `service_all` para `mail_service`, como la cuarentena. No guarda asuntos ni
cuerpos: solo el sobre (remitente, destinatario), el estado, el código DSN, el servidor que
respondió y su respuesta.

**Retención.** `MAIL_DELIVERY_LOG_RETENTION_DAYS` (90 por defecto), podado cada hora.

**Cota.** Si el lector no corre, la lista crece: el propio `syslog-ng` hace `LPUSH` y `LTRIM` en un
solo `EVAL` con el tope `delivery_log_max_lines` (200.000), así que Redis nunca se queda sin memoria
por esto y solo cambia un motor (Postfix). La métrica `mail_security_delivery_log_backlog_lines` y la
alerta `RegistroDeEntregasAtrasado` avisan del retraso antes de llegar al tope.

**Atribución.** Un envío saliente solo se atribuye con usuario autenticado (`sasl_username`): el
remitente del sobre se puede falsificar y no basta. El entrante se atribuye por el dominio del
destinatario: lo entregado en un buzón de la celda, lo reenviado por un transporte (un dominio en
convivencia con su proveedor anterior, cuyo rebote tiene que verse) y lo rechazado a la entrada.

**Una sola réplica lee**, con cerrojo de líder renovado cada minuto: dos lectores se repartirían la
lista de trabajo y desordenarían las líneas de un mismo mensaje.

**API.** `GET /api/v1/mail-security/delivery-log` con filtros de sentido (`direction`), estado,
dirección de correo exacta (`address`, remitente o destinatario) y fechas, paginado (50, hasta 200),
bajo RLS. `mail_app` solo lee la tabla: una empresa no puede borrar su rastro de envíos.

**Menú.** "Envíos" está en el grupo Envío con el módulo `transactional`; cada pestaña exige su
permiso. Una sesión con el registro corporativo pero sin el módulo transaccional no ve la entrada
del menú (hoy el `tenant_admin` tiene los dos).

## 3. Fases

| Fase | Contenido | Estado |
|---|---|---|
| 1 | Pantalla "Envíos", pestaña transaccional | Hecho (2026-09-26) |
| 2 | Registro de entregas corporativo (motor, lector, tabla, API) y su pestaña | Hecho (2026-09-26) |
