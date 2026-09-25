# Plan: seguridad del webmail (verificación en dos pasos, reenvío externo, límite por buzón)

Origen: auditoría del webmail del 2026-09-24 (hallazgos 3, 4 y 5 del servicio) y decisión del usuario
("sí, hazlo todo al detalle"). Este documento fija los contratos antes de implementar; lo que aquí
figura como diseño pasa a describir el estado real en la misma tarea que lo implemente.

## 1. Qué se resuelve

| Hallazgo | Riesgo | Respuesta |
|---|---|---|
| El webmail entra solo con contraseña | una contraseña robada abre lectura, envío y reenvío | verificación en dos pasos (TOTP, RFC 6238) por buzón, con códigos de recuperación |
| Con TOTP, IMAP/SMTP seguirían aceptando la contraseña principal | el segundo factor se saltaría por la puerta de los programas de correo | con TOTP activo, mail-auth rechaza la contraseña principal en todo protocolo salvo el webmail; los programas usan contraseñas de aplicación, que el propio usuario crea desde el webmail |
| Reenvío externo sin reautenticación | una sesión robada deja una copia de todo el correo fuera, que sobrevive al cambio de contraseña (persistencia típica de fraude BEC) | reenviar a una dirección de fuera de la empresa exige volver a escribir la contraseña (y el código si hay TOTP); interruptor por empresa para prohibirlo; evento de auditoría |
| Límite de peticiones por IP en un servicio con sesión | una oficina tras NAT comparte cupo; un solo usuario tiene cupo de sobra | cupo por buzón en Redis tras la sesión; por IP solo en lo que no tiene sesión |

Además (pendientes de la auditoría de imágenes): responder y reenviar conservan las imágenes del
original, e imprimir incluye las imágenes en línea.

## 2. Decisiones

1. **El secreto TOTP vive en la celda, cifrado.** Tabla `mail.mailbox_mfa` (propiedad de
   mail-directory), secreto cifrado con `MAIL_ENCRYPTION_KEY` (`pkg/crypto` KeyRing, AES-256-GCM con
   AAD = id del buzón, rotación con `MAIL_ENCRYPTION_KEYS_OLD`). Nunca sale de mail-directory después de
   activarse: la verificación de un código la hace mail-directory.
2. **mail-auth solo lee un booleano.** `mail.mailboxes.mfa_enabled` (columna nueva). mail-auth lo lee en
   `FindByUsername` (los grants son por tabla: no hay que tocarlos) y:
   - servicio `webmail`: contraseña correcta + `mfa_enabled` → responde éxito con `mfa_required: true`;
     el webmail no abre sesión hasta el segundo paso;
   - cualquier otro protocolo: con `mfa_enabled`, la contraseña principal se rechaza (resultado nuevo
     `mfa_app_password_required`, 401 para el cliente como cualquier otro rechazo); las contraseñas de
     aplicación siguen valiendo.
3. **Activar exige la contraseña actual.** Si no, un ladrón de sesión activaría su propio TOTP y
   dejaría fuera al dueño (también de IMAP). Desactivar exige contraseña y código. Regenerar códigos de
   recuperación exige código.
4. **El secreto no se guarda hasta que se confirma.** Igual que identity: `setup` devuelve secreto y URI
   (no persiste nada); `activate` recibe secreto + código, lo valida y entonces lo cifra y guarda.
5. **Anti-repetición.** Un código TOTP vale una vez: `mail.mailbox_mfa.last_step` guarda el último paso
   de 30 s aceptado y solo se acepta un paso mayor (`UPDATE ... WHERE last_step < $step`). `pkg/totp`
   gana `ValidateStep(secret, code, now) (step int64, ok bool)`.
6. **Códigos de recuperación.** 10 códigos de 10 caracteres (base32 sin caracteres ambiguos, en dos
   grupos de 5), guardados como SHA-256: son aleatorios de 50 bits, así que no necesitan un hash lento.
   Cada uno vale una vez y se muestran una sola vez.
7. **Segundo paso del login.** `POST /session` con TOTP activo responde `200 {"mfa_required": true}` y
   una cookie `cf_wm_mfa` (HttpOnly, Secure, SameSite=Strict, Path=/api/v1/webmail/session, 5 min) con
   un token de desafío de 256 bits guardado en Redis (solo su SHA-256) con la identidad del buzón y un
   contador de intentos (máximo 5; al quinto fallo el desafío se borra). `POST /session/mfa {code}`
   valida con mail-directory y abre la sesión normal (cookie `cf_wm`), borrando el desafío.
8. **Reenvío externo = dominio que no es de la empresa.** "Propio" = dominio en `mail.domains` o
   `mail.alias_domains` de la misma empresa. mail-directory decide, al guardar filtros, qué destinos
   externos son **nuevos** respecto de lo guardado; si hay alguno y la petición no viene
   reautenticada, responde `403 REAUTH_REQUIRED` con los destinos. El webmail reautentica (contraseña
   por mail-auth y, con TOTP, código) y repite con `reauthenticated=true`.
9. **Política por empresa.** Tabla `mail.mail_policy (tenant_id unique, external_forwarding_allowed
   boolean NOT NULL DEFAULT true)`. Sin fila = permitido (se conserva el comportamiento actual). Con
   `false`: guardar filtros con destinos externos da `422 EXTERNAL_FORWARDING_DISABLED`, y al apagarlo
   mail-directory retira en la misma transacción los destinos externos de los filtros ya guardados de
   la empresa y regenera su Sieve (un script guardado seguiría reenviando).
10. **Auditoría.** Eventos por outbox de mail-directory, en el stream `MAIL_DIRECTORY`:
    `mail.mailbox.forwarding_changed`, `mail.mailbox.mfa_enabled`, `mail.mailbox.mfa_disabled`,
    `mail.policy.updated`. Se añaden a `AUDIT_SUBJECTS`. El restablecimiento por el administrador emite
    además `mail.mailbox.credentials_changed` con `credential: "mfa"`, que revoca las sesiones del
    webmail del buzón.
11. **Contraseñas de aplicación desde el webmail.** Hoy solo las crea el administrador. Con TOTP el
    usuario las necesita: rutas internas por `?username=` que reutilizan los casos de uso existentes
    (máximo 25, se muestran una vez). Crear una exige la contraseña actual (y código con TOTP): una
    sesión robada no debe poder dejarse un acceso IMAP permanente.
12. **Cupo por buzón.** `pkg/middleware` `NewSharedRateLimiter` sobre el Redis del webmail, clave =
    buzón de la sesión, `WEBMAIL_RATE_LIMIT_PER_MAILBOX` (por defecto 600/min). Las rutas sin sesión
    (`POST /session`, `POST /session/mfa`, `/public/*`) conservan el límite por IP.

## 3. Contratos

### 3.1 Migraciones

- `migrations/cell/canonical/mail-directory/16_mailbox_security.sql` (`-- Schema: mail | Service:
  mail-directory`), idempotente y aditiva:
  - `ALTER TABLE mail.mailboxes ADD COLUMN IF NOT EXISTS mfa_enabled boolean NOT NULL DEFAULT false;`
  - `mail.mailbox_mfa (mailbox_id uuid PRIMARY KEY, tenant_id uuid NOT NULL, secret_enc bytea NOT NULL,
    recovery_hashes text[] NOT NULL DEFAULT '{}', last_step bigint NOT NULL DEFAULT 0, enabled_at
    timestamptz NOT NULL DEFAULT now(), created_at, updated_at)`; trigger `update_updated_at`; RLS
    `tenant_isolation` (mail_app) + `service_all` (mail_service); `REVOKE ALL ... FROM mail_engine`.
  - `mail.mail_policy (id uuid PK, tenant_id uuid NOT NULL UNIQUE, external_forwarding_allowed boolean
    NOT NULL DEFAULT true, updated_by uuid, created_at, updated_at)`; mismo patrón de RLS y grants.
- `migrations/registry/0NN_mail_directory_security_permissions.sql`: permisos
  `(mailboxes, mail_policy, read|update)` y `(mailboxes, mailbox_mfa, delete)` para `tenant_admin`,
  como `045_mail_directory_assistant_permissions.sql`.

### 3.2 mail-directory

Internas (grupo `RequireInternalCaller`, `?username=`), para el webmail:

| Método y ruta | Cuerpo | Respuesta |
|---|---|---|
| `GET /internal/mail-directory/mfa` | | `{enabled, enabled_at, recovery_remaining}` |
| `POST /internal/mail-directory/mfa/activate` | `{secret, code}` | `201 {recovery_codes: [10]}`; 422 `INVALID_MFA_CODE`; 409 `MFA_ALREADY_ENABLED` |
| `POST /internal/mail-directory/mfa/verify` | `{code}` (TOTP o recuperación) | `{method: "totp"\|"recovery", recovery_remaining}`; 422 `INVALID_MFA_CODE`; 409 `MFA_NOT_ENABLED` |
| `POST /internal/mail-directory/mfa/recovery-codes` | `{code}` | `{recovery_codes: [10]}` |
| `DELETE /internal/mail-directory/mfa` | `{code}` | 204 |
| `GET /internal/mail-directory/app-passwords` | | lista como la de administración (sin hash) |
| `POST /internal/mail-directory/app-passwords` | `{name, imap, pop3, smtp, sieve, dav}` | `201 {..., password}` (una vez) |
| `DELETE /internal/mail-directory/app-passwords/{id}` | | 204 |
| `PUT /internal/mail-directory/filters` | como hoy + `reauthenticated` (bool, opcional) | como hoy; 403 `REAUTH_REQUIRED {details: {addresses}}`; 422 `EXTERNAL_FORWARDING_DISABLED {details: {addresses}}` |

La contraseña y el código previos a estas llamadas los comprueba el webmail (mail-auth y
`mfa/verify`); mail-directory confía en el llamador interno como hoy con `password`.

Administración (gateway, módulo `mailboxes`):

| Método y ruta | Permiso | Efecto |
|---|---|---|
| `GET /api/v1/mail-directory/mail-policy` | `mail_policy/read` | `{external_forwarding_allowed, updated_at}` |
| `PUT /api/v1/mail-directory/mail-policy` | `mail_policy/update` | `{external_forwarding_allowed}` (explícito); al apagar retira destinos externos y regenera Sieve; evento |
| `GET /api/v1/mailboxes/{id}` | (existente) | añade `mfa_enabled` |
| `DELETE /api/v1/mailboxes/{id}/mfa` | `mailbox_mfa/delete` | restablece (borra la fila, `mfa_enabled=false`), eventos `mfa_disabled` (`by: "admin"`) y `credentials_changed` (`credential: "mfa"`) |

Eventos (outbox, `events.Event` con `TenantID`, `Source: "mail-directory"`):

| Subject | Data |
|---|---|
| `mail.mailbox.mfa_enabled` | `tenant_id, id, username, at` |
| `mail.mailbox.mfa_disabled` | `tenant_id, id, username, at, by` (`"user"` \| `"admin"`), `actor_id` si admin |
| `mail.mailbox.forwarding_changed` | `tenant_id, id, username, at, external_added[], external_removed[], forwarding_enabled` |
| `mail.policy.updated` | `tenant_id, external_forwarding_allowed, updated_by, removed_mailboxes` |

### 3.3 mail-auth

- `Mailbox.MFAEnabled` (lee `mfa_enabled`).
- `Result` nuevo `mfa_app_password_required` (métrica y log; para el cliente, el mismo 401).
- Respuesta al webmail: campo `mfa_required` (bool) en el éxito.

### 3.4 webmail (API pública `/api/v1/webmail`, `Origin` en toda escritura)

| Método y ruta | Cuerpo | Respuesta |
|---|---|---|
| `POST /session` | `{username, password}` | como hoy, o `200 {mfa_required: true}` + cookie `cf_wm_mfa` |
| `POST /session/mfa` | `{code}` | como `POST /session` sin TOTP; 422 `INVALID_MFA_CODE`; 401 `MFA_CHALLENGE_EXPIRED` |
| `GET /security` | | `{mfa: {enabled, enabled_at, recovery_remaining}, app_passwords: [...], app_passwords_max}` |
| `POST /security/mfa/setup` | `{current_password}` | `{secret, provisioning_uri}` |
| `POST /security/mfa/activate` | `{secret, code}` | `{recovery_codes}` |
| `POST /security/mfa/recovery-codes` | `{code}` | `{recovery_codes}` |
| `DELETE /security/mfa` | `{current_password, code}` | 204 |
| `POST /security/app-passwords` | `{name, protocols..., current_password, code?}` | `201 {..., password}` |
| `DELETE /security/app-passwords/{id}` | | 204 |
| `PUT /filters` | como hoy + `current_password?`, `code?` | como hoy; 403 `REAUTH_REQUIRED {addresses}`; 422 `EXTERNAL_FORWARDING_DISABLED` |
| `POST /password` | como hoy + `code?` (obligatorio con TOTP) | como hoy |

Errores nuevos: `MFA_REQUIRED` (falta el código en una acción que lo exige), `INVALID_MFA_CODE`,
`MFA_CHALLENGE_EXPIRED`, `REAUTH_REQUIRED`, `EXTERNAL_FORWARDING_DISABLED`.

#### Estado de S2 (servicio webmail, V 2026-09-24: unitarias, adaptadores con `httptest`, integración contra Redis real; sin S1 desplegado)

Implementado como la tabla, con estas precisiones, que son el contrato para S3 y S4:

- **Activar exige haber preparado.** `setup` guarda en el Redis del webmail el SHA-256 del secreto,
  ligado al buzón, 10 minutos; `activate` solo acepta ese secreto (409 `MFA_SETUP_EXPIRED` si no hay
  preparación o el secreto es otro). Sin esto, una sesión robada llamaría a `activate` con su propio
  secreto sin pasar por la contraseña. Un código malo no gasta la preparación. `activate` responde 200.
- **Desactivar no llama antes a `mfa/verify`.** La contraseña se comprueba en mail-auth y el código lo
  valida mail-directory en el propio `DELETE /internal/mail-directory/mfa {code}`: validarlo antes lo
  gastaría (anti-repetición) y el `DELETE` lo rechazaría. Lo mismo `recovery-codes {code}`. **S1 debe
  validar el código dentro de esas dos rutas.** `mfa/verify` lo usan el segundo paso del acceso y la
  reautenticación (`PUT /filters`, `POST /password`, `POST /security/app-passwords`).
- **Con TOTP lo dice mail-auth.** La reautenticación sabe si pedir código por `mfa_required` en la
  respuesta de mail-auth a la contraseña; sin él basta la contraseña. `setup` con TOTP ya activo es 409
  `MFA_ALREADY_ENABLED`.
- **Contraseña actual incorrecta** en cualquiera de estos flujos: 401 `INVALID_CREDENTIALS`, como
  `POST /password`; la sesión sigue abierta.
- **Códigos adicionales:** 409 `MFA_ALREADY_ENABLED`, 409 `MFA_NOT_ENABLED`, 409 `MFA_SETUP_EXPIRED`,
  404 `APP_PASSWORD_NOT_FOUND`, 409 `APP_PASSWORD_LIMIT` (el 409 de mail-directory al crear), 429
  `RATE_LIMITED` con `Retry-After`.
- **`details.addresses`** de `REAUTH_REQUIRED` y `EXTERNAL_FORWARDING_DISABLED` sale como lista JSON. El
  cliente de mail-directory la acepta como lista o como texto separado por comas (el `APIError` de
  `pkg/response` solo admite texto).
- **Contraseñas de aplicación en el webmail:** `{id, name, imap, pop3, smtp, sieve, dav, active,
  last_used_at, created_at}` (los mismos nombres que el alta); el alta responde 201 con esos campos y
  `password`. `app_passwords_max` es `null` salvo que la lista interna llegue como `{items, max}`; el
  cliente acepta también la lista suelta de la administración y el alta como `{app_password, password}`
  o plana.
- **Segundo paso:** el token del desafío tiene la forma del de sesión (`<celda>.<256 bits>`); en Redis
  solo su SHA-256 (`webmail:<celda>:mfa:c:<hash>`, identidad e intentos). Cada intento se cuenta de
  forma atómica antes de validar (también un código vacío o imposible, que no llega a mail-directory);
  el quinto fallo borra el desafío. Si mail-directory responde `MFA_NOT_ENABLED` (restablecido entre los
  dos pasos) el desafío se borra y la respuesta es `MFA_CHALLENGE_EXPIRED`. La sesión hereda como
  inicio el de la verificación de la contraseña: una revocación posterior la alcanza.
- **Cupo:** por IP (en memoria, 600/min) en `POST /session`, `POST /session/mfa`, `DELETE /session`,
  `/public/booking/*` y toda petición cuya cookie no abre sesión; por buzón (`NewSharedRateLimiter`,
  limitador `webmail:mailbox:<celda>`, `WEBMAIL_RATE_LIMIT_PER_MAILBOX`, 60 a 100000, 600 por defecto)
  en todo lo que tiene sesión. `GET /events` cuenta una vez por conexión.
- **Emisor TOTP:** `MFA_ISSUER` (el de la plataforma; por defecto `Core Force Mail`).
- **Pendiente para S4 (gateway):** `POST /session/mfa` no lleva `cf_wm`, así que con varias celdas el
  gateway debe enrutarlo por la celda del token de `cf_wm_mfa` (mismo prefijo `<celda>.`) y añadirlo a
  `strict_limit`. Con una sola celda funciona sin cambios.

### 3.5 Web

- Webmail: paso del código en el acceso (TOTP o código de recuperación); pestaña **Seguridad** en
  Ajustes (activar con QR `pages/shared/QrCode.tsx`, códigos de recuperación una vez con copiar y
  descargar, desactivar, regenerar; contraseñas de aplicación con aviso de que con TOTP los programas de
  correo las necesitan); diálogo de reautenticación al guardar un reenvío externo; responder y reenviar
  conservan imágenes; imprimir con imágenes en línea.
- Consola: tarjeta "Reenvío a direcciones externas" en Buzones (como `AssistantSettingsCard`); en la
  ficha del buzón, estado de la verificación en dos pasos y "Restablecer".

### 3.6 Estado de S1 (implementado, sin desplegar)

S1 cumple los contratos de 3.1 a 3.3. Lo que el contrato dejaba abierto se resolvio asi:

- `details.addresses` de `REAUTH_REQUIRED` y `EXTERNAL_FORWARDING_DISABLED` es un texto con las direcciones
  separadas por coma (`"a@x.com,b@y.com"`): `error.details` es un mapa de textos en toda la plataforma
  (`pkg/response`, y el cliente interno del webmail lo decodifica asi). Una direccion validada no lleva comas.
- Destino externo **nuevo** = direccion externa que empieza a recibir correo con el cambio: la del reenvio si
  esta encendido y las acciones `forward` de las reglas activas. Guardar una direccion externa en un reenvio
  apagado o en una regla apagada no pide nada; encenderlos despues si. Con la politica apagada no se guarda
  ninguna direccion externa, activa o no.
- Al apagar la politica: el reenvio pierde sus direcciones externas y se apaga si no le queda ninguna; una regla
  pierde sus acciones `forward` externas y, si no le queda ninguna accion, se retira entera (solo reenviaba
  fuera; el correo que casaba se entrega en el buzon). Se repasa tambien al volver a guardar la politica ya
  apagada. El `PUT` responde ademas `removed_mailboxes`.
- `mail.mailbox.forwarding_changed` sale cuando cambian las direcciones externas activas o el interruptor del
  reenvio; la retirada por politica solo se anuncia en `mail.policy.updated` (`removed_mailboxes`).
- `POST /internal/mail-directory/app-passwords` responde el registro con `password` al mismo nivel
  (`{id, name, imap_access, ..., password}`).
- `DELETE /api/v1/mailboxes/{id}/mfa` sobre un buzon sin verificacion: `409 MFA_NOT_ENABLED`.
- `mail-security` trata `mail.mailbox.mfa_enabled` como credencial cambiada: vacia la cache de Dovecot y cierra
  las sesiones abiertas con la contrasena principal, que ya no vale por IMAP/POP3/SMTP/Sieve.
- `pkg/events.EnsureStream` no anade un subject que otro del stream ya captura (JetStream rechaza subjects
  solapados): `audit` pide `mail.mailbox.mfa_enabled` sobre `MAIL_DIRECTORY`, que ya tiene `mail.>`.

Pendiente fuera de S1: re-cifrar los secretos TOTP antes de retirar una llave de `MAIL_ENCRYPTION_KEYS_OLD` (hoy
se leen con las retiradas, pero ningun proceso los pasa a la activa).

## 4. Bloques

- **S1** (mail-directory, migraciones, permisos, eventos, auditoría, mail-auth).
- **S2** (webmail Go).
- **S3** (web: webmail y consola).
- **S4** (integración, e2e, documentos, despliegue): migración de celda → registro → mail-directory,
  mail-auth, audit → webmail → web.

## 5. Estado de la integración (S4)

Fusionados S1, S2 y S3 en una rama y cuadrados sus contratos:

- La lista interna de contraseñas de aplicación de mail-directory es `{items, max}` (el tope sale del
  directorio: `MaxAppPasswordsPerMailbox`); el webmail la publica en `GET /security` como
  `app_passwords_max`, y sus contraseñas con los mismos nombres que la administración (`imap_access`...).
- Códigos de error añadidos por S2 que la interfaz trata: `MFA_SETUP_EXPIRED` (la preparación caducó:
  se vuelve a pedir la contraseña), `APP_PASSWORD_LIMIT`, `APP_PASSWORD_NOT_FOUND`.
- Gateway: `POST /webmail/session/mfa` va con el limitador estricto y se enruta por la celda del token de
  `cf_wm_mfa` (`cell_challenge_cookie` en `routes.json`), porque el segundo paso aún no lleva `cf_wm`.
- `make e2e-mail` recorre todo contra los motores reales con un buzón propio (erika@): activación con
  contraseña, secreto cifrado, evento en el outbox, IMAP rechaza la contraseña principal y acepta la de
  aplicación, los dos pasos del acceso, anti-repetición, códigos de recuperación de un solo uso, el
  desafío borrado al quinto fallo, reenvío externo con reautenticación y su Sieve, la política que lo
  retira y lo prohíbe, y el restablecimiento del administrador que cierra las sesiones.

Despliegue: migración de celda 16 y de registro 046 (con resembrado de `tenant_admin`), añadir los cuatro
subjects nuevos a `AUDIT_SUBJECTS` del `.env` del servidor (que la fija), y los servicios en el orden de
arriba más gateway y mail-security.
