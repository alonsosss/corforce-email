# ADR 0002: migración de buzones desde otro proveedor con imapsync en un ejecutor aislado

## Estado

Propuesto (2026-09-21). No implementado. Cierra la decisión de `Plan_Estrategico_Mejoras_Correo.md`, C1:
herramienta y verificación de su licencia. Lo que falta es construirlo y que el responsable del producto
confirme el alcance de la primera versión (sección "Primera versión").

## Contexto

Sin migración, un cliente con correo en otro proveedor no puede entrar sin perder su historial. mailcow
la resuelve con `imapsync` lanzado desde su panel; aquí `imapsync` se quitó de la imagen de Dovecot
(`deploy/mail/UPSTREAM.md`) y no hay sustituto. Es la carencia con más efecto en la adopción.

Lo que hay que copiar es correo de terceros, con la contraseña del buzón de origen, hacia el buzón de la
plataforma. Eso fija los riesgos: una credencial ajena en tránsito y en reposo, un servidor IMAP de origen
que no controlamos (respuestas hostiles a un cliente que las parsea), volumen (un buzón de decenas de GB),
y el riesgo de perder o duplicar correo.

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
  lista, pausa y cancela, guarda el estado y el progreso, aplica el límite de `billing`, audita quién lanzó
  cada uno y publica eventos por outbox. Es lo único que toca la base y lo único que ve la interfaz.
  Alta con `make new-service`, ruta en `routes.json`, módulo de permisos `migration`.
* **`mail-migration-runner` (contenedor con imapsync y un envoltorio mínimo en Go).** No tiene acceso a la
  base ni a Redis. Vive en una red propia sin ruta a la plataforma: solo sale hacia Internet (los IMAP de
  origen, con puertos 143 y 993) y hacia el IMAP de Dovecot por el usuario maestro con `Dovecot master`
  restringido a la red del ejecutor (`DOVECOT_MASTER_ALLOWED_NETS`). Pide trabajo a `mail-migration` por
  HTTPS con su propia clave y recibe cada credencial de origen **una vez, al tomar el trabajo**, la pasa a
  imapsync por un descriptor de fichero (nunca por argumento ni entorno visibles en `ps`) y la olvida al
  terminar. Corre como usuario sin privilegios, sistema de ficheros de solo lectura, sin capacidades, con
  límites de CPU, memoria y tiempo por trabajo.

Controles obligatorios (los del plan):

* La contraseña de origen se cifra con `MAIL_ENCRYPTION_KEY` en la base de la empresa y se **borra al
  terminar** el trabajo, cancelado o fallido. Nunca sale por la API ni por el registro.
* Salida del ejecutor solo hacia el origen: sin acceso a la base, a Redis ni al resto de la red interna; el
  destino es únicamente Dovecot.
* Límite por empresa (buzones simultáneos y volumen) como derecho del plan en `billing`.
* Progreso y errores visibles por buzón (carpetas, mensajes copiados, omitidos, errores y último motivo),
  sin contenido.
* Auditoría de cada acción (quién, sobre qué buzón, qué origen) en la cadena de hashes.
* Idempotente y reanudable: repetir un trabajo no duplica correo (imapsync compara por `Message-ID`).
* El correo copiado pasa por el mismo camino que el entrante para los adjuntos: ClamAV no se salta por venir
  de una migración (se copia por IMAP `APPEND`, y el análisis se aplica igual que al resto antes de guardar
  cualquier adjunto: ver "Riesgos").

## Primera versión

Migración de un buzón a la vez desde un IMAP con usuario y contraseña (o contraseña de aplicación), en
una pasada inicial y una de repaso, con la interfaz en la ficha del buzón. Fuera de la primera versión:
OAuth2 de Google y Microsoft (los proveedores retiran la contraseña básica; es la mayor limitación real y
necesita registro de aplicaciones ante ellos), migración de calendarios y contactos, y migración masiva
por CSV.

## Consecuencias

* Un servicio nuevo, un contenedor nuevo y una red nueva: coste de operación real, con ADR (este) y con los
  chequeos existentes (`check-coupling`, contratos de eventos, `check-test-ratio`).
* El ejecutor es el componente que parsea respuestas de servidores ajenos: por eso no tiene nada que robar
  ni a lo que llegar dentro de la plataforma.
* Un Perl más en la lista de imágenes a escanear (`ops/security/escanear-motores.sh`) y en el libro de
  parches (`UPSTREAM.md`, sección 9), con la versión de imapsync fijada.

## Riesgos y preguntas abiertas

* **ClamAV sobre lo migrado.** `APPEND` por IMAP no pasa por Postfix/Rspamd. Hay que decidir si se analiza
  en el ejecutor (ClamAV en su red, el camino más simple) o después con un barrido; sin decidirlo no se
  puede afirmar que la regla de "ClamAV antes de guardar cualquier adjunto" se cumple en migraciones.
* **OAuth2**: sin él, los buzones de Gmail y Microsoft 365 con autenticación moderna no migran. Necesita
  decisión de producto y credenciales de aplicación ante cada proveedor.
* **Cuotas**: un buzón migrado puede superar su cuota; imapsync debe respetar `quota` y fallar limpio.
* **Servidor de origen bloqueando la IP** de la plataforma por parecer un ataque de fuerza bruta: limitar
  intentos de autenticación por trabajo y avisar al cliente.
