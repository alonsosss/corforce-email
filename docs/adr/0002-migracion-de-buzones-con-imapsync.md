# ADR 0002: migración de buzones desde otro proveedor con imapsync en un ejecutor aislado

## Estado

Aceptado, implementado en su primera versión (2026-09-21): migración de un buzón por trabajo desde un
IMAP con usuario y contraseña, en una pasada inicial y una de repaso. Lo implementado, lo probado y lo
que falta están en "Implementación de la primera versión" y en "Riesgos y preguntas abiertas". No se ha
ejecutado `make e2e-mail` con la sección nueva de `ops/e2e/mail.sh` ni se ha probado contra un
proveedor real; OAuth2 queda fuera.

Cierra la decisión de `Plan_Estrategico_Mejoras_Correo.md`, C1: herramienta y verificación de su licencia.

## Contexto

Sin migración, un cliente con correo en otro proveedor no puede entrar sin perder su historial. mailcow
la resuelve con `imapsync` lanzado desde su panel; aquí `imapsync` se quitó de la imagen de Dovecot
(`deploy/mail/UPSTREAM.md`) y no había sustituto. Es la carencia con más efecto en la adopción.

Lo que hay que copiar es correo de terceros, con la contraseña del buzón de origen, hacia el buzón de la
plataforma. Eso fija los riesgos: una credencial ajena en tránsito y en reposo, un servidor IMAP de origen
que no controlamos (respuestas hostiles a un cliente que las parsea, y una dirección que el usuario elige
y a la que sale el ejecutor: SSRF), volumen (un buzón de decenas de GB), y el riesgo de perder o duplicar
correo.

## Opciones

1. **`imapsync` en un ejecutor aislado (elegida).** Herramienta madura, en uso desde hace más de quince
   años, que resuelve lo difícil: mapeo de carpetas, banderas, duplicados por `Message-ID`, reanudación,
   mensajes enormes, particularidades de Gmail, Exchange, Office 365 y Dovecot. Licencia comprobada el
   2026-09-21 sobre su `LICENSE`: **NO LIMIT PUBLIC LICENSE** (versión 0/0, 2012), «no limits to do anything
   with this work and this license»: sin restricción de uso, modificación, redistribución ni uso comercial
   o como servicio. Está en Perl; no se reescribe ni se parchea por dentro (como Postfix y Dovecot).
2. **Implementación propia en Go con una librería IMAP.** Más control y sin Perl, pero es reescribir años de
   casos límite del correo real, con el riesgo de perder mensajes de clientes en el primer caso raro. Solo
   valdría si imapsync resultara inaceptable por seguridad, y no hay motivo hoy.
3. **Lanzarlo desde el `scheduler` como contenedor efímero** (la idea original del plan). Descartada: exige
   que un servicio Go hable con el socket de docker, que es la capacidad privilegiada que la decisión de
   C4 (`deploy/mail/README.md`, gestor de cola) evita ampliar.

## Decisión

Un **ejecutor** propio y una **capa de control** propia, separados:

* **`mail-migration` (servicio Go, molde hexagonal, base de la empresa).** Dueño de los trabajos: crea,
  lista y cancela, guarda el estado y el progreso, limita los trabajos activos por empresa, audita quién
  lanzó cada uno y publica eventos por outbox. Es lo único que toca la base y lo único que ve la interfaz.
  Alta con `make new-service`, ruta en `routes.json`, módulo de permisos `migration`.
* **`mail-migration-runner` (contenedor con imapsync y un envoltorio mínimo en Go).** No tiene acceso a la
  base ni a Redis. Vive en una red propia sin ruta a la plataforma: solo sale hacia Internet (los IMAP de
  origen, con puertos 143 y 993) y hacia el IMAP de Dovecot por un usuario maestro propio restringido a la
  red del ejecutor. Pide trabajo a `mail-migration` con su propia clave y recibe cada credencial de origen
  **una vez, al tomar el trabajo**, la pasa a imapsync por un fichero efímero (nunca por argumento ni
  entorno) y la olvida al terminar. Corre como usuario sin privilegios, sistema de ficheros de solo
  lectura, sin capacidades, con límites de CPU, memoria, procesos y tiempo por trabajo.

Controles obligatorios (los del plan):

* La contraseña de origen se cifra con `MAIL_ENCRYPTION_KEY` en la base de la empresa y se **borra al
  terminar** el trabajo, cancelado o fallido. Nunca sale por la API ni por el registro.
* Salida del ejecutor solo hacia el origen: sin acceso a la base, a Redis ni al resto de la red interna; el
  destino es únicamente Dovecot.
* Límite por empresa (trabajos simultáneos) y, pendiente, volumen como derecho del plan en `billing`.
* Progreso y errores visibles por buzón (carpetas, mensajes copiados, omitidos, errores y último motivo),
  sin contenido.
* Auditoría de cada acción (quién, sobre qué buzón, qué origen) en la cadena de hashes.
* Idempotente y reanudable: repetir un trabajo no duplica correo (imapsync compara por `Message-ID`).
* El correo copiado se analiza con ClamAV antes de guardarse (ver "ClamAV").

## Primera versión

Migración de un buzón a la vez desde un IMAP con usuario y contraseña (o contraseña de aplicación), en
una pasada inicial y una de repaso, con la interfaz en la ficha del buzón. Fuera de la primera versión:
OAuth2 de Google y Microsoft (los proveedores retiran la contraseña básica; es la mayor limitación real y
necesita registro de aplicaciones ante ellos), migración de calendarios y contactos, migración masiva
por CSV y pausar un trabajo (solo se cancela).

## Implementación de la primera versión

### `mail-migration` (V, 2026-09-21)

* **Datos.** `mail_migration.jobs` en la base de cada empresa (`migrations/tenant/canonical/mail-migration/`,
  `Modelo_de_Datos_y_Celdas.md` 4.1). El buzón vive en la celda: al lanzar el trabajo `mail-migration` lo
  pide a `mail-directory` por la instancia de la celda de la empresa (`GET
  /internal/mail-directory/mailboxes/{id}`, ruta interna nueva, solo servicios) y guarda su id y su nombre;
  rechaza uno que no es de la empresa (404 `MAILBOX_NOT_FOUND`) o no está activo (409 `MAILBOX_INACTIVE`).
* **Credencial.** `source_password_enc` con `pkg/crypto.KeyRing` (`MAIL_ENCRYPTION_KEY`, con las llaves
  viejas de la rotación para descifrar). Un `CHECK` de la base impide un estado final que la conserve, y todo
  cierre la pone a NULL en la misma sentencia. No sale por la API, los eventos ni el registro; el mensaje de
  error que informa el ejecutor se sanea (controles, 300 caracteres y la contraseña retirada si el servidor
  ajeno la repite). Un trabajo no se crea si el servicio no tiene clave del ejecutor: nadie lo ejecutaría y la
  contraseña quedaría guardada sin motivo (503 `NOT_CONFIGURED`).
* **SSRF (servicio).** El servidor de origen se valida en dos pasos al crear: sintaxis (nombre DNS ASCII con
  al menos un punto y última etiqueta no numérica, o IP literal sin zona; nada de `localhost`, nombres de una
  etiqueta ni formas como `2130706433`) y resolución del DNS, donde TODAS las direcciones deben ser públicas
  (`domain.IsPublicAddr`: fuera 0/8, 10/8, 100.64/10, 127/8, 169.254/16, 172.16/12, 192.168/16, reservadas,
  documentación, multicast y broadcast; en IPv6 solo `2000::/3` menos 2001::/23, documentación, 6to4 y
  `3fff::/20`; una IPv4 mapeada se juzga como IPv4). Puertos solo los de `MAIL_MIGRATION_SOURCE_PORTS` (143 y
  993), TLS obligatorio (`ssl` o `starttls`) salvo `MAIL_MIGRATION_ALLOW_PLAINTEXT=true`, una decisión de
  operador. `MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES` admite orígenes internos solo para el e2e: el servicio se
  niega a arrancar con ella fuera de `ENVIRONMENT` `development` o `test`. IMAP no tiene redirecciones HTTP
  que seguir; imapsync no sigue referrals. Esta resolución puede quedar vieja (DNS rebinding): la segunda
  comprobación, la que cuenta, es la del ejecutor, que conecta a la IP que él mismo validó.
* **Reclamo por empresa.** El ejecutor no conoce empresas y el servicio abre una base por empresa. Se
  eligió: una sentencia atómica por empresa (`FOR UPDATE SKIP LOCKED`, lease y contador de intentos), y para
  saber en qué empresa buscar, una pista en memoria de las empresas con trabajo conocido en esa instancia más
  un recorrido de las empresas activas (`TenantDB.ForEachActiveTenant`, el mismo patrón de los barridos de
  otros servicios) como mucho cada `MAIL_MIGRATION_SWEEP_INTERVAL`. Alternativas descartadas: recorrer todas las
  empresas en cada sondeo (coste proporcional al número de empresas cada 15 s), y una tabla de trabajos en el
  registro (el registro no guarda negocio, y llevaría la credencial de terceros a la base del plano de
  control). Coste asumido: un trabajo creado por otra instancia o cuyo lease venció se ve con hasta
  `MAIL_MIGRATION_SWEEP_INTERVAL` de retraso.
* **Lease.** Un ejecutor que no da latido en `MAIL_MIGRATION_LEASE` pierde el trabajo; otro puede tomarlo
  (`attempt` + 1, hasta `MAIL_MIGRATION_MAX_ATTEMPTS`) y recibe la credencial otra vez. Sin intentos, el
  trabajo pasa a `failed` (`runner_lost`); con la cancelación pedida y el lease vencido, a `cancelled`.
* **Límite por empresa.** `billing` solo lleva derechos de siete recursos fijos (`billing.plan_limits`, con un
  `CHECK` sobre la lista) que se cuentan por eventos de quien los consume; no hay un derecho de "trabajos
  simultáneos" y crearlo pide ampliar billing (recurso, contador, meta y pantallas). Se aplicó
  `MAIL_MIGRATION_MAX_ACTIVE_PER_TENANT` (2 por defecto), comprobado dentro de la transacción del alta con un
  cerrojo consultivo por empresa. **Pendiente de billing:** pasarlo a un derecho del plan, y el volumen.
* **Permisos y auditoría.** `migration/jobs/{read,create,cancel}` (alcance empresa,
  `034_mail_migration_permissions.sql`; `corporate_mail` los agrupa). Eventos `migration.job.*` por la outbox
  de la empresa (stream `MIGRATION`), con quien lanzó o canceló; `audit` los recoge (`AUDIT_SUBJECTS`,
  `migration.>`).
* **API del ejecutor.** Otro listener del mismo proceso (`MAIL_MIGRATION_RUNNER_PORT`, 8057) que no pasa por
  el gateway ni por JWT: `Authorization: Bearer <MAIL_MIGRATION_RUNNER_KEY>` comparada en tiempo constante,
  `Cache-Control: no-store`. Sin la clave, el listener no se abre. La clave es un secreto opcional del almacén
  (sufijo `?`), como `QUEUE_AGENT_API_KEY`: desplegar el código no la exige.

### `mail-migration-runner` (V, 2026-09-21)

Detalle operativo en `deploy/mail/README.md` ("Migración de buzones"). Lo decidido:

* **Imagen.** El paquete `imapsync` no existe en Debian ni en Ubuntu (comprobado en trixie, bookworm y
  noble). La imagen usa Alpine 3.23 (`community`) con `imapsync~=2.314`, y el build falla si
  `imapsync --version` no da esa serie; `apt` no interviene. Un binario Go de biblioteca estándar, sin
  dependencias ni shell propio, en su módulo aparte (`deploy/mail/migration-runner`, como
  `postfix/queue-agent`): no puede importar `pkg/db` ni `pkg/redis`, y `ops/scaffold/check-migration-runner.sh`
  lo comprueba en `make checks`.
* **Contraseñas.** imapsync no acepta descriptores de fichero: la contraseña de origen y la del maestro llegan
  por `--passfile1/2`, ficheros 0400 en un directorio 0700 de nombre aleatorio dentro de un tmpfs, borrados al
  terminar el trabajo y si se mata al hijo. No hay contraseña en argv, entorno ni registro (prueba con un
  imapsync falso que anota ambos), y el entorno del hijo es mínimo y explícito.
* **Destino.** IMAP con TLS implícito verificado contra `MAIL_HOSTNAME`, con un usuario maestro de Dovecot
  **propio de la migración** (`DOVECOT_MIGRATION_MASTER_*`, distinto del del webmail), con `allow_nets` de la
  red del ejecutor: se rota y se revoca por separado y una filtración no lo abre desde la red de los motores.
  Sigue abriendo cualquier buzón de la celda desde esa red: es el mismo poder que el del webmail, acotado por
  red. Destino fijo (`MIGRATION_DEST_HOST`): una sola celda (ver riesgos).
* **SSRF (ejecutor).** Resuelve el origen y rechaza cualquier dirección no pública (la misma lista del
  servicio, más el metadata y las formas ambiguas), y conecta imapsync a la **IP validada** (`--host1=<IP>`)
  con verificación del certificado contra el nombre pedido (`SSL_verify_mode=1`, `SSL_verifycn_name`,
  `SSL_hostname`), de modo que el DNS no puede cambiar de dirección entre la comprobación y la conexión.
  Probado con imapsync real: un nombre equivocado, una CA no confiable y un certificado sin la IP fallan, y
  Gmail y Office 365 pasan con la IP fijada. No hay opción para desactivar la verificación.
* **Red.** Solo la red `mail-migration` (bridge propio `br-mail-migr`): ejecutor, Dovecot, clamd y
  `mail-migration`. No está en `mail-engines` ni en la de la plataforma, así que no ve Postgres, Redis, NATS,
  Postfix, mail-auth ni mail-policy. Sale a Internet por NAT. Dentro de esa red Dovecot expone todos sus
  puertos; la segunda barrera recomendada es una regla `DOCKER-USER` en el host que este compose no pone.
* **Contenedor.** Usuario 10001, `read_only`, `cap_drop: ALL`, `no-new-privileges`, `pids_limit`, memoria y
  CPU acotadas, `ulimit core=0`, sin puertos publicados; tiempo por pasada (`MIGRATION_JOB_TIMEOUT`, 24 h)
  que mata el grupo de procesos, y la salida de imapsync y las respuestas de la API acotadas.
* **Sin clave, sin trabajo.** Sin `MAIL_MIGRATION_RUNNER_KEY` arranca, lo avisa y no reclama; con una
  configuración incoherente (origen privado fuera de pruebas, sin clamd y sin permiso expreso) queda no sano
  y no reclama, sin reiniciarse en bucle.
* **Progreso.** De la salida de imapsync 2.314 (muestras reales en `migration-runner/testdata/`) se extraen
  solo contadores por carpeta y totales y un motivo clasificado con mensaje fijo: nunca asuntos ni contenido.

### ClamAV (decisión cerrada en el ejecutor, con una limitación)

`APPEND` por IMAP no pasa por Postfix ni por Rspamd, así que la regla de "ClamAV antes de guardar cualquier
adjunto" no se cumple sola en una migración. Se eligió analizar **en el ejecutor, antes del APPEND, cada
mensaje**: imapsync tiene `--pipemess`, que pasa cada mensaje por un filtro y solo copia lo que devuelve. El
filtro es el propio binario del ejecutor (`migration-runner scan-filter`): envía el mensaje por INSTREAM al
clamd de la celda (alias `clamd` en la red `mail-migration`), lo devuelve intacto solo con `stream: OK`, y en
cualquier otro caso (infectado, clamd caído o con una respuesta rara, mensaje mayor que
`MIGRATION_SCAN_MAX_BYTES`) sale con error. Falla cerrado, y no borra nada del origen. Se descartó el barrido
posterior: dejaría el correo infectado en el buzón del cliente durante la ventana.

Hallazgo con imapsync real: sale con código 0 aunque el filtro rechace mensajes. El ejecutor parsea los
rechazos: cuenta el virus, sigue con el resto y deja el trabajo en `failed` con `virus_found` y los
contadores de lo copiado; si clamd cae a mitad, corta la pasada tras cinco mensajes sin analizar. Sin
`MIGRATION_CLAMD_ADDR` el ejecutor se niega a reclamar, salvo `MIGRATION_ALLOW_UNSCANNED=true`, una decisión
explícita del operador que queda como aviso en cada arranque. Probado con clamd real y EICAR: no se copia y el
origen queda intacto.

**Limitación honesta:** un mensaje mayor que `MIGRATION_SCAN_MAX_BYTES` (100 MiB, por debajo del
`StreamMaxLength` de clamd) no se copia ni se analiza y el trabajo acaba en `failed`; y `--pipemess` lo
ejecuta imapsync por un shell (necesita las redirecciones): la línea es fija y la ruta del binario se valida,
pero es un shell dentro del contenedor del ejecutor.

### Pruebas (ejecutadas el 2026-09-21)

* Unitarias: dominio (SSRF con 127.0.0.1, 10.x, 169.254.169.254, `::1`, IPv4 mapeada, CGNAT, dominios que
  resuelven a privada, mezcla pública y privada, formas numéricas), casos de uso con dobles, API de
  administración y del ejecutor, publicador de eventos, cliente de `mail-directory`, configuración; y el
  módulo del ejecutor con `go test -race` (guarda SSRF, API, parseo con salidas reales, filtro contra un clamd
  falso, ejecutor completo con un imapsync falso).
* Integración contra Postgres real (`IT_PACKAGES='./services/mail-migration/...' make test-integration`):
  migraciones dos veces, reclamo concurrente con 16 ejecutores (cada trabajo una sola vez), límite con 12 altas
  simultáneas, borrado de la credencial en todo estado final y rechazo de la base a uno que la conserve,
  aislamiento entre empresas, lease y reintentos, rol de servicio, y el recorrido completo con cifrado real y
  outbox.
* Laboratorio efímero con imapsync 2.314, dos Dovecot 2.3 y clamd reales: migración de punta a punta, TLS
  por IP con nombre, EICAR, clamd caído, origen privado rechazado, certificado no confiable, `allow_nets` del
  maestro.
* `make checks`, incluidos `check-migration-runner`, `check-upstream-ledger`, `check-deploy-mail` y
  `check-test-ratio` (suelo propio de `mail-migration`).
* **No ejecutado:** `make e2e-mail` (la sección de migración de `ops/e2e/mail.sh` solo pasó `bash -n`) y
  `ops/scaffold/test-deploy-mail.sh`; ni IPv6 con imapsync real ni un proveedor real.

## Consecuencias

* Un servicio nuevo, un contenedor nuevo y una red nueva: coste de operación real, con ADR (este) y con los
  chequeos existentes (`check-coupling`, contratos de eventos, `check-test-ratio`, `check-migration-runner`).
* El ejecutor es el componente que parsea respuestas de servidores ajenos: por eso no tiene nada que robar
  ni a lo que llegar dentro de la plataforma, salvo, por diseño, las credenciales de los trabajos que reclama
  y el usuario maestro de la migración. Un ejecutor comprometido podría reclamar los trabajos pendientes y
  leer sus contraseñas: el límite de trabajos activos y el borrado al terminar acotan la exposición.
* Un Perl más en la lista de imágenes a escanear (`ops/security/escanear-motores.sh`, que ya recorre
  `deploy/mail/*/Dockerfile`) y en el libro de parches (`UPSTREAM.md`), con la serie de imapsync fijada.
* Primera vez que la plataforma exige una red de los motores nueva: en un servidor que ya corre hay que
  desplegar los motores antes que la plataforma (`docs/Operacion_Despliegue.md`).
* La clave del ejecutor y el maestro de migración viven en `secret-keys.txt`, cuyo fichero se entrega entero a
  los servicios que lo declaran (limitación conocida que ya tienen las demás claves de motores).

## Riesgos y preguntas abiertas

* **OAuth2**: sin él, los buzones de Gmail y Microsoft 365 con autenticación moderna no migran (con
  contraseña de aplicación sí). Necesita decisión de producto y credenciales de aplicación ante cada
  proveedor.
* **Derecho de `billing`**: el límite por empresa es de operador; falta el derecho del plan (y el volumen).
* **TLS entre `mail-migration` y el ejecutor**: hoy HTTP en una red de cuatro miembros de confianza; la
  respuesta del reclamo lleva la contraseña de origen. Un certificado de la CA interna en el listener 8057 lo
  cierra.
* **Regla `DOCKER-USER`** en el host que limite lo que el ejecutor alcanza dentro de su red (Dovecot solo por
  993, clamd por 3310, `mail-migration` por 8057) y que le impida las redes privadas y el metadata del
  proveedor. La guarda del ejecutor ya lo rechaza; la regla es la segunda barrera y no la pone el compose.
* **Varias celdas**: el ejecutor tiene un solo destino Dovecot (`MIGRATION_DEST_HOST`); con más de una celda
  el trabajo debe llevar la celda y el ejecutor elegir su Dovecot.
* **Cuotas**: un buzón migrado puede superar su cuota; imapsync falla limpio y el ejecutor lo informa como
  `quota_exceeded`.
* **Servidor de origen bloqueando la IP** de la plataforma por parecer un ataque de fuerza bruta: un
  trabajo son dos pasadas, cada una con su autenticación; falta limitar intentos por origen y avisar al
  cliente.
* **Historial de trabajos**: no se poda (una fila por migración, sin credencial).
