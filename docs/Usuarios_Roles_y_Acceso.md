# Usuarios, roles y acceso

V = verificado en el codigo. P = propuesto, todavia no implementado.

## 1. Identidad (V)

* Usuario = persona con cuenta en una empresa (`identity.users`, unico por
  `(tenant_id, email)`). El superadmin es un usuario de la empresa de plataforma.
* Contrasena con bcrypt, politica por empresa (longitud, clases, caducidad, historial,
  bloqueo tras N intentos) con valores por defecto cuando la empresa no tiene fila;
  comprobacion contra contrasenas filtradas (HIBP, k-anonimato) al crear o cambiar.
* Sesion: access token JWT HS256 de 5 minutos con claims `uid`, `tid`, `roles`, `iat`;
  refresh opaco de 256 bits, hasheado en `identity.sessions`, rotado en cada uso con
  gracia de 60 s para carreras legitimas y revocacion de todas las sesiones si se reutiliza
  uno ya rotado; politica de sesion por empresa (TTL, concurrentes, inactividad).
* Navegador: refresh en cookie `cf_rt` `HttpOnly; Secure; SameSite=Strict;
  Path=/api/v1/auth`, access solo en memoria (`docs/arquitectura/CSP-Y-SESION.md`).
* MFA TOTP (setup, activate, disable, reto de 5 minutos); step-up de 5 minutos para
  acciones criticas (`X-Step-Up`, `STEP_UP_MODE=enforce`).
* Revocacion instantanea: `tokens_valid_from` por usuario; el gateway rechaza cualquier
  access token emitido antes (logout-all, cambio o reinicio de contrasena, MFA
  desactivada).
* Reinicio de contrasena por enlace de un solo uso, respuesta identica exista o no el
  correo; enviado por el servicio transaccional (`TRANSACTIONAL_MAIL_URL`, P hasta que
  exista).

## 2. Roles (V)

Solo dos roles viven en codigo (`pkg/middleware/roles.go`):

| Rol | Alcance | Se siembra |
|---|---|---|
| `superadmin` | Opera la plataforma: empresas, celdas, migraciones, sesiones de cualquier empresa. No dirige ninguna empresa y no recibe sus avisos | Empresa de plataforma, por operacion |
| `tenant_admin` | Administra su empresa: usuarios, roles, dominios, politicas | `organization.RoleSeeder` al crear cada empresa, `is_system = true`, con todos los permisos de alcance `tenant` |

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
empresa, ya no basta el id.

## 3. Permisos (V)

Triple `(module, resource, action)` en `access_control.permissions`, con comodin `*` en
`resource` y `action`. Modulos de permiso: `organization`, `identity`, `access`, `audit`,
`scheduler` (plano de control, siempre disponibles) y los de correo (`domains`,
`mailboxes`, `mail_routing`, `mail_security`, `mail_storage`, `transactional`,
`templates`, `suppression`, `reputation`, `contacts`, `segments`, `campaigns`,
`automations`, `analytics`, `billing`, `policy`), que solo estan disponibles si la empresa
tiene contratado el modulo del catalogo que los agrupa (`module_catalog.permission_modules`).

Politica efectiva del usuario (`GET /api/v1/access/my-modules`):
`{is_admin, roles, modules, write_modules, write_actions, disabled_modules, tokens_valid_from}`,
en cache Redis 5 minutos por usuario y empresa, invalidada al asignar o revocar.

## 4. Tres capas de control

| Capa | Donde | Que hace | Estado |
|---|---|---|---|
| 1. Menu | `web/` | Muestra solo los modulos donde el usuario tiene algun permiso (`modules`) y consulta `can(module, resource, action)` para cada accion | P (fase 1) |
| 2. Gateway | `services/gateway/rbac.go` | Lecturas: exige que el modulo del prefijo este en `modules` (`RBAC_READ_MODE`). Escrituras: DELETE exige `delete`, PUT/PATCH `update`, POST cualquier accion de escritura del modulo (`RBAC_ENFORCE_MODE`). Modulo deshabilitado bloquea a todos, administradores incluidos. Autoservicio (`/auth`, `/sessions`, `/users/me`, `/access/my-modules`) no se gatea. Fallo: `RBAC_FAIL_MODE=closed`. Por defecto todo en `enforce` | V |
| 3. Handler | cada servicio | Exige el permiso de accion concreto ademas del gateo por modulo | V: `pkg/authz.Checker.RequirePermission(module, resource, action)` consulta la politica en access-control (`/api/v1/policy/{user}`, cache en memoria 1 minuto), deja pasar a `superadmin` y `tenant_admin`, responde 403 sin permiso y 503 si no se puede comprobar ni hay politica en cache. Lo usan `contacts` (modulos `contacts` y `segments`), `domain-service`, `mail-directory`, `mail-security`, `suppression`, `templates`, `transactional`, `campaigns` (`send` para programar, iniciar, reanudar y probar; `cancel` para pausar y cancelar; el detalle y el listado solo llevan estadisticas si el usuario tiene ademas `campaigns/stats/read`), `analytics` (`analytics/reports/read` en todas sus rutas), `automations` (`automations/workflows/{read,create,update,delete,activate}`, donde `activate` cubre tambien pausar y archivar; `automations/runs/read`; `automations/settings/{read,update}` para el doble opt-in y su historial; alcance `tenant`, `019_automations_permissions.sql`) y `billing` (que en su API de plataforma, planes y suscripciones de todas las empresas, exige ademas `RequireRoles(superadmin)`) y `reputation` (sus rutas de plataforma `/api/v1/reputation/tenants/*`, que operan sobre la base de otra empresa, exigen tambien `RequireRoles(superadmin)` por el mismo motivo con `reputation/tenants/*`). V tambien en el plano de control: `organization` exige `organization/*` (alcance plataforma, `020_control_plane_permissions.sql` anade `cells/*`); empresas, migraciones, modulos y celdas siguen ademas con `RequireRoles(superadmin)` porque operan la plataforma u otra empresa, y `reseed-roles` sigue con `RequireRoles(tenant_admin)` mas `access/roles/update` porque repara el rol de sistema, que ningun rol de empresa puede tocar. `identity` exige `identity/users/*` (`delete` para desactivar y borrar, `reset_password` para fijar la contrasena de otro), `identity/sessions/*` y `identity/session_policies/*`; la vista de sesiones de todas las empresas sigue con `RequireRoles(superadmin)` mas `identity/platform_sessions/read`; un rol de empresa con permisos sobre usuarios no edita, desactiva, borra ni reinicia a un usuario con rol del sistema (403; 503 si sus roles no se pueden leer). `access-control` comprueba en proceso con su caso de uso de politica (misma semantica, sin llamarse por HTTP): `access/roles/*`, `access/permissions/read`, `access/user_roles/*`, `access/denials/read`; la politica, los roles y el `check-access` propios son autoservicio y los de otro exigen `access/user_roles/read`; `POST /access/denials` solo lo llama la plataforma (403 si la peticion trae usuario) y `users-with-permission` pasa sin usuario (llamada interna) o con `access/user_roles/read`. Sin permiso, por ser autoservicio: `/auth/*` y MFA, `/sessions/{logout,logout-all,mine}`, la ficha y el perfil propios en `/users/{id}`, `change-password`, `password-policy` y `/access/my-modules`. V tambien `audit` y `scheduler`, cuyos datos son de la propia empresa (su base), con permisos de alcance `tenant` y sin `RequireRoles`: `audit` exige `audit/logs/read` (rastro, detalle, resumen, actividad de un usuario, cambios), `audit/logs/create` (apunte y lote), `audit/security_events/read`, `audit/security_events/acknowledge` para reconocer un evento y `audit/integrity/verify` para recorrer la cadena de hash (`audit/integrity/read` no tiene ruta); `scheduler` exige `scheduler/jobs/*` (`read`, `create`, `update` para editar, habilitar y deshabilitar, `run` para lanzar a mano), `scheduler/executions/*` (`read`, tambien el historial de un trabajo; `cancel`; `retry`) y `scheduler/tasks/*` (`read`, `create`, `cancel`) |

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
