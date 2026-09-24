# ADR 0014: Ficheros grandes por enlace con el servicio mail-files

## Estado

Aceptado (2026-09-24). Bloque C4 (G8) de `docs/Plan_Webmail_Innovador.md`, decision 5.

## Contexto

Un buzon necesita enviar planos, contratos o videos que no caben como adjunto (el mensaje entero, con
adjuntos, lo acota `WEBMAIL_MAX_MESSAGE_BYTES`) sin recurrir a servicios de terceros. El plan fija la
forma: el fichero va al almacen de objetos (`pkg/objectstore`, MinIO en el servidor propio), ClamAV lo
analiza antes de publicarse y el correo lleva un enlace firmado con `MAIL_LINK_SIGNING_KEY` a una ruta
publica del gateway con caducidad, tope de descargas y revocacion.

El webmail no tiene base de datos (es un servicio de celda con la sesion en Redis y el correo en
Dovecot). Los metadatos del enlace (buzon dueno, empresa, clave del objeto, nombre, tamano, huella,
caducidad, descargas, estado) son datos de la empresa (decision 1 del plan) y tienen que vivir en la
base de la empresa de un servicio que la tenga.

## Decision

1. **Servicio nuevo `mail-files`**, dado de alta con `make new-service` (puerto 8061), plano de
   empresa: esquema `mail_files` en la base de cada empresa (`migrations/tenant/canonical/mail-files/`),
   rol `mail_files_service` con RLS por empresa, credencial `mail_svc_mail_files`.

   Se descarta meterlo en `templates` aunque ya maneja el almacen y ClamAV: templates es el contenido de
   los envios de marketing y transaccional (plantillas, kit de marca, paginas de aterrizaje) y sus
   usuarios son los de la plataforma. Los ficheros compartidos son de un buzon (su usuario es la sesion
   del webmail), tienen su propio ciclo de vida (caducidad, descargas, revocacion, barrido) y mueven
   cuerpos de hasta cien megas en los dos sentidos. En templates habrian mezclado dos dominios, dos
   modelos de identidad y el perfil de memoria y de tiempos de un editor con el de una transferencia de
   ficheros: un pico de descargas degradaria el editor. El coste del servicio nuevo es de operacion
   (compose, credencial, objetivo de metricas), no de codigo: el analisis y el almacen se reutilizan de
   `pkg/clamav` (que gana `ScanReader`, en flujo) y `pkg/objectstore`.

2. **Subida por el webmail**: `POST /api/v1/webmail/large-files` (multipart, campo `file`, opciones
   `expires_in_days` y `max_downloads` en la consulta) con la sesion del buzon y el Origin que el webmail
   ya exige a toda escritura. La parte pasa en flujo a la API interna de mail-files
   (`POST /internal/mail-files/files`, cuerpo en crudo, identidad en `X-Mailbox-Tenant-ID` y
   `X-Mailbox-ID` junto al token del gateway, el mismo contrato que mail-dav). El webmail no la carga en
   memoria.

3. **ClamAV antes de guardar**, en mail-files, que es quien guarda: la subida se recibe entera en un
   directorio temporal propio del servicio (con su SHA-256 y el tope de tamano), clamd la analiza en
   flujo y solo con un veredicto limpio se reserva en la cuota y se sube al almacen. Una firma es
   `422 FILE_INFECTED`; sin veredicto (clamd caido, tope de clamd) es `503 SCAN_UNAVAILABLE`: falla
   cerrado. El tamano maximo por fichero es 100 MiB porque es lo que clamd analiza entero
   (`StreamMaxLength` y `MaxFileSize`, 101 MiB) y lo que admite el borde (`EDGE_MAX_BODY_SIZE`).

4. **Almacen**: el bucket de la plataforma (`MINIO_*`), espacio `private/<empresa>/mail-files/<id>`,
   siempre como `application/octet-stream` y sin el nombre del usuario en la clave. El gateway solo sirve
   `public/` en `/media/*`: nada de `private/` es alcanzable salvo por mail-files.

5. **Enlace**: `PUBLIC_BASE_URL/api/v1/public/files/{empresa}/{fichero}?x=<caducidad unix>&s=<firma>`,
   HMAC-SHA256 con `MAIL_LINK_SIGNING_KEY` sobre `mail-files`, empresa, fichero y caducidad (el prefijo
   impide que valga como enlace de baja o de cuarentena). La firma se comprueba antes de resolver la base
   de la empresa. Abrir el enlace (`GET`) muestra una pagina sin JavaScript con el nombre, el tamano, la
   caducidad, las descargas restantes, la huella SHA-256 y un boton; la descarga es un `POST` a la misma
   URL. Asi los analizadores de enlaces de los correos, que siguen los `GET`, no gastan descargas.
   Cualquier enlace que no sirve (firma alterada, caducado, revocado, agotado, empresa inexistente)
   responde la misma pagina 404.

6. **Descarga**: `Content-Disposition: attachment` con `filename` ASCII y `filename*` UTF-8 del nombre
   saneado (solo el ultimo segmento, sin controles ni caracteres de formato como los que invierten el
   texto, sin reservados de sistemas de ficheros, sin nombres de dispositivo de Windows, 200 bytes como
   mucho), `application/octet-stream`, `nosniff`, `X-Download-Options: noopen`, CSP `sandbox`,
   `no-store`, `Content-Length` y `Repr-Digest`. La descarga se cuenta en una sola sentencia que exige
   que el enlace siga vigente, despues de abrir el objeto: si el objeto ya no esta, no se gasta. La ruta
   `POST` se declara en `routes.json` con `"transfer": "download"`, que amplia el plazo de escritura del
   gateway (120 s) para esa respuesta; el plazo real lo pone `MAIL_FILES_DOWNLOAD_TIMEOUT`. Cupo por IP:
   el general del gateway mas `MAIL_FILES_PUBLIC_RATE_LIMIT_PER_MIN` en el servicio.

7. **Cuotas y topes** de la configuracion de mail-files y servidos en cada listado para que la interfaz
   no los copie: tamano por fichero, caducidad por defecto y maxima, descargas por defecto y maximas,
   espacio por buzon y por empresa y enlaces vigentes por buzon. La comprobacion y la reserva van en una
   transaccion con un cerrojo por empresa: dos subidas simultaneas no pasan una cuota que solo admite una.

8. **Revocacion y barrido**: revocar cierra el enlace y borra el objeto en el acto. Un barrido con
   cerrojo de lider (`MAIL_FILES_SWEEP_INTERVAL`) caduca los enlaces vencidos, da por fallidas las subidas
   que no terminaron (`MAIL_FILES_PENDING_GRACE`), borra los objetos de lo revocado y fallido y, pasado el
   plazo de una descarga, de lo caducado o agotado, y poda el historial (`MAIL_FILES_HISTORY_RETENTION`).

## Consecuencias

* Servicio nuevo en `docker-compose.yml` y en el perfil autoalojado (96 MiB, en la red `mail-scan` con
  clamd), con su credencial de base (`MAIL_FILES_DB_PASSWORD`, en el almacen) y su fila en el reparto de
  secretos (`INTERNAL_GATEWAY_TOKEN`, `MAIL_LINK_SIGNING_KEY`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`).
  Despliegue: migracion de empresa, `ops/db/tenant-service-role.sh --service mail-files`, el servicio,
  el webmail (`MAIL_FILES_URL`) y el gateway (rutas publicas).
* El directorio temporal va en disco (volumen `mail-files-spool`) y no en tmpfs: cien megas por subida en
  RAM no caben en el presupuesto de memoria del perfil (6400 MiB). Es la unica diferencia con la regla del
  borde, donde un cuerpo sin analizar no toca el disco. Se mitiga con permisos 0700 del servicio, borrado
  al terminar cada subida y borrado al arrancar de lo que dejo un proceso interrumpido; el fichero limpio
  acaba de todos modos en el disco del almacen. Quien pueda permitirselo monta un tmpfs en
  `MAIL_FILES_SPOOL_DIR`.
* El techo de 100 MiB lo marca clamd. Subirlo exige subir antes `StreamMaxLength` y `MaxFileSize` de
  `deploy/mail/clamav/clamd.conf` (compartido con los motores) y `EDGE_MAX_BODY_SIZE`.
* Sin `MINIO_ENDPOINT` mail-files arranca con la funcion apagada, y sin `MAIL_FILES_URL` el webmail
  tambien: la interfaz no la ofrece.

## Mejoras futuras

* Retirar los enlaces de un buzon dado de baja al recibir `mail.mailbox.deleted` (hoy caducan solos, como
  mucho a `MAIL_FILES_MAX_EXPIRY_DAYS`).
* Subida por partes reanudable y con progreso para conexiones lentas.
* Bucket propio con usuario de servicio acotado y politica de ciclo de vida.
* Cuotas por empresa editables por su `tenant_admin` o ligadas al plan.
* Aviso al remitente cuando descargan su fichero.
