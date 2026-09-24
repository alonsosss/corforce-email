# ADR 0014: Asistente del webmail con la API de Claude

## Estado

Aceptado (2026-09-24). Bloque C5 (G13) de `docs/Plan_Webmail_Innovador.md`, decision 9.

## Contexto

La fase 2 del webmail ofrece a quien dirige una empresa pequena o mediana un asistente que le resuma un hilo, le proponga
una respuesta, cambie el tono de un borrador y saque de un mensaje las tareas y las citas con fecha. Es la unica funcion
del webmail que envia contenido de correo fuera de la plataforma: el texto lo procesa un modelo de lenguaje de un tercero.
El resto de la plataforma se autoadministra en Netcup y no hay modelo propio que pueda servir esto con calidad; montar uno
exigiria GPU e infraestructura que el proyecto no tiene ni justifica.

## Decision

1. **Proveedor: Anthropic, API de mensajes de Claude** (`POST https://api.anthropic.com/v1/messages`, version
   `2023-06-01`), por HTTPS directo desde `services/webmail` (`internal/adapters/anthropic`), sin SDK: el cliente es una
   sola llamada y no anade dependencias al modulo. Modelo por defecto `claude-haiku-4-5` (rapido y economico) para resumir y
   extraer; la redaccion (respuesta y tono) puede ir a otro modelo con `WEBMAIL_ASSISTANT_DRAFT_MODEL` (por ejemplo
   `claude-sonnet-5`). Tope de tokens de salida por peticion (`WEBMAIL_ASSISTANT_MAX_OUTPUT_TOKENS`), reintentos con espera
   exponencial y `retry-after` en 429, 5xx y 529, sin seguir redirecciones (la clave va en una cabecera). La extraccion pide
   salida estructurada con esquema JSON (`output_config.format`) y el webmail la valida y la sanea antes de devolverla.
2. **Clave solo en el almacen de secretos** (`ANTHROPIC_API_KEY?` en `ops/security/secrets/secret-keys.txt`, entregada solo
   al contenedor `webmail` segun `reparto.tsv`). Es opcional: sin ella el webmail arranca y el asistente no se ofrece.
3. **Apagado por defecto y activable por empresa.** El interruptor vive en la celda, `mail.assistant_settings`
   (`migrations/cell/canonical/mail-directory/15_ai_settings.sql`), porque quien lo consulta en cada uso es el webmail, un
   servicio de celda sin base propia que ya habla con `mail-directory`; la base de la empresa obligaria a un servicio de
   empresa en el camino de cada peticion del webmail. Lo cambia el `tenant_admin` desde el panel (Buzones, tarjeta del
   asistente) con los permisos `mailboxes/assistant_settings/read` y `update` (`migrations/registry/045`), tras un aviso de
   tratamiento de datos que debe confirmar; la fila guarda quien lo cambio y cuando se acepto el aviso (`enabled_at`). El
   webmail lo lee por `GET /internal/mail-directory/assistant?username=` en cada uso, con una cache de
   `WEBMAIL_ASSISTANT_SETTINGS_TTL` (30 s por defecto); la empresa la resuelve el directorio a partir del buzon, nunca la
   elige el cliente. Si el directorio no responde, el asistente no se sirve.
4. **Datos que salen.** Solo lo que el usuario pide procesar en ese momento: del mensaje (o de los mensajes del hilo), el
   nombre visible del remitente (o su direccion si no hay nombre), la fecha, el asunto y el cuerpo en texto plano, sin
   caracteres de control y acotado a `WEBMAIL_ASSISTANT_MAX_INPUT_CHARS` por peticion (los mensajes de un hilo se reparten el
   tope y pierden las lineas citadas). Para cambiar el tono, el borrador que escribio el propio usuario; para proponer una
   respuesta, sus indicaciones (tope propio) y su nombre visible. Nunca adjuntos, destinatarios, copias ni otras cabeceras.
   El webmail lee el mensaje del buzon sin marcarlo como leido: el cliente solo manda carpeta y UID, no el contenido.
5. **Retencion: ninguna por nuestra parte.** El texto va y vuelve en la misma peticion; no se guarda en base, Redis, ni
   registros. Queda un apunte de auditoria por uso en el rastro de la empresa (evento `webmail.assistant.used`, que `audit`
   recoge con `AUDIT_SUBJECTS`): empresa, buzon, accion, resultado, modelo, caracteres enviados y devueltos, tokens y numero
   de mensajes, sin contenido. Metricas: `webmail_assistant_requests_total{action,outcome}`,
   `webmail_assistant_tokens_total{model,kind}` (el coste es tokens por la tarifa vigente del proveedor, que no se fija en
   codigo), `webmail_assistant_provider_seconds` y `webmail_assistant_audit_failures_total`. Lo que el proveedor retiene lo
   rigen sus condiciones comerciales de la API; ver "Base legal".
6. **Topes diarios servidos**: peticiones por buzon (`WEBMAIL_ASSISTANT_MAILBOX_DAILY_LIMIT`) y por empresa
   (`WEBMAIL_ASSISTANT_TENANT_DAILY_LIMIT`) y dia UTC, contados en Redis con un script atomico. La interfaz los recibe de
   `GET /api/v1/webmail/assistant` junto con los demas topes; no los copia. Una entrada invalida no gasta cupo.
7. **Defensa contra la inyeccion de instrucciones.** El correo viaja como dato entre marcas (`<correo>`, `<borrador>`,
   `<indicaciones_del_usuario>`) y cualquier aparicion de esas marcas dentro del texto se neutraliza para que un correo no
   pueda cerrar su bloque. Las instrucciones del sistema ordenan tratar lo delimitado como dato, no seguir ordenes que
   aparezcan en el y no afirmar acciones. La salida es solo texto propuesto: el webmail nunca envia, mueve, borra ni crea nada
   con ella. El usuario lo copia, lo inserta en su redaccion o, en la extraccion, confirma cada cita, que se crea con la API
   de calendario de siempre (`POST /api/v1/webmail/calendar/events`).

## Base legal y aviso

El tratamiento lo decide la empresa cliente como responsable: el `tenant_admin` lo activa despues de leer el aviso, que
explica que el texto que cada usuario pida procesar se envia a Anthropic para generar la respuesta y que la plataforma no lo
guarda. Anthropic actua como encargado del tratamiento bajo sus condiciones comerciales de la API; antes de ofrecerlo a una
empresa en produccion, el responsable del producto debe tener firmado el acuerdo de tratamiento de datos (DPA) del proveedor
y revisar su politica de retencion de la API (por defecto no entrena con datos de la API y conserva las entradas un tiempo
limitado para seguridad) y, si alguna empresa lo exige, solicitar retencion cero. El webmail muestra un aviso breve junto a
cada resultado: el texto se ha procesado con un proveedor externo y hay que revisarlo antes de usarlo.

## Como apagarlo

- Una empresa: su `tenant_admin` desactiva el interruptor en el panel; el webmail lo nota en menos de
  `WEBMAIL_ASSISTANT_SETTINGS_TTL`.
- Toda la plataforma: retirar `ANTHROPIC_API_KEY` del almacen (`ops/security/secrets/remove-secret.sh`) y redesplegar el
  webmail. Sin clave el asistente responde `ASSISTANT_NOT_CONFIGURED` y la interfaz no lo ofrece.
- Contener el gasto sin apagarlo: bajar los topes diarios o `WEBMAIL_ASSISTANT_MAX_OUTPUT_TOKENS`.

## Consecuencias

- Una dependencia externa nueva en el camino de una funcion opcional; su caida solo afecta al asistente
  (`ASSISTANT_BUSY` con `Retry-After`), nunca al correo.
- El apunte de auditoria sale por NATS sin outbox (el webmail no tiene base): si NATS o el stream `WEBMAIL` no responden,
  el uso no se bloquea y se cuenta en `webmail_assistant_audit_failures_total`, que debe vigilarse.
- `audit` declara el stream `WEBMAIL` para `webmail.assistant.used` al arrancar con el subject en `AUDIT_SUBJECTS` (esta en
  el valor por defecto y en `.env.example`; un entorno con `AUDIT_SUBJECTS` propio debe anadirlo).
