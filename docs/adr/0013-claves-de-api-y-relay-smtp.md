# ADR 0013: Claves de API de empresa y relay SMTP de envio

## Estado

Aceptado (2026-09-23). Oleada 3-G de `docs/Plan_Marketing_Avanzado.md`. Operacion en `docs/Operacion_Despliegue.md`,
«Claves de API y relay SMTP»; modelo de acceso en `docs/Usuarios_Roles_y_Acceso.md` 4.1.

## Contexto

Hasta aqui una empresa solo podia enviar correo transaccional desde la web o desde otro servicio de la plataforma: el
API publico exige el JWT de una persona, que dura 5 minutos y vive en memoria del navegador. Una integracion (un sistema de gestion, una
tienda, un CRM) necesita una credencial de maquina, duradera, revocable y acotada a enviar, y muchas solo saben hablar
SMTP con usuario y contrasena.

Las restricciones del proyecto acotan la solucion: Netcup bloquea el puerto 25 de salida y el transaccional y el marketing
salen solo por la API de SES; Postfix es el correo corporativo y no se parchea ni comparte reputacion con el transaccional;
toda direccion pasa por `suppression` antes de encolarse; `transactional` es el unico que habla con SES.

## Decision

1. **Claves de API en access-control**, no en identity. access-control custodia los permisos, sabe si la cuenta del
   creador sigue activa (`identity.v_user_status`), que roles tiene y que modulos tiene contratados la empresa: con eso
   calcula en cada resolucion el alcance efectivo de la clave (el suyo acotado a lo que su creador tiene hoy), sin que otro
   servicio tenga que repetir la regla. identity gestiona sesiones de personas, y una clave no es una persona.
2. **Formato y almacenamiento.** `cfm_<prefijo>_<secreto>`: prefijo de 12 caracteres base32 (60 bits, unico en la
   plataforma, publico: es el identificador de la lista y el usuario SMTP) y secreto de 256 bits aleatorios. El secreto se
   muestra una sola vez y se guarda como **HMAC-SHA256 con una llave del almacen** (`API_KEY_HASH_KEY`, anillo con
   `API_KEY_HASH_KEYS_OLD` de `pkg/crypto.MACKeyRing`, y el prefijo dentro de lo firmado). No argon2id: el secreto no es
   una contrasena elegida por alguien, no hay diccionario que frenar, y un hash lento en cada resolucion (API y SMTP) seria
   una palanca de denegacion de servicio; la llave del almacen hace que una copia de la tabla no sirva para probar
   secretos fuera de linea. Una clave firmada con una llave retirada se vuelve a firmar con la activa en su siguiente uso.
3. **Alcance.** Permisos exactos del catalogo marcados `api_key_grantable` (hoy `transactional/messages/create` y `read`,
   migracion `044`), nunca uno que el creador no tenga. Caducidad opcional, revocacion, ultimo uso y alta y revocacion
   auditadas por la outbox del registro (`access.api_key.created`, `access.api_key.revoked`).
4. **El gateway acepta la clave solo en una lista cerrada** declarada en `routes.json` (`api_key_routes`, validada al
   arrancar) y la resuelve contra access-control por `pkg/apikey` con cache corta. La peticion sigue como una de sesion pero
   sin usuario ni roles: empresa y alcance en cabeceras que solo escribe el gateway, RBAC por el alcance en cualquier modo, y
   `pkg/authz` decide solo con ese alcance. La revocacion es inmediata: access-control deja una marca en Redis que el
   gateway y el relay consultan en cada uso de su cache.
5. **Servicio nuevo `smtp-relay`**, en Go con `github.com/emersion/go-smtp` (MIT, la misma familia que `go-imap` y
   `go-message`, ya en uso), sin base de datos: STARTTLS obligatorio antes de AUTH en 2525 y TLS implicito en 2465, AUTH
   PLAIN y LOGIN con la clave, lectura del MIME con `pkg/rawmail` (compartido con transactional), ClamAV para todo mensaje
   con adjuntos, y entrega a una ruta interna nueva de transactional (`POST /internal/transactional/raw-messages`) que aplica
   las mismas reglas que su API y envia el MIME a SES con SendEmail contenido Raw. SES autoriza ese SendEmail como
   `ses:SendRawEmail` (comprobado: con solo `ses:SendEmail` se rechaza con `not authorized to perform 'ses:SendRawEmail'`,
   aunque la Service Authorization Reference de SES v2 solo nombra `ses:SendEmail`), asi que la politica `ses-envio` de
   `ops/aws/setup-iam.sh` concede las dos acciones sobre las mismas identidades y los mismos conjuntos.

## Alternativas descartadas

* **Postfix de la celda como relay de envio.** Mezclaria el transaccional de las empresas con el correo corporativo
  (reputacion, colas, IP de salida), obligaria a sacar ese correo por SMTP hacia SES o hacia internet (el 25 esta bloqueado
  y el proyecto prohibe el SMTP directo a SES), y la supresion, la reputacion y la facturacion quedarian fuera del camino,
  en un motor que no se parchea. La autenticacion de Postfix es la de los buzones, no la de una integracion de empresa.
* **Dar a las empresas credenciales SMTP de SES.** Prohibido por `CLAUDE.md`: el trafico sale por la API, nunca por SMTP
  directo. Ademas saltaria la supresion, la reputacion por empresa, la atribucion de eventos y la facturacion, y una
  credencial SMTP de SES es una credencial IAM de la cuenta de la plataforma en manos de un tercero.
* **Claves en identity.** Duplicaria en identity la politica efectiva y los modulos contratados, que viven en
  access-control, o le haria preguntarlos en cada resolucion.
* **argon2id o bcrypt para el secreto.** Ver la decision 2.
* **Aceptar la clave en cualquier ruta con sesion.** Una clave filtrada daria acceso a todo lo que el creador puede hacer;
  la lista cerrada la limita a enviar y a leer el estado de lo enviado.

## Superficie publica nueva y sus defensas

* **Puertos 2525 y 2465** (`smtp.core-force.com`), publicados en `SMTP_RELAY_BIND_ADDRESS` (127.0.0.1 por defecto: se abren
  al desplegar con el cortafuegos de `docs/Operacion_Despliegue.md`). Docker publica por delante de UFW; el control fino
  por origen va en `DOCKER-USER`.
* **TLS**: AUTH ni se anuncia ni se admite sin TLS; TLS 1.2 minimo; certificado publico de acme recargado al renovarse,
  copiado a un volumen en memoria por un contenedor sin capacidades para que el relay no corra como root.
* **Credenciales**: secreto de 256 bits; freno por (usuario, IP) y por IP con bloqueo temporal en Redis; el usuario debe
  ser el prefijo de la contrasena (lo que no casa ni llega a access-control); cache negativa corta; la clave se vuelve a
  comprobar en cada `MAIL FROM`; respuestas que no distinguen motivo.
* **Abuso y recursos**: cupos por minuto de conexiones por IP y de mensajes por clave y por IP, tope de conexiones
  abiertas y de mensajes en proceso, tamano (10 MiB por defecto, hasta 40), destinatarios (50), partes MIME (500),
  anidamiento y largo de linea, tiempos de lectura y de entrega.
* **Contenido**: ClamAV sobre el mensaje entero si lleva adjuntos, con fallo cerrado (451); se retiran `Bcc`,
  `Return-Path` y toda cabecera `X-SES-*`, con la que un cliente podria elegir otro configuration set, otras etiquetas (y
  con ellas la atribucion de eventos a otra empresa) o la identidad de otra cuenta; el remitente del sobre, el de `From` y
  el de `Sender` deben ser de un dominio de envio verificado de la empresa.
* **Aislamiento**: el relay solo esta en las redes `mail-internal` y `mail-scan`, nunca en `mail-engines` (que es
  `mynetworks` de Postfix), no tiene base de datos y habla con access-control y transactional con el token interno.
* **Vigilancia**: metricas `smtp_relay_*`, `gateway_api_key_requests_total` y `access_control_api_key_resolutions_total`,
  y alertas del grupo `relay-smtp-y-claves-de-api` (barrido de credenciales, relay sin comprobar credenciales o sin
  entregar, malware, certificado por caducar, claves invalidas en el gateway).

## Consecuencias

* Una integracion envia con una credencial que no caduca a los 5 minutos y que la empresa revoca al instante.
* Cada mensaje de SMTP es un mensaje de `transactional` como los demas (estado, eventos de SES, supresion, reputacion,
  facturacion, ver en el navegador si trae HTML), con `origin = 'smtp'` y su MIME en `transactional.raw_contents`.
* Un secreto mas en el almacen (`API_KEY_HASH_KEY`) sin el que access-control no arranca; perderlo invalida todas las
  claves.
* Un puerto publico mas que vigilar y un certificado mas que debe llevar su nombre (`ADDITIONAL_SAN` de acme).
* El MIME de 40 MiB por mensaje ocupa la base de la empresa; queda como mejora podarlo tras el envio o moverlo al
  almacen de objetos si el volumen lo exige.
