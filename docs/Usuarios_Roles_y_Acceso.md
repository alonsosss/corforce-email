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
  acciones criticas (`X-Step-Up`, `STEP_UP_MODE=enforce`). El reto y el step-up salen de
  la misma clave con su propio `typ`: ninguno vale como token de acceso ni al reves.
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
  intentos a cero) y una contrasena mala tras vencer vuelve a bloquear desde ese momento. El
  bloqueo solo cae sobre una cuenta `active` o `locked`: nunca convierte en `locked` una
  `inactive` o `pending`, que al vencer quedaria activa. Cerrar una cuenta sin plazo es
  `inactive`. `inactive`, `pending` y cualquier estado desconocido se rechazan antes de mirar
  la contrasena con 403 `ACCOUNT_INACTIVE` (antes `pending` entraba y recibia un token que la
  renovacion y el gateway rechazaban), y el bloqueo vigente con 403 `ACCOUNT_LOCKED`, sin
  contar un intento fallido. Contrasena mala, correo desconocido y correo que no resuelve
  empresa responden el mismo 401; sin `tenant_slug`, el correo de una cuenta `inactive` o
  `pending` no resuelve empresa y responde ese 401, asi que su estado solo lo ve quien indica
  la empresa, como ya pasaba con `inactive`.
* Tiempo del inicio de sesion (V, 2026-09-13): todo intento compara la contrasena exactamente
  una vez. Con una cuenta que puede tener sesion, contra su hash; sin empresa (slug
  desconocido o correo que no la resuelve), sin cuenta en la empresa o con una cuenta que no
  puede tener sesion (`inactive`, `pending`, bloqueo vigente), contra un hash de relleno que
  identity deriva al arrancar con el mismo hasher y el mismo coste que todo hash que escribe
  (`services/identity/internal/adapters/passwordhash`, bcrypt 10; el superadmin que siembra
  `ops/db/bootstrap-platform.sh` lleva ese coste y una prueba del paquete lo vigila). El tiempo
  no dice si el correo existe ni si resuelve empresa, el de una cuenta sin sesion no dice mas
  que su cuerpo, y su contrasena sigue sin mirarse. Queda: (1) la contrasena mala de una
  cuenta existente anade despues el apunte del intento (contador, evento, politica y, en el
  umbral, el bloqueo), milisegundos frente a las decenas del bcrypt, que solo se miden con
  muchas muestras y las cortan el limite por IP y el bloqueo; (2) ese bloqueo, a los N fallos,
  responde 403 `ACCOUNT_LOCKED` y delata la cuenta por el cuerpo; (3) una cuenta con un hash
  de otro coste (el superadmin de una plataforma arrancada antes de este cambio, con 12) se
  distingue por el tiempo hasta que cambie su contrasena; (4) cada intento sin cuenta cuesta
  ahora un bcrypt de CPU, que acotan los mismos limites. El reto MFA no compara contrasena ni
  sirve para enumerar: pide un token de reto firmado, que solo sale con la contrasena correcta.
  La solicitud de reinicio (`forgot-password`) responde igual, pero con cuenta guarda el enlace
  y llama al servicio transaccional antes de responder: su tiempo si delata la cuenta (P).
* Reinicio de contrasena por enlace de un solo uso, respuesta identica exista o no el
  correo; enviado por el servicio transaccional (`TRANSACTIONAL_MAIL_URL`, P hasta que
  exista).

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
`021_mail_security_access_permissions.sql`, que `mail-security` vuelve a exigir al
superadmin en el caso de uso porque el cortafuegos es de toda la celda) los ejerce solo el
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
`jobs/run`, que `jobs/update` no sustituye.

Un rol inactivo (`access_control.roles.status <> 'active'`) no concede nada: la politica
efectiva que sirve access-control (roles, permisos, modulos visibles, acciones de escritura
y usuarios con un permiso) solo cuenta roles activos, igual que el token de identity.
Desactivar un rol retira sus permisos en cuanto caduca la cache de la politica.

## 3. Permisos (V)

Triple `(module, resource, action)` en `access_control.permissions`, con comodin `*` en
`resource` y `action`. Modulos de permiso: `organization`, `identity`, `access`, `audit`,
`scheduler` (plano de control, siempre disponibles) y los de correo (`domains`,
`mailboxes`, `mail_routing`, `mail_security`, `mail_storage`, `transactional`,
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
(`identity.user.deleted`, seccion 7).

## 4. Tres capas de control

| Capa | Donde | Que hace | Estado |
|---|---|---|---|
| 1. Menu | `web/` | Muestra solo los modulos donde el usuario tiene algun permiso (`modules`) y consulta `can(module, resource, action)` para cada accion | P (fase 1) |
| 2. Gateway | `services/gateway/rbac.go` | Antes, en toda ruta con sesion: rechaza con 401 el token revocado o de una cuenta cerrada (seccion 1). Lecturas: exige que el modulo del prefijo este en `modules` (`RBAC_READ_MODE`). Escrituras: DELETE exige `delete`, PUT/PATCH `update`, POST cualquier accion de escritura del modulo (`RBAC_ENFORCE_MODE`). Modulo deshabilitado bloquea a todos, administradores incluidos. Autoservicio (`/auth`, `/sessions`, `/users/me`, `/access/my-modules`) no se gatea. Fallo: `RBAC_FAIL_MODE=closed`. Por defecto todo en `enforce` | V |
| 3. Handler | cada servicio | Exige el permiso de accion concreto ademas del gateo por modulo | V: `pkg/authz.Checker.RequirePermission(module, resource, action)` consulta la politica en access-control (`/api/v1/policy/{user}`, cache en memoria 1 minuto), deja pasar a `superadmin` y `tenant_admin`, responde 403 sin permiso y 503 si no se puede comprobar ni hay politica en cache. Lo usan `contacts` (modulos `contacts` y `segments`), `domain-service`, `mail-directory`, `mail-security`, `suppression`, `templates`, `transactional`, `campaigns` (`send` para programar, iniciar, reanudar y probar; `cancel` para pausar y cancelar; el detalle y el listado solo llevan estadisticas si el usuario tiene ademas `campaigns/stats/read`), `analytics` (`analytics/reports/read` en todas sus rutas), `automations` (`automations/workflows/{read,create,update,delete,activate}`, donde `activate` cubre tambien pausar y archivar; `automations/runs/read`; `automations/settings/{read,update}` para el doble opt-in y su historial; alcance `tenant`, `019_automations_permissions.sql`) y `billing` (que en su API de plataforma, planes y suscripciones de todas las empresas, exige ademas `RequireRoles(superadmin)`) y `reputation` (sus rutas de plataforma `/api/v1/reputation/tenants/*`, que operan sobre la base de otra empresa, exigen tambien `RequireRoles(superadmin)` por el mismo motivo con `reputation/tenants/*`). V tambien en el plano de control: `organization` exige `organization/*` (alcance plataforma, `020_control_plane_permissions.sql` anade `cells/*`); empresas, migraciones, modulos y celdas siguen ademas con `RequireRoles(superadmin)` porque operan la plataforma u otra empresa, y `reseed-roles` sigue con `RequireRoles(tenant_admin)` mas `access/roles/update` porque repara el rol de sistema, que ningun rol de empresa puede tocar. `identity` exige `identity/users/*` (`delete` para desactivar y borrar, `reset_password` para fijar la contrasena de otro), `identity/sessions/*` y `identity/session_policies/*`; la vista de sesiones de todas las empresas sigue con `RequireRoles(superadmin)` mas `identity/platform_sessions/read`; un rol de empresa con permisos sobre usuarios no edita, desactiva, borra ni reinicia a un usuario con rol del sistema (403; 503 si sus roles no se pueden leer). `access-control` comprueba en proceso con su caso de uso de politica (misma semantica, sin llamarse por HTTP): `access/roles/*`, `access/permissions/read`, `access/user_roles/*`, `access/denials/read`; la politica, los roles y el `check-access` propios son autoservicio y los de otro exigen `access/user_roles/read`; `POST /access/denials` solo lo llama la plataforma (403 si la peticion trae usuario) y `users-with-permission` pasa sin usuario (llamada interna) o con `access/user_roles/read`. Sin permiso, por ser autoservicio: `/auth/*` y MFA, `/sessions/{logout,logout-all,mine}`, la ficha y el perfil propios en `/users/{id}`, `change-password`, `password-policy` y `/access/my-modules`. V tambien `audit` y `scheduler`, cuyos datos son de la propia empresa (su base), con permisos de alcance `tenant` y sin `RequireRoles`: `audit` exige `audit/logs/read` (rastro, detalle, resumen, actividad de un usuario, cambios), `audit/logs/create` (apunte y lote), `audit/security_events/read`, `audit/security_events/acknowledge` para reconocer un evento y `audit/integrity/verify` para recorrer la cadena de hash (`audit/integrity/read` no tiene ruta); `scheduler` exige `scheduler/jobs/*` (`read`, `create`, `update` para editar, habilitar y deshabilitar, `run` para lanzar a mano), `scheduler/executions/*` (`read`, tambien el historial de un trabajo; `cancel`; `retry`) y `scheduler/tasks/*` (`read`, tambien la ventana de las pendientes, que llega en la meta de `GET /tasks` y no en `GET /meta`, que sigue exigiendo `jobs/read`; `create`; `cancel`) |

Las consultas que viajan en POST por llevar cuerpo (comprobar una lista de direcciones,
renderizar o previsualizar una plantilla, previsualizar un segmento) se declaran en
`routes.json` como `read_posts` y el gateway las gatea como LECTURA del modulo (V).

Las denegaciones se cuentan en `rbac_denials_total` y se guardan en
`access_control.access_denials`.

## 5. Cuentas de correo frente a usuarios de la plataforma (V; P contra motores reales)

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

V (2026-09-13): el webmail (`services/webmail`) autentica contra el buzon por `mail-auth`
con service `webmail`, que exige `imap_access` y `smtp_access`, acepta solo la contrasena
principal (nunca una de aplicacion), responde igual a un buzon inexistente y a una
contrasena mala y frena por (buzon, IP real que llega del gateway en `X-Real-IP`). No lleva
JWT de la plataforma ni pasa por el RBAC por modulo: el gateway lo enruta como prefijo
`self_authenticated` (`services/gateway/routes.json`, limitador estricto en
`POST /api/v1/webmail/session`) y el servicio autentica cada peticion con su cookie `cf_wm`
y exige un `Origin` permitido en toda escritura (`docs/arquitectura/CSP-Y-SESION.md`). Las
sesiones de un buzon se revocan al actualizarse o borrarse (`mail.mailbox.updated`,
`mail.mailbox.deleted`) y al cambiar su contrasena (`mail.mailbox.credentials_changed`, P
hasta que mail-directory lo emita). P: la verificacion contra Dovecot y Postfix reales.

## 6. Auditoria de acceso (V)

Todo inicio, fallo, bloqueo, logout y revocacion publica `identity.*`; el gateway publica
`audit.api.write` por cada escritura autenticada y `gateway.security.exfiltration` cuando
un usuario supera el umbral de lecturas; `audit` los persiste en cadena de hashes por
empresa y el detector genera eventos de seguridad (fuerza bruta, IP o dispositivo nuevo,
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

Baja: roles, cuentas y, si el alta no llego a completarse, la base que creo; despues la
empresa, sus modulos y su saga salen del registro. No se deshace: un fallo deja la baja en
curso en su paso y el siguiente `DELETE` o el barrido la terminan. La base de una empresa que
llego a operar se conserva.

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
