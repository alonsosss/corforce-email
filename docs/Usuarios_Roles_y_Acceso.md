# Usuarios, roles y acceso

V = verificado en el codigo. P = propuesto, todavia no implementado.

## 1. Identidad (V)

* Usuario = persona con cuenta en una empresa (`identity.users`, unico por
  `(tenant_id, email)`). El superadmin es un usuario de la empresa de plataforma.
* Contrasena con bcrypt, politica por empresa (longitud, clases, caducidad, historial,
  bloqueo tras N intentos) con valores por defecto cuando la empresa no tiene fila;
  comprobacion contra contrasenas filtradas (HIBP, k-anonimato) al crear o cambiar. El
  primer usuario de cada empresa pasa por la misma puerta: lo crea identity cuando se lo
  pide la saga de alta (seccion 7).
* Sesion: access token JWT de 5 minutos con claims `uid`, `tid`, `roles`, `iat`, firmado
  por identity con EdDSA (Ed25519, `kid` y `typ` `at+jwt` en la cabecera); solo identity
  tiene la clave privada (`JWT_SIGNING_KEY`) y el gateway verifica con las publicas
  (`JWT_PUBLIC_KEYS`), sin poder firmar; HS256 y `none` se rechazan
  (`docs/arquitectura/CSP-Y-SESION.md`, Firma del access token);
  refresh opaco de 256 bits, hasheado en `identity.sessions`, rotado en cada uso con
  gracia de 60 s para carreras legitimas y revocacion de todas las sesiones si se reutiliza
  uno ya rotado; politica de sesion por empresa (TTL, concurrentes, inactividad).
* Navegador: refresh en cookie `cf_rt` `HttpOnly; Secure; SameSite=Strict;
  Path=/api/v1/auth`, access solo en memoria (`docs/arquitectura/CSP-Y-SESION.md`).
* MFA TOTP (setup, activate, disable, reto de 5 minutos); step-up de 5 minutos para
  acciones criticas (`X-Step-Up`, `STEP_UP_MODE=enforce`). Fuera de identity lo exige `domain-service` al conectar
  un proveedor DNS (`POST /api/v1/domains/dns-providers/{provider}/connect`), que guarda una
  credencial de terceros con permiso de escritura sobre la zona del cliente: el permiso va antes que
  el step-up, y con `STEP_UP_MODE=enforce` el servicio verifica el token con `JWT_PUBLIC_KEYS` y no
  arranca sin ellas (V, 2026-09-17). El reto y el step-up salen de
  la misma clave con su propio `typ`: ninguno vale como token de acceso ni al reves.
* Secreto del segundo factor (V, 2026-09-24, `048_identity_mfa_secret_encryption.sql`): se guarda cifrado con
  `MAIL_ENCRYPTION_KEY` (AES-256-GCM, `identity.users.mfa_secret_enc`, con `identity-mfa:<id del usuario>` como datos
  autenticados: copiado a otra cuenta no se abre) y nunca en claro; identity no arranca sin la llave. La columna
  anterior `mfa_secret` queda vacia: una fila que aun la tenga (una replica anterior durante un despliegue) sigue
  valiendo y el barrido de identity, al arrancar y cada hora, la cifra y borra el secreto de las cuentas sin segundo
  factor activo. Con llaves en `MAIL_ENCRYPTION_KEYS_OLD` el mismo barrido re-cifra bajo la activa
  (`docs/Operacion_Despliegue.md`, seccion 2). Un secreto que ninguna llave abre responde 500 y no cuenta como
  intento fallido. Guardar el perfil (`PUT /users/{id}`) ya no reescribe el segundo factor.
* Anti-repeticion del codigo TOTP (V, 2026-09-24): `identity.users.mfa_last_step` guarda el ultimo paso de 30 s
  aceptado y un codigo solo vale si su paso es mayor, en una sola sentencia condicional (`AdvanceMFAStep`): el mismo
  codigo, o uno de un paso anterior dentro del margen de una ventana, no sirve dos veces en el reto de inicio de
  sesion, el step-up ni la desactivacion, ni aunque lleguen a la vez. Activar apunta el paso del codigo de activacion,
  que tampoco vale despues. Es la misma regla que ya seguia la verificacion en dos pasos de los buzones
  (`mail.mailbox_mfa.last_step`).
* Revocacion: `tokens_valid_from` por usuario; el gateway rechaza (401 `SESSION_REVOKED`)
  cualquier access token emitido antes (logout-all, cambio o reinicio de contrasena, MFA
  desactivada) y el de una cuenta que ya no existe en su empresa o no esta activa
  (`inactive`, `pending`, o `locked` con el bloqueo vigente: la regla con la que identity
  inicia sesion y renueva). Lo
  comprueba en toda ruta con sesion, tambien las de autoservicio, MFA y step-up, con la
  respuesta de access-control guardada 60 s por replica: ese es el plazo maximo para que
  una baja, una desactivacion o un logout-all surtan efecto. Detalle y comportamiento con
  access-control caido en `docs/arquitectura/CSP-Y-SESION.md` (Revocacion en el gateway).
* Estado de la cuenta (V, 2026-09-13): una sola regla (`domain.User.SessionAllowed`) decide
  el inicio de sesion, el segundo factor, la renovacion y la vista que lee access-control
  (`identity.v_user_status.effective_status`, `027_identity_effective_status.sql`): solo una
  cuenta efectivamente activa abre o conserva sesion. El bloqueo por intentos fallidos es
  temporal: con `locked_until` vencido, o sin fecha, la cuenta cuenta como `active` sin que
  nada escriba en ella; el siguiente inicio de sesion correcto lo retira (estado a `active`,
  intentos a cero) y una contrasena mala tras vencer vuelve a bloquear desde ese momento,
  mientras los fallos no se hayan olvidado (24 horas, ver "Bloqueo por intentos sin
  enumeracion"). El
  bloqueo solo cae sobre una cuenta `active` o `locked`: nunca convierte en `locked` una
  `inactive` o `pending`, que al vencer quedaria activa. Cerrar una cuenta sin plazo es
  `inactive`. `inactive`, `pending` y cualquier estado desconocido se rechazan antes de mirar
  la contrasena con 403 `ACCOUNT_INACTIVE` (antes `pending` entraba y recibia un token que la
  renovacion y el gateway rechazaban), y el bloqueo vigente con 403 `ACCOUNT_LOCKED`, sin
  contar un intento fallido. Contrasena mala, correo desconocido y correo que no resuelve
  empresa responden el mismo 401; sin `tenant_slug`, la cuenta `inactive` o `pending` no es
  candidata a entrar (ver "Empresa por la credencial") y responde ese 401, asi que su estado
  solo lo ve quien indica la empresa, como ya pasaba con `inactive`.
* Empresa por la credencial (V, 2026-09-17): el formulario ya no pide la empresa, asi que el
  inicio de sesion sin `tenant_slug` la resuelve por la contrasena. `identity.users` es unico
  por `(tenant_id, email)`, nunca por correo: la misma direccion puede estar dada de alta en
  varias empresas (una persona que administra mas de una). Antes la empresa se resolvia con
  `SELECT tenant_id ... WHERE email = $1 AND status IN ('active','locked') LIMIT 1`, sin
  orden: Postgres devolvia una cualquiera, y quien tenia la contrasena correcta de la otra
  empresa recibia "credenciales invalidas" de forma intermitente. Ahora identity lee las
  cuentas de ese correo que pueden tener sesion (`UserRepo.ListLoginCandidates`: `active` o
  `locked`, en orden de alta estable, hasta `app.loginCandidateLimit` = 4) y entra la que
  coincide con la contrasena. El bloqueo de la cuenta de una empresa no impide entrar en la
  otra, y una contrasena que no es de ninguna cuenta el intento fallido en TODAS las que
  podian entrar: omitir la empresa no puede ser la forma de probar contrasenas sin gastar los
  intentos de ninguna cuenta (por eso no se cuenta en el contador de correos sin cuenta, que
  solo iguala respuestas y no frena nada, sino en las cuentas). Lo que cuesta y lo que queda
  fuera: (1) cada intento sin empresa paga exactamente cuatro comparaciones de bcrypt (ver
  "Tiempo del inicio de sesion"), asi que subir el tope encarece todo inicio de sesion de la
  plataforma; (2) la direccion dada de alta en mas de cuatro empresas solo alcanza, sin
  indicarla, las cuatro cuentas mas antiguas, y las demas entran con `tenant_slug` (el enlace
  "Mi correo esta en mas de una empresa" del formulario, que se conserva por esto y por las
  cuentas `inactive` o `pending`); (3) la misma direccion con la MISMA contrasena en varias
  empresas es ambigua: entra la cuenta mas antigua, siempre la misma, y queda un aviso en el
  registro; (4) `POST /auth/forgot-password` sigue resolviendo la empresa por el correo
  (`TenantRepo.GetIDByEmail`), ahora con orden estable (la cuenta mas antigua) en vez de una
  cualquiera, asi que una direccion que esta en varias empresas solo recupera por si misma la
  contrasena de esa cuenta; las demas las reinicia su administrador. Pendiente (P): un enlace
  de reinicio por cuenta, que necesita nombrar la empresa en el correo.
* Tiempo del inicio de sesion (V, 2026-09-17): todo intento gasta las comparaciones de su
  camino, falle donde falle: una si indica la empresa (`tenant_slug`, una sola cuenta posible)
  y cuatro (`app.loginCandidateLimit`) si solo trae el correo, que es el tope de cuentas que
  puede resolver. Cada cuenta que puede tener sesion se compara contra su hash; lo que sobra
  del camino va contra un hash de relleno que identity deriva al arrancar con el mismo hasher
  y el mismo coste que todo hash que escribe
  (`services/identity/internal/adapters/passwordhash`, bcrypt 10; el superadmin que siembra
  `ops/db/bootstrap-platform.sh` lleva ese coste y una prueba del paquete lo vigila). Sin
  empresa (correo sin cuenta candidata o registro que no responde), sin cuenta en la empresa
  indicada o con una cuenta que no puede tener sesion (`inactive`, `pending`, bloqueo
  vigente), todas las comparaciones del camino son de relleno. El tiempo no dice si el correo
  existe, ni si resuelve empresa, ni en cuantas empresas esta, el de una cuenta sin sesion no
  dice mas que su cuerpo, y su contrasena sigue sin mirarse. Que un camino cueste mas que el
  otro no dice nada de ninguna cuenta: lo elige quien inicia sesion.
  Una cuenta con un hash de otro coste (el
  superadmin de una plataforma arrancada antes de 7172693, con 12) lo cambia por uno del coste
  vigente en su primer inicio correcto, en la misma sentencia que apunta el inicio y retira los
  intentos (`UserRepo.RecordLogin`), solo si la fila conserva el hash que se comparo (un cambio
  de contrasena simultaneo no se pisa) y sin tocar `password_changed_at`, porque la contrasena
  es la misma; hasta ese inicio se sigue distinguiendo por el tiempo. Queda: (1) tras comparar,
  la contrasena mala de una cuenta apunta el fallo en su fila y lee la politica, y un correo
  sin cuenta lee la politica y apunta el fallo en su contador: dos sentencias ligeras en los dos
  casos, milisegundos frente a las decenas del bcrypt; en el umbral la cuenta anade el bloqueo,
  su evento y su apunte, y durante un bloqueo la cuenta no escribe nada mientras el correo sin
  cuenta lee la politica y consulta su contador; solo se miden con muchas muestras, que cortan
  el limite por IP y el bloqueo; (2) sin empresa, una contrasena mala apunta el fallo en cada
  cuenta que podia entrar, asi que la direccion que esta en varias empresas escribe una fila
  mas por cuenta: milisegundos frente a las cuatro comparaciones que ya paga el camino;
  (3) cada intento sin cuenta cuesta un bcrypt de CPU con la empresa indicada y cuatro sin
  ella, mas una escritura, que acotan los mismos limites. El reto MFA no compara contrasena ni sirve para
  enumerar: pide un token de reto firmado, que solo sale con la contrasena correcta.
* Bloqueo por intentos sin enumeracion (V, 2026-09-13): un correo sin cuenta cuenta sus fallos
  como una cuenta y llega al mismo 403 `ACCOUNT_LOCKED`, con el mismo cuerpo, tras los mismos
  intentos, con `tenant_slug` o sin el, con un slug que no existe o con un correo que no
  resuelve empresa. Su contador vive en `identity.unknown_login_failures`
  (`028_identity_login_failures.sql`), nunca en `identity.users`, bajo el SHA-256 del ambito en
  que se busco (la empresa resuelta, el slug que no resolvio o ninguna empresa) y del correo tal
  como se busco, sin normalizar, porque la busqueda tampoco normaliza: la tabla no guarda lo que
  alguien probo. Umbral y plazo son los de la politica de la empresa resuelta y, sin empresa,
  los de por defecto (5 intentos, 30 minutos). La regla es una (`domain.LoginFailures`) y la
  base la aplica en una sentencia a los dos: un intento durante el bloqueo no cuenta, al vencer
  el siguiente fallo vuelve a bloquear, y los fallos se olvidan pasadas 24 horas
  (`domain.FailedLoginWindow`) desde el ultimo fallo contado y desde el final del ultimo
  bloqueo. Por eso `identity.users` lleva `last_failed_login_at` (una cuenta con fallos
  anteriores a la 028, sin esa fecha, los conserva hasta su siguiente fallo), y por eso identity
  borra cada hora, en lotes, los contadores de correos sin cuenta ya olvidados sin cambiar
  ninguna respuesta. Antes una cuenta no olvidaba nunca: una contrasena mala meses despues de
  cuatro fallos la bloqueaba. La ventana no sube el ritmo de un ataque sostenido, que al vencer
  cada bloqueo vuelve a bloquear al primer fallo. El mensaje sigue distinto del de la contrasena
  mala a proposito: ya no confirma la cuenta, y a quien si la tiene le dice por que no entra y
  que el reinicio de contrasena lo retira. El bloqueo de un correo sin cuenta no corta ninguna
  sesion ni publica `identity.user.locked`. Queda: (1) combinar intentos con y sin
  `tenant_slug` para el mismo correo, nombrando su empresa, revela si la cuenta es de esa
  empresa: la cuenta comparte su contador entre los dos caminos y un correo sin cuenta tiene uno
  por ambito (se comporta como la cuenta de una empresa que no se nombra); (2) sin
  `tenant_slug`, la cuenta de una empresa con umbral o plazo propios se bloquea con los suyos y
  un correo sin cuenta con los de por defecto; (3) una cuenta con fallos propios recientes
  (menos de 24 horas sin un inicio correcto despues) se bloquea antes que un correo sin cuenta.
* Recuperacion de contrasena sin enumeracion (V, 2026-09-13): `POST /auth/forgot-password`
  responde el mismo 200 con el mismo cuerpo exista o no el correo y ya no tarda distinto: la
  peticion valida el formato y encola la solicitud (correo en minusculas e IP) sin buscar la
  cuenta, en una cola acotada en memoria de identity (`adapters/resetqueue`: 256 en espera, 4
  trabajadores, 30 s por solicitud, con un contexto propio que no depende de la peticion).
  Buscar la cuenta, invalidar los enlaces anteriores, guardar el nuevo y llamar al servicio
  transaccional ocurre en el trabajador (`PasswordResetUseCase.ProcessReset`). El enlace sigue
  siendo de un solo uso, caduca a los 30 minutos y solo se guarda su SHA-256; los limites de
  peticiones no cambian (`gateway:auth` y los 60 por minuto e IP de identity). V (2026-09-21):
  el trabajador no genera otro enlace si la cuenta ya tuvo uno en los ultimos 2 minutos
  (`PasswordResetRepository.RequestedSince`, sobre `identity.password_reset_tokens`, ya indexada por
  usuario): sin ese freno, desde un pequeno numero de IPs se llenaba de correos el buzon de cualquier
  cuenta y, como cada enlace anula el anterior, se le impedia terminar un reinicio legitimo; ante un
  fallo al comprobarlo no se envia nada. Antes de encolar
  solo se decide lo que no depende del correo: sin `PUBLIC_BASE_URL` o sin
  `TRANSACTIONAL_MAIL_URL` no se encola nada. Es una cola en proceso y no la outbox porque no
  hay un cambio de negocio con el que confirmarse (con un correo desconocido no se escribe
  nada) y la outbox y el stream guardarian 7 dias cada correo que alguien teclea. Con la cola
  llena la solicitud se descarta con un aviso en el registro y la respuesta es la misma; al
  apagar, identity atiende lo encolado durante 10 s despues de cerrar el servidor. Queda: una
  caida pierde las solicitudes en espera (el usuario la repite: la respuesta nunca prometio el
  envio), y el trabajo del trabajador comparte base y CPU con las peticiones, un efecto que no
  se atribuye a ninguna. El correo lleva `html_body` y `text_body` con el mismo contenido y el
  enlace completo en la parte de texto, y sale por SES como `multipart/alternative`; el nombre
  del usuario va escapado en la parte HTML (V, 2026-09-24, unitarias). El envio por transactional
  (`POST /internal/send-email`) no lo recorre ninguna prueba de punta a punta (P).

## 2. Roles (V)

Solo dos roles viven en codigo (`pkg/middleware/roles.go`):

| Rol | Alcance | Se siembra |
|---|---|---|
| `superadmin` | Opera la plataforma: empresas, celdas, migraciones, sesiones de cualquier empresa. No dirige ninguna empresa y no recibe sus avisos | Empresa de plataforma, por operacion |
| `tenant_admin` | Administra su empresa: usuarios, roles, dominios, politicas | access-control al crear cada empresa, cuando se lo pide la saga de alta de organization (seccion 7), `is_system = true`, con todos los permisos de alcance `tenant` del catalogo |

Cualquier otro rol lo crea un `tenant_admin` por API y recibe permisos por
`role_permissions`. Un handler nunca compara con un nombre de rol distinto de los dos del
sistema; `IsPrivileged` es la unica pregunta que hace el codigo.

Cada permiso del catalogo tiene un alcance (`access_control.permissions.scope`,
`018_permission_scope.sql`): `tenant` o `platform`. Los de plataforma (`organization/*`,
`identity/platform_sessions/*`, `billing/plans/*`, `billing/subscriptions/*`,
`reputation/tenants/*`, `mail_security/firewall/*` de
`021_mail_security_access_permissions.sql`, `mail_security/queue/*` de
`033_mail_security_queue_permissions.sql` y `mail_security/rspamd/read` de
`036_mail_security_rspamd_permissions.sql`, que `mail-security` vuelve a exigir al
superadmin en el caso de uso porque el cortafuegos, la cola de Postfix y el historial del antispam son de toda la celda,
y `observability/logs/read` de `037_observability_permissions.sql`, que `observability` exige con el rol `superadmin`
en el handler y de nuevo en el caso de uso porque los registros mezclan a todas las empresas; `docs/adr/0009`) los ejerce solo el
`superadmin`, que no los necesita en ningun rol. Ningun rol de empresa los recibe: el
sembrado del `tenant_admin` filtra por alcance, `PUT /roles/{id}/permissions` responde 403
si se pide uno y el catalogo `GET /permissions` los oculta a quien no es `superadmin`. Una
migracion que siembre un permiso de plataforma lo declara con `scope = 'platform'`.

Quien no tiene un rol del sistema solo concede lo que tiene (`services/access-control`,
caso de uso): `PUT /roles/{id}/permissions` rechaza con 403 un permiso que no esta en su
politica efectiva (misma regla de comodines), y `POST /user-roles/assign` y `/revoke`
rechazan un rol cuyos permisos no tiene y cualquier rol del sistema. Sin esto, un permiso
de gestion de roles era una escalada a `tenant_admin`.

Dos reglas mas de la misma familia: con sesion, un apunte de `audit` solo se registra a
nombre de quien la tiene (`POST /logs` y `/logs/bulk` responden 403 si el cuerpo nombra a
otro usuario; solo una llamada interna, sin usuario, puede hacerlo), y los trabajos de
plataforma del `scheduler` (sin empresa) se leen desde una empresa pero no se cambian, ni
se activan, desactivan o lanzan (403); desactivar exige ademas que el trabajo sea de la
empresa, ya no basta el id. En el `scheduler` ninguna lectura ni escritura por id va sin la
empresa del token (V, 2026-09-13, unitarias, contrato e integracion contra Postgres 16): leer
y cancelar una tarea, el historial de un trabajo, las ejecuciones activas y las escrituras de
trabajos y ejecuciones llevan la condicion en el SQL, y una tarea, un trabajo o una ejecucion
de otra empresa responde 404 igual que uno que no existe, sin efectos. Reactivar un trabajo
`one_time` que el calendario ya despacho es 409 `JOB_ALREADY_RUN`: volver a lanzarlo es
`jobs/run`, que `jobs/update` no sustituye. Por lo mismo, desactivar un trabajo (`jobs/update`)
no cancela la ejecucion que el calendario ya reclamo antes (V, 2026-09-15): desactivar espera a
ese despacho y corta las siguientes; pararla es `executions/cancel`. Editar un trabajo exige la
version que se leyo (V, 2026-09-15): sin ella 428 `VERSION_REQUIRED` y, si otro administrador
lo edito despues, 409 `VERSION_CONFLICT` sin escribir nada.

Un rol inactivo (`access_control.roles.status <> 'active'`) no concede nada: la politica
efectiva que sirve access-control (roles, permisos, modulos visibles, acciones de escritura
y usuarios con un permiso) solo cuenta roles activos, igual que el token de identity.
Desactivar un rol retira sus permisos en cuanto caduca la cache de la politica.

El superadmin opera el cortafuegos de otra celda nombrandola en `X-Target-Cell` (V,
2026-09-13; Modelo 5.4): el gateway solo la acepta con el rol `superadmin` y la instancia
solo le atiende rutas de plataforma, nunca datos de una empresa.

## 3. Permisos (V)

Triple `(module, resource, action)` en `access_control.permissions`, con comodin `*` en
`resource` y `action`. Modulos de permiso: `organization`, `identity`, `access`, `audit`,
`scheduler`, `observability` (plano de control, siempre disponibles; `observability` solo tiene permisos de plataforma) y los de correo (`domains`,
`mailboxes`, `mail_routing`, `mail_security`, `mail_storage`, `migration`, `transactional`,
`templates`, `suppression`, `reputation`, `contacts`, `segments`, `campaigns`,
`automations`, `analytics`, `billing`, `policy`), que solo estan disponibles si la empresa
tiene contratado el modulo del catalogo que los agrupa (`module_catalog.permission_modules`).

Acceso operativo del usuario (`GET /api/v1/access/my-modules`):
`{is_admin, roles, modules, write_modules, write_actions, disabled_modules, tokens_valid_from}`.
access-control lo calcula en cada consulta, sin cache propia; el gateway lo guarda 60 s por
replica. Responde 404 `USER_NOT_FOUND` si la cuenta no existe en la empresa del token y 403
`USER_NOT_ACTIVE` si no esta activa: son las unicas respuestas que el gateway toma como
cuenta cerrada. La politica de permisos (`GET /api/v1/policy/{user}`, la que consulta
`pkg/authz`) va en cache Redis 5 minutos por usuario y empresa, invalidada al asignar o
revocar un rol, al retirar los roles de una empresa y al borrar una cuenta
(`identity.user.deleted`, seccion 7); tambien (V, 2026-09-17) al cambiar los permisos de un
rol (`PUT /roles/{id}/permissions`), al renombrarlo o editar su descripcion y al borrarlo:
`RBACUseCase` lee los usuarios que tienen el rol (`ListUsersByRole`, antes de borrarlo, ya
que sus filas de `user_roles` caen con el `ON DELETE CASCADE` del rol) e invalida la
politica cacheada de cada uno, para que un permiso revocado no siga activo hasta que expire
la cache.

## 4. Tres capas de control

| Capa | Donde | Que hace | Estado |
|---|---|---|---|
| 1. Menu | `web/` | Muestra solo los modulos donde el usuario tiene algun permiso (`modules`) y consulta `can(module, resource, action)` para cada accion; el superadmin pasa siempre las dos comprobaciones (`canSee` en `web/src/layout/nav.ts`, tambien usada por `RequireModule` y por la portada; `selectCan` en `web/src/access/store.ts`), igual que en las capas 2 y 3: los permisos de alcance plataforma (`organization/*`, `billing/plans`, `reputation/tenants`) nunca llegan a `role_permissions` ni siquiera para el, asi que sin este pase Empresas y Celdas no se veian en el menu y el boton para dar de alta una empresa no aparecia (V, 2026-09-18) | V |
| 2. Gateway | `services/gateway/rbac.go` | Antes, en toda ruta con sesion: rechaza con 401 el token revocado o de una cuenta cerrada (seccion 1). Lecturas: exige que el modulo del prefijo este en `modules` (`RBAC_READ_MODE`). Escrituras: DELETE exige `delete`, PUT/PATCH `update`, POST cualquier accion de escritura del modulo (`RBAC_ENFORCE_MODE`). Modulo deshabilitado bloquea a todos, administradores incluidos. Autoservicio (`/auth`, `/sessions`, `/users/me`, `/access/my-modules`; solo eso: un segundo segmento `me` en cualquier otro modulo NO exime, antes saltaba el gateo y el bloqueo de los modulos no contratados) no se gatea. Fallo: `RBAC_FAIL_MODE=closed`. Por defecto todo en `enforce`. La tabla `routes.json` solo admite un modulo vacio en `access`, `check-access` y `policy` (`ungatedPrefixes`): otro prefijo sin modulo hace que el gateway no arranque, en vez de quedar sin RBAC en silencio. El gateway escribe las cabeceras `X-User-ID`, `X-Tenant-ID`, `X-User-Roles` y `X-Gateway-Token` y ningun cliente las fija ni las quita, tampoco nombrandolas en `Connection` (`docs/arquitectura/CSP-Y-SESION.md`, «Cabeceras internas»): de eso depende que `RequireInternalCaller`, `internalOrPerm` y `Membership.Require` tomen la ausencia de usuario por una llamada entre servicios | V |
| 3. Handler | cada servicio | Exige el permiso de accion concreto ademas del gateo por modulo | V: `pkg/authz.Checker.RequirePermission(module, resource, action)` consulta la politica en access-control (`/api/v1/policy/{user}`, cache en memoria 1 minuto), deja pasar a `superadmin` y `tenant_admin`, responde 403 sin permiso y 503 si no se puede comprobar ni hay politica en cache. Lo usan `contacts` (modulos `contacts` y `segments`), `domain-service` (`domains/domains/*`; revocar una clave DKIM comprometida exige `revoke_dkim`, aparte de `rotate_dkim`, `030_domain_service_dkim_revoke.sql`; la publicacion automatica del DNS, V 2026-09-17 con `032_domain_service_dns_providers.sql`, exige `domains/dns_providers/read` para ver la conexion con Cloudflare, `connect` para guardar su token de API, `disconnect` para borrarlo y `domains/domains/publish_dns` para elegir el modo de un dominio y publicar sus registros; todas sus escrituras van por POST para que el gateway no pida `update` ni `delete` del modulo a quien solo tiene estas acciones), `mail-directory` (con el modulo `domains` la politica MTA-STS de un dominio, V 2026-09-21, `034_mail_directory_mta_sts_permissions.sql`: `domains/mta_sts/read` para verla y `domains/mta_sts/update` para cambiar su modo, incluido enforce; alcance `tenant`; la ruta publica `/public/mail-directory/mta-sts/{cell}/{dominio}` es sin sesion a proposito, la consultan otros servidores de correo, y solo devuelve el cuerpo de la politica publicada), `mail-security`, `suppression`, `templates`, `transactional`, `campaigns` (`send` para programar, iniciar, reanudar y probar; `cancel` para pausar y cancelar; el detalle y el listado solo llevan estadisticas si el usuario tiene ademas `campaigns/stats/read`), `analytics` (`analytics/reports/read` en todas sus rutas), `automations` (`automations/workflows/{read,create,update,delete,activate}`, donde `activate` cubre tambien pausar y archivar; `automations/runs/read`; `automations/settings/{read,update}` para el doble opt-in y su historial; alcance `tenant`, `019_automations_permissions.sql`) y `billing` (que en su API de plataforma, planes y suscripciones de todas las empresas, exige ademas `RequireRoles(superadmin)`) y `reputation` (sus rutas de plataforma `/api/v1/reputation/tenants/*`, que operan sobre la base de otra empresa, exigen tambien `RequireRoles(superadmin)` por el mismo motivo con `reputation/tenants/*`). V tambien en el plano de control: `organization` exige `organization/*` (alcance plataforma, `020_control_plane_permissions.sql` anade `cells/*`); empresas, migraciones, modulos y celdas siguen ademas con `RequireRoles(superadmin)` porque operan la plataforma u otra empresa, y `reseed-roles` sigue con `RequireRoles(tenant_admin)` mas `access/roles/update` porque repara el rol de sistema, que ningun rol de empresa puede tocar. `identity` exige `identity/users/*` (`delete` para desactivar y borrar, `reset_password` para fijar la contrasena de otro), `identity/sessions/*` y `identity/session_policies/*`; la vista de sesiones de todas las empresas sigue con `RequireRoles(superadmin)` mas `identity/platform_sessions/read`; un rol de empresa con permisos sobre usuarios no edita, desactiva, borra ni reinicia a un usuario con rol del sistema (403; 503 si sus roles no se pueden leer). `access-control` comprueba en proceso con su caso de uso de politica (misma semantica, sin llamarse por HTTP): `access/roles/*`, `access/permissions/read`, `access/user_roles/*`, `access/denials/read`; la politica, los roles y el `check-access` propios son autoservicio y los de otro exigen `access/user_roles/read`; `POST /access/denials` solo lo llama la plataforma (403 si la peticion trae usuario) y `users-with-permission` pasa sin usuario (llamada interna) o con `access/user_roles/read`. Sin permiso, por ser autoservicio: `/auth/*` y MFA, `/sessions/{logout,logout-all,mine}`, la ficha y el perfil propios en `/users/{id}`, `change-password`, `password-policy` y `/access/my-modules`. V tambien `audit` y `scheduler`, cuyos datos son de la propia empresa (su base), con permisos de alcance `tenant` y sin `RequireRoles`: `audit` exige `audit/logs/read` (rastro, detalle, resumen, actividad de un usuario, cambios), `audit/logs/create` (apunte y lote), `audit/security_events/read`, `audit/security_events/acknowledge` para reconocer un evento y `audit/integrity/verify` para recorrer la cadena de hash (`audit/integrity/read` no tiene ruta; cada recorrido lee entera la tabla de auditoria de la empresa en una base compartida, asi que el servicio admite uno por empresa y cuatro a la vez, responde 429 `VERIFICATION_BUSY` con `Retry-After` a los demas y corta el que pasa de 15 minutos); `scheduler` exige `scheduler/jobs/*` (`read`, `create`, `update` para editar, habilitar y deshabilitar, `run` para lanzar a mano), `scheduler/executions/*` (`read`, tambien el historial de un trabajo; `cancel`; `retry`) y `scheduler/tasks/*` (`read`, tambien la ventana de las pendientes, que llega en la meta de `GET /tasks` y no en `GET /meta`, que sigue exigiendo `jobs/read`; `create`; `cancel`) |

Las consultas que viajan en POST por llevar cuerpo (comprobar una lista de direcciones,
renderizar o previsualizar una plantilla, previsualizar un segmento) se declaran en
`routes.json` como `read_posts` y el gateway las gatea como LECTURA del modulo (V).

Las denegaciones se cuentan en `rbac_denials_total` y se guardan en
`access_control.access_denials`.

### 4.1 Claves de API de empresa (V, 2026-09-23)

Segunda forma de autenticarse, para integraciones y no para personas (`docs/adr/0013-claves-de-api-y-relay-smtp.md`).
Hay dos familias de credencial con poderes disjuntos (`docs/adr/0017-aprovisionamiento-desde-otro-producto.md`):
la de **envio** (`cfm_`), que es la descrita aqui, y la de **aprovisionamiento** (`cfp_`), que creara empresas,
dominios y claves de envio para otro producto y **no puede mandar un solo correo ni leer un buzon**. La familia va en
el prefijo del token y en la columna `kind`, y cada una solo entra en su propia lista cerrada de rutas. La de
aprovisionamiento todavia no tiene ninguna ruta: la integracion esta aparcada.

Una clave de envio es `cfm_<prefijo>_<secreto>`: el prefijo (12 caracteres) es publico y es el usuario SMTP; el secreto (256
bits) solo se muestra al crearla y access-control guarda su HMAC-SHA256 con la llave `API_KEY_HASH_KEY` del almacen.

* **Quien la crea.** Quien tiene `access/api_keys/create` (el `tenant_admin` siempre), en `POST /api/v1/access/api-keys`
  y con step-up si `STEP_UP_MODE=enforce`. El alcance son permisos exactos del catalogo marcados `api_key_grantable`
  (hoy `transactional/messages/create` y `transactional/messages/read`, `044_access_control_api_keys.sql`) y nunca uno
  que el creador no tenga (403). Caducidad opcional, de una hora a cinco anos; hasta 100 vigentes por empresa. Verla y
  revocarla exigen `access/api_keys/read` y `access/api_keys/revoke`. Una clave no gestiona claves ni llega a las rutas
  internas de access-control (403 aunque llegara hasta ellas).
* **Donde vale.** Solo en las rutas de `api_key_routes` de `services/gateway/routes.json` (hoy enviar y leer el estado
  de un mensaje transaccional) y como credencial de `smtp-relay` si lleva `transactional/messages/create`. En cualquier
  otra ruta el gateway responde 401 `API_KEY_ROUTE_NOT_ALLOWED` sin tratarla como JWT.
* **Que se inyecta.** Sin usuario ni roles: `X-Tenant-ID`, `X-Api-Key-ID` y `X-Api-Key-Scopes` con el alcance
  efectivo, cabeceras que el gateway borra de toda peticion de cliente (`StripInternalHeaders`). La capa 2 exige que el
  alcance cubra el modulo y la accion del metodo en cualquier modo del RBAC; la capa 3 (`pkg/authz`) decide solo con
  ese alcance, nunca con la politica de una persona, y `RequireInternalCaller` e `internalOrPerm` no toman una clave por
  una llamada entre servicios.
* **Limites.** El alcance efectivo se recalcula en cada resolucion: el de la clave acotado a lo que su creador tiene hoy
  (un `tenant_admin` lo tiene todo) y a los modulos contratados; si el creador pierde el permiso, se desactiva o se
  borra, o la empresa deja de estar activa, la clave deja de valer. La revocacion es inmediata: access-control deja una
  marca en Redis que el gateway y el relay consultan en cada uso de su cache (`API_KEY_CACHE_TTL`, 30 s; sin Redis, la
  revocacion llega al vencer la cache). Cupo por clave en el gateway (`API_KEY_RATE_LIMIT_PER_MIN`) y en el relay
  (`SMTP_RELAY_MESSAGES_PER_MIN_PER_KEY`). Ultimo uso (fecha e IP) como mucho una vez por minuto.
* **Auditoria.** Alta y revocacion van por la outbox del registro (`access.api_key.created`, `access.api_key.revoked`,
  sin secreto ni hash) al rastro de la empresa; cada escritura del API con clave lleva `api_key_id` en `audit.api.write`
  y cada mensaje su `api_key_id` en `transactional.messages`.

## 5. Cuentas de correo frente a usuarios de la plataforma (V)

Un **usuario** de la plataforma (identity) administra; un **buzon** (`mail.mailboxes`) es
una cuenta de correo con su propia contrasena y contrasenas de aplicacion, verificadas por
`mail-auth`. Son entidades distintas: una persona puede tener usuario sin buzon (un
administrador de marketing) y un buzon sin usuario (una cuenta compartida). El webmail
(fase 2) autentica contra el buzon, no contra identity, y `mail-auth` aplica la misma
politica de bloqueo y el mismo registro de inicios (`mail.sasl_logins`).

V: `mail-auth` verifica hoy contrasena principal y de aplicacion con bcrypt, deniega
`active <> 1` y el protocolo sin flag, frena por `(username, IP)` y por IP en Redis y
escribe `mail.sasl_logins`; expone los inicios de un buzon por
`GET /internal/mail-auth/logins` acotado por `X-Tenant-ID`.

V (2026-09-21, revision adversaria de CardDAV y CalDAV, `docs/adr/0004-contactos-y-calendario-carddav-caldav.md`): una
contrasena que no es la principal cuesta siempre dos rondas de bcrypt en `mail-auth`, exista o no el buzon y tenga o no
contrasenas de aplicacion (la segunda compara todas las de aplicacion a la vez, o un hash ficticio), de modo que el
tiempo de respuesta no dice que direcciones existen; `mail-auth` lee como mucho 50 contrasenas de aplicacion por buzon y
`mail-directory` no deja crear mas de 25 (409); un protocolo que verifica cada peticion (`dav`) deja un registro en
`mail.sasl_logins` (y anota `last_used_at`) por cliente y ventana de 5 minutos, no por peticion; y `mail-dav` recuerda
un acierto `MAIL_DAV_AUTH_CACHE_TTL` (10 segundos), asi que apagar `dav_access`, desactivar o cambiar una contrasena, o dar
de baja el buzon, alcanza a DAV como mucho a los 10 segundos. La retencion de `mail.sasl_logins` sigue pendiente.

V (2026-09-23, pruebas de componente en jsdom; P: en navegador real): **un solo inicio de sesion**
para las dos cuentas. `/login` (a donde lleva la raiz sin sesion) pide correo y contrasena y
prueba primero el buzon (`POST /api/v1/webmail/session`) y, si no coincide, la plataforma
(`POST /api/v1/auth/login`): el buzon abre el webmail (`/webmail`, solo su correo), la cuenta
de la plataforma abre el panel con los modulos de su rol. La orquestacion es de la interfaz
(`web/src/auth/signIn.ts`); los dos servicios no cambian y cada uno sigue contando y frenando
sus propios fallos. El orden es deliberado: probar antes la plataforma le contaria a identity un
fallo por cada entrada de un empleado (y bloquearia la cuenta de la plataforma de quien tiene
ademas un buzon con la misma direccion y otra contrasena); al reves, cada entrada de un
administrador cuesta un fallo en el freno de `mail-auth` (10 por buzon y 50 por IP cada 15
minutos por defecto), que ademas no pasa por el detector de fuerza bruta de audit. Con la
empresa indicada (`tenant_slug`) se prueba solo la plataforma: es la salida de quien tiene
buzon y cuenta de la plataforma con la misma direccion y la misma contrasena, que sin ella
entraria al buzon. Si la plataforma rechaza la credencial y el buzon no llego a comprobarse
(servicio caido o freno), se muestra el motivo del buzon y no "contrasena incorrecta". El
cliente del webmail se carga bajo demanda y no entra en el chunk de la plataforma;
`/webmail/login` sigue existiendo para volver a entrar cuando caduca la sesion del buzon.

V (2026-09-24, `make e2e-mail`; `docs/Plan_Webmail_Competitivo.md`): **el dueno del buzon cambia su
contrasena** desde el webmail (`POST /api/v1/webmail/password`). El webmail comprueba primero la actual en
`mail-auth` (service `webmail`, con la IP real y su freno; una actual mala es 401 `INVALID_CREDENTIALS` y no
cambia nada) y solo entonces pide el cambio a `mail-directory` por `PUT /internal/mail-directory/password`,
que aplica la misma politica, el mismo bcrypt y el mismo evento `mail.mailbox.credentials_changed` que el
cambio del administrador. El webmail revoca en el acto las sesiones del buzon, tambien la suya, y el evento
lo repite para las demas instancias y para Dovecot. `mail-auth` devuelve ahora al webmail `tenant_id` y
`mailbox_id`, que su sesion guarda para hablar con `mail-dav`.

V (2026-09-13): el webmail (`services/webmail`) autentica contra el buzon por `mail-auth`
con service `webmail`, que exige `imap_access` y `smtp_access`, acepta solo la contrasena
principal (nunca una de aplicacion), responde igual a un buzon inexistente y a una
contrasena mala y frena por (buzon, IP real que llega del gateway en `X-Real-IP`). No lleva
JWT de la plataforma ni pasa por el RBAC por modulo: el gateway lo enruta como prefijo
`self_authenticated` (`services/gateway/routes.json`, limitador estricto en
`POST /api/v1/webmail/session`) y el servicio autentica cada peticion con su cookie `cf_wm`
y exige un `Origin` permitido en toda escritura (`docs/arquitectura/CSP-Y-SESION.md`). Con
varias celdas el gateway lleva el inicio de sesion a la celda del dominio del buzon y el resto a
la celda del token de la cookie, que enruta y no autoriza: cada instancia solo acepta tokens de
su celda (V, 2026-09-13; `Modelo_de_Datos_y_Celdas.md` 5.5). Las
sesiones de un buzon se revocan al borrarse (`mail.mailbox.deleted`), al cambiar su contrasena
principal y al perder el buzon un inicio de sesion (`mail.mailbox.credentials_changed` con
`credential` = `password`), y con el `mail.mailbox.updated` que toca lo que la sesion necesita. El
aviso de una contrasena de aplicacion (`app_password`) no las toca, porque el webmail no la admite.
Quitar `imap_access` o `smtp_access` las cierra porque el webmail necesita los dos.

V (2026-09-15): los dos eventos de cambio llevan `changed`, los atributos que cambiaron
(`domain.MailboxChanges` en mail-directory, en la misma transaccion del cambio), y el webmail revoca
salvo que TODOS sean inofensivos para su sesion (nombre visible, cuota, `kind`, TLS, relayhost,
`force_pw_update`, `pop3_access`, `sieve_access` y `dav_access`): cambiar la cuota o el nombre visible ya no echa
al usuario, que es lo que pasaba cuando revocaba con todo `mail.mailbox.updated`. La lista del
webmail es de lo inofensivo, no de lo peligroso, asi que un atributo que no reconozca, un `changed`
ilegible o su ausencia (un publicador anterior al campo) revocan igual que antes; el campo es
aditivo y un consumidor que no lo lea se comporta como hasta ahora. `changed` vale `password` o
`app_password` cuando lo que cambio fue la credencial misma, y la baja de la empresa apaga cada
buzon con `active`, que revoca. mail-security no lo lee: sigue decidiendo con el estado real del
directorio.

Acceso DAV (CardDAV y CalDAV, V, 2026-09-21, `docs/adr/0004-contactos-y-calendario-carddav-caldav.md`): un cliente de
contactos o de calendario (iOS, Thunderbird, DAVx5) no tiene sesion ni JWT, autentica cada peticion con HTTP Basic contra el BUZON. `mail-dav`
no valida la credencial: la verifica `mail-auth` con `service: "dav"`, que exige `active = 1` y el flag `dav_access`
(en el buzon y, si entra con una contrasena de aplicacion, en esa contrasena; la contrasena principal tambien vale) y
aplica el freno de fuerza bruta por (buzon, IP) y por IP como en IMAP. `mail-auth` devuelve solo a `dav` la empresa y
el buzon (`tenant_id`, `mailbox_id`, `username`): la empresa elige la base, el buzon acota todo lo que se lee o se
escribe (filtros de consulta y politicas RLS por buzon) y ninguna ruta o cabecera los sustituye. El gateway lo
declara como prefijo `self_authenticated` (`/api/v1/dav`, sin JWT ni RBAC por modulo, con su cupo general por IP); un
fallo de `mail-auth` es 503 y no 401. No hay revocacion de sesiones porque no hay sesion ni cache: apagar `dav_access`
o desactivar una contrasena de aplicacion se aplica en la siguiente peticion, y por eso perder `dav_access` no
publica `mail.mailbox.credentials_changed` (que echaria a IMAP sin necesidad) y el webmail lo cuenta entre los
cambios inofensivos. Administrar el flag es una accion de `mailboxes` (la ficha del buzon y sus contrasenas de
aplicacion; la ficha muestra la URL del servidor que ofrece `mail-directory` desde `MAIL_DAV_PUBLIC_URL`). Basic solo
es admisible porque el proxy de borde termina TLS.


V (2026-09-15, contra Dovecot y Postfix reales con `make e2e-mail`): Dovecot deja de aceptar al
momento la credencial de un buzon apagado, borrado o con la contrasena cambiada, aunque la tuviera
en su cache de autenticacion, y cierra sus sesiones IMAP y POP3 abiertas: mail-security consume
`mail.mailbox.*`, vacia su entrada y lo echa por el API HTTP de doveadm de la celda
(`deploy/mail/README.md`, "Revocacion en Dovecot"). Un cambio que no le retira nada (cuota, nombre
visible) solo vacia la cache, sin echarlo. Igual con una contrasena de aplicacion que se desactiva,
se borra o pierde un protocolo de inicio de sesion: mail-directory publica en la misma transaccion
`mail.mailbox.credentials_changed` con `credential` = `app_password`, Dovecot la rechaza al momento y
cierra las sesiones abiertas del buzon, y la contrasena principal y la sesion del webmail siguen.
Darla de alta o reactivarla no publica nada: no deja ninguna credencial vieja valiendo. Quitarle un
protocolo al buzon cierra al momento la sesion abierta con el (`credentials_changed` con `password`
en la misma transaccion, que mail-security atiende echando al buzon), y el buzon sigue entrando por
los que conserva.

Seguridad de los buzones (V 2026-09-24 en mail-directory y mail-auth, `046_mail_directory_security_permissions.sql`;
`docs/Plan_Webmail_Seguridad.md`): en el modulo `mailboxes`, `mail_policy/read` y `mail_policy/update` para ver y
cambiar si los buzones de la empresa pueden reenviar a direcciones externas (apagarlo retira en la misma
transaccion los reenvios externos ya guardados), y `mailbox_mfa/delete` para restablecer la verificacion en dos
pasos de un buzon (`DELETE /api/v1/mailboxes/{id}/mfa`, que publica `mail.mailbox.mfa_disabled` con `by: admin` y
`credentials_changed` con `credential: mfa`, con el que el webmail cierra las sesiones del buzon). Alcance
`tenant`: llegan al `tenant_admin` al sembrar el rol y con el resembrado. La verificacion en dos pasos del buzon es
del propio buzon, no de un usuario de la plataforma: la activa y la apaga su dueno desde el webmail; con ella activa
`mail-auth` solo acepta la contrasena principal en el webmail (que pide el codigo) y los programas de correo entran
con contrasenas de aplicacion. Guardar un reenvio a una direccion externa nueva exige reautenticacion
(`403 REAUTH_REQUIRED`); con la politica de la empresa apagada, `422 EXTERNAL_FORWARDING_DISABLED`.

Modulo `migration` (V, 2026-09-21, `035_mail_migration_permissions.sql`): `migration/jobs/read`
(ver los trabajos de migracion de buzones y su progreso), `migration/jobs/create` (lanzar la
migracion de un buzon: guarda cifrada una credencial de terceros) y `migration/jobs/cancel`,
todos de alcance `tenant`, asi que llegan al `tenant_admin` al sembrar el rol y con el resembrado.
Se contrata con `corporate_mail` (la migracion la anade a su `permission_modules`). `mail-migration`
exige la accion concreta en cada handler; el gateway deja pasar un `POST` a quien tenga cualquier
permiso de escritura del modulo. La API del ejecutor de migracion (`/v1/*` en el puerto 8057) no
usa permisos ni gateway: se autentica con su propia clave de servicio (`MAIL_MIGRATION_RUNNER_KEY`).

## 6. Auditoria de acceso (V)

Todo inicio, fallo, bloqueo, logout y revocacion publica `identity.*`; el gateway publica
`audit.api.write` por cada escritura autenticada y por cada peticion con celda destino
(tambien lecturas y rechazadas), con `target_cell`, y `gateway.security.exfiltration` cuando
un usuario supera el umbral de lecturas; `audit` los persiste en cadena de hashes por
empresa (con clave por fila y la cabeza anclada periodicamente, ADR 0006) y el detector genera eventos de seguridad (fuerza bruta, IP o dispositivo nuevo,
viaje imposible, exfiltracion) que reconoce quien tiene `audit/security_events/acknowledge`
(el `tenant_admin` siempre).

## 7. Alta y baja de una empresa (V, 2026-09-13)

organization no escribe en `identity` ni en `access_control` (las dos allowlists de
`check-coupling` estan vacias): el alta y la baja de una empresa son una saga
(`organization.tenant_sagas`, `026_organization_tenant_sagas.sql`) que pide cada paso al
dueno del dato por su API interna. Esas rutas solo las llaman otros servicios: exigen el
token interno (`X-Gateway-Token` = `INTERNAL_GATEWAY_TOKEN`, `RequireGatewayToken`) y
`middleware.RequireInternalCaller` responde 403 a cualquier peticion con usuario (el gateway
siempre lo inyecta y no enruta `/internal`). Todas son idempotentes.

| Ruta | Dueno | Que hace |
|---|---|---|
| `PUT /internal/access-control/tenants/{id}/system-role` | access-control | Crea `tenant_admin` (`is_system`) si falta y le concede los permisos de alcance `tenant` del catalogo que no tenga; 409 `SYSTEM_ROLE_NAME_TAKEN` si un rol propio ocupa el nombre |
| `POST /internal/access-control/system-role/reseed` | access-control | Lo mismo para el rol del sistema de todas las empresas, en una sentencia; organization la pide al arrancar, despues de migrar el registro |
| `PUT` y `DELETE /internal/access-control/tenants/{id}/users/{user}/roles/{role}` | access-control | Asigna (sin actor: `assigned_by` NULL) o retira un rol de la empresa; retirar un rol que ya no existe responde 200 |
| `DELETE /internal/access-control/tenants/{id}/roles` | access-control | Borra los roles de la empresa con sus permisos y asignaciones e invalida la politica cacheada de los afectados; 409 `TENANT_ACTIVE` si la empresa esta activa segun `organization.v_tenants` |
| `PUT /internal/identity/tenants/{id}/first-user` | identity | Primer usuario con el id que elige la saga: politica de contrasenas de la empresa, filtraciones y bcrypt como cualquier alta; 201 al crear, 200 al repetir con el mismo id, correo y contrasena, 409 `FIRST_USER_CONFLICT` si la empresa ya tiene otra cuenta, 422 `PASSWORD_POLICY` o `PASSWORD_BREACHED`. La contrasena no sale en la respuesta ni en un error |
| `DELETE /internal/identity/tenants/{id}/users` | identity | Borra las cuentas de la empresa; sus sesiones, historial y enlaces de reinicio caen con ellas. 409 `TENANT_ACTIVE` si la empresa esta activa |
| `PUT /internal/mail-directory/tenant-retirement` | mail-directory de la celda de la empresa | Da de baja a la empresa de `X-Tenant-ID` en el directorio de la celda: apaga todo lo suyo que recibe, reenvia o autentica y lo anuncia por la outbox; 200 con lo que apago, todo a cero al repetirla. Desde entonces su directorio responde 409 `TENANT_RETIRED` a toda escritura (`Modelo_de_Datos_y_Celdas.md`, 5.4) |

Alta: la empresa se registra inactiva; se crea su base (con el id de la empresa en su
comentario, para que un reintento adopte la suya y nunca toque una ajena) y se migra;
access-control siembra el rol; identity crea el primer usuario; access-control se lo asigna,
y solo entonces la empresa pasa a activa y sale `tenant.created`. Un fallo deshace en orden
inverso (asignacion, cuentas, roles, base propia) y deja la saga en `failed`: repetir
`POST /organizations` con el mismo slug vuelve a empezar y `DELETE /organizations/{id}` la
retira. Una contrasena que identity rechaza se responde con su 422 y no deja la empresa
registrada. Cada paso se guarda bajo un arriendo (`ORGANIZATION_SAGA_LEASE`, 5 min): si la
instancia muere, el reintento de la peticion sigue desde el ultimo paso, y si nadie la
reintenta el barrido (`ORGANIZATION_SAGA_SWEEP_INTERVAL`, 1 min) termina el alta cuando el
primer usuario ya existe y la deshace cuando no, porque la contrasena no se guarda nunca.
Mientras un alta o una baja estan en curso, el estado de la empresa no se cambia a mano (409).

Baja: roles, cuentas y, si el alta no llego a completarse, la base que creo; despues (V,
2026-09-15) mail-directory de su celda la da de baja en el directorio de correo, que deja de
recibir y de autenticar lo suyo y de admitir cambios, y organization suelta sus dominios del
indice global; al final la empresa, sus modulos y su saga salen del registro. Desde que empieza
la baja la empresa no reclama dominios. No se deshace: un fallo, tambien el de una celda que no
responde o no tiene instancia declarada, deja la baja en curso en su paso y el siguiente
`DELETE` o el barrido la terminan. La base de una empresa que llego a operar se conserva, y su
directorio de correo tambien: se desactiva, no se borra.

V (2026-09-13): el access token (5 min) de una cuenta borrada, con la baja de su empresa o
una a una, deja de servir en cuanto el gateway vuelve a preguntar a access-control (como
mucho 60 s por replica): sin fila en `identity.v_user_status` access-control responde
`USER_NOT_FOUND` y el gateway rechaza el token con 401 en toda ruta, tambien en las de
autoservicio que no gatea ningun modulo. Antes esa comprobacion fallaba abierta.

V (2026-09-13): borrar una cuenta suelta (`DELETE /users/{id}`) encola
`identity.user.deleted` (`tenant_id`, `user_id`, `deleted_at`; en el sobre, quien la borro)
en la outbox del registro dentro de la transaccion del borrado: si el evento no se encola, la
cuenta no se borra, y una baja repetida responde 404 sin anunciarse dos veces. El rele de
identity lo entrega al stream `IDENTITY`, que declaran identity y, con la misma definicion,
access-control. access-control lo consume (su primer consumidor: durable
`access-control-user-deleted`, reentrega y `EVENTS_DLQ` de `pkg/events`) y retira las
asignaciones de la cuenta a roles de su empresa, solo si la cuenta ya no existe en ella segun
`identity.v_user_status`, y descarta su politica en Redis. Es idempotente: una reentrega, una
cuenta sin roles o una empresa cuya baja ya retiro sus roles no borran nada y se confirman.
La baja de una empresa entera no lo emite, porque la saga ya retira los roles por empresa. Una
cuenta que vuelve a existir con el mismo id (restauracion) conserva sus asignaciones aunque le
llegue una entrega tardia.
