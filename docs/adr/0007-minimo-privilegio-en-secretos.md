# ADR 0007: Mínimo privilegio en secretos: cada contenedor recibe solo los suyos

## Estado

Implementado y probado (2026-09-21). Sin desplegar: los contenedores en marcha siguen con el fichero de secretos entero
hasta que se recrean (sección "Despliegue"). Lo que queda abierto (ficheros por servicio, un token interno por
servicio, ACL de Redis) está en "Siguientes pasos".

## Contexto

`docker-compose.yml` daba a los 22 servicios Go el fichero de secretos materializado **entero** (`env_file:
/dev/shm/core-force-mail/secrets.env`). Solo `JWT_SIGNING_KEY`, `AUDIT_HASH_KEY` y `AUDIT_HASH_KEYS_OLD` se vaciaban a
mano en los demás, y cada servicio nuevo tenía que acordarse de hacerlo (una lista negra que se olvida y que crece
con el número de secretos por el número de servicios). Consecuencia medida en el compose: comprometer un servicio
cualquiera, por ejemplo `contacts` o `templates`, que solo necesitan el token del gateway, filtraba:

* `DOVECOT_MASTER_PASS` y `WEBMAIL_MASTER_PASSWORD`: abren **cualquier buzón de la celda**.
* `MAIL_ENCRYPTION_KEY` (y `MAIL_ENCRYPTION_KEYS_OLD`): descifran las claves DKIM y las credenciales de terceros
  (relayhosts, SES propio, orígenes de migración) que hay guardadas.
* `MAIL_LINK_SIGNING_KEY`: forja enlaces de baja y de cuarentena.
* `DOVEADM_API_KEY` y `QUEUE_AGENT_API_KEY`: revocar sesiones de Dovecot y manejar la cola de Postfix.
* `MAIL_MIGRATION_RUNNER_KEY`, `DOVECOT_MIGRATION_MASTER_PASS`, `MAIL_REDIS_PASSWORD`, `REDIS_PASSWORD`,
  `SES_ACCESS_KEY_ID` y `SES_SECRET_ACCESS_KEY`.

Las credenciales de base de datos ya se habían sacado de ese fichero (`secret-keys-db.txt`, `ops/db/service-credentials.json`,
`make check-db-credentials`): llegan solo por el `environment:` del bloque que las declara. Los motores de correo
(`deploy/mail/docker-compose.mail.yml`) ya declaraban sus variables una a una. Faltaba hacer lo mismo con el resto de la
plataforma y comprobarlo.

## Decisión

### 1. Una tabla, fuente única: `ops/security/secrets/reparto.tsv`

Una fila por (contenedor, secreto) con la evidencia de dónde lo lee (fichero y línea). Cubre los 22 servicios Go, `redis`,
`grafana` y los 10 contenedores de motores que reciben alguno. Resultado, sin contar `INTERNAL_GATEWAY_TOKEN`, que leen los 22 servicios
Go (todo servicio monta `RequireGatewayToken`, `pkg/middleware/middleware.go`):

| Contenedor | Secretos, además del token interno |
|---|---|
| identity | `JWT_SIGNING_KEY`, `MAIL_ENCRYPTION_KEY`, `MAIL_ENCRYPTION_KEYS_OLD` (secreto del segundo factor; 2026-09-24) |
| gateway | `REDIS_PASSWORD`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY` (usuario de servicio de MinIO, acotado al bucket; 2026-09-23, ADR 0012) |
| access-control, mail-auth, reputation | `REDIS_PASSWORD` |
| webmail | `REDIS_PASSWORD`, `WEBMAIL_MASTER_USER`, `WEBMAIL_MASTER_PASSWORD` |
| audit | `AUDIT_HASH_KEY`, `AUDIT_HASH_KEYS_OLD` |
| domain-service | `MAIL_ENCRYPTION_KEY`, `MAIL_ENCRYPTION_KEYS_OLD`, `SES_IDENTITIES_ACCESS_KEY_ID`, `SES_IDENTITIES_SECRET_ACCESS_KEY` |
| mail-migration | `MAIL_ENCRYPTION_KEY`, `MAIL_ENCRYPTION_KEYS_OLD`, `MAIL_MIGRATION_RUNNER_KEY` |
| mail-security | `MAIL_REDIS_PASSWORD`, `MAIL_LINK_SIGNING_KEY`, `DOVEADM_API_KEY`, `QUEUE_AGENT_API_KEY` |
| transactional | `MAIL_LINK_SIGNING_KEY`, `SES_ACCESS_KEY_ID`, `SES_SECRET_ACCESS_KEY` |
| mail-directory | `MAIL_ENCRYPTION_KEY`, `MAIL_ENCRYPTION_KEYS_OLD` (secreto de la verificacion en dos pasos de los buzones) |
| organization, scheduler, billing, mail-dav, suppression, templates, contacts, campaigns, automations, analytics | ninguno |
| redis / grafana | `REDIS_PASSWORD` / `GRAFANA_ADMIN_PASSWORD` |
| minio / minio-init (perfil autoalojado) | `MINIO_ROOT_USER`, `MINIO_ROOT_PASSWORD` / los dos y `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY` (crea el usuario de servicio) |
| dovecot-mail | `MAIL_REDIS_PASSWORD`, `DOVECOT_MASTER_USER/PASS`, `DOVECOT_MIGRATION_MASTER_USER/PASS`, `DOVEADM_API_KEY` |
| postfix-mail | `MAIL_REDIS_PASSWORD`, `QUEUE_AGENT_API_KEY` |
| mail-migration-runner | `MAIL_MIGRATION_RUNNER_KEY`, `DOVECOT_MIGRATION_MASTER_USER/PASS` |
| redis-mail, rspamd-mail, postfix-tlspol-mail, acme-mail, netfilter-mail, watchdog-mail, dockerapi-mail | `MAIL_REDIS_PASSWORD` |

`INTERNAL_SERVICE_TOKEN` no aparece: no la lee ningún código (solo estaba en `secret-keys.txt` y en `.env.example`),
así que se retira de la lista. Un secreto que el almacén materializa y nadie usa es superficie sin dueño.

El inventario sale de leer el código, no de la intuición: los literales `os.Getenv`/`config.Env*`/`crypto.LoadKeyRing`
de cada `services/<svc>`, más las lecturas que pasan por `pkg/` (`middleware.RequireGatewayToken` e
`InternalGatewayToken`, `cfg.Redis` de `pkg/config/redis.go`, `auth.SignerFromEnv` de `pkg/auth/keys.go` y, desde
2026-09-23, `objectstore.FromEnv`/`FromEnvNamespace` de `pkg/objectstore`, que leen `MINIO_ACCESS_KEY` y
`MINIO_SECRET_KEY`); en los
motores, los `entrypoint`, `syslog-ng.conf`, `redis-conf.sh` y el código Go/Python de cada uno. `config.Load` lee
`REDIS_PASSWORD` en todos los servicios, pero solo cinco abren Redis: los demás no lo reciben. Todo servicio Go que
importe `go-redis` sin `cfg.Redis` (mail-security) usa el Redis de los motores (`MAIL_REDIS_PASSWORD`).

### 2. Entrega: `environment:` explícito (opción a), sin fichero de secretos por `env_file`

Cada bloque de `docker-compose.yml` declara sus secretos como `NOMBRE: ${NOMBRE:-}`; Compose los interpola contra el
entorno que deja `with-secrets.sh` (el mismo mecanismo que ya usan las credenciales de base) y ningún contenedor recibe
`secrets.env` por `env_file`. Es una **lista blanca**: un servicio nuevo que no declara nada no recibe nada, en vez de
recibirlo todo salvo lo que se acuerde de vaciar. Se retiran los `JWT_SIGNING_KEY: ""`, `AUDIT_HASH_KEY: ""` y
`AUDIT_HASH_KEYS_OLD: ""`, que ya no tienen función. `.env` sigue por `env_file` (configuración no sensible).

Compatible con el flujo actual: `with-secrets.sh`, `fetch-secrets.sh`, `load.sh` y `scripts/deploy-*.sh` no cambian.
`e2e` pasa las variables a cada binario por su cuenta y `docker-compose.e2e.yml` ya era explícito: no se toca. En
desarrollo, sin fichero de secretos, la interpolación lee el `.env`, así que `make dev` sigue arrancando.

Dos diferencias de comportamiento, ambas inocuas y comprobadas:

* Un secreto **opcional** que el almacén no trae llega vacío en vez de ausente. El código lo lee con `os.Getenv` y trata
  la cadena vacía como no configurado (`crypto.LoadKeyRing`, `LoadMACKeyRing`, `sesclient`, `queue-agent`).
* Antes, el valor llegaba por `env_file` (literal); ahora pasa por el shell de `load.sh`, que carga el fichero con `.`.
  Un valor con espacios, comillas, `$` o `#` llegaría cambiado. Los secretos de la plataforma son hex o base64 y las
  credenciales de base ya viajaban así, pero `verify-scope.sh entorno` lo comprueba antes de recrear (sección
  "Despliegue").

### 3. Comprobación automática: `make check-secret-scope`

`ops/scaffold/check-secret-scope.sh`, en `make checks` y en CI, falla si:

* (i) un bloque de compose recibe (como variable o interpolada bajo otro nombre) un secreto que su fila no lista;
* (ii) un secreto de `secret-keys.txt` no tiene destinatario, ni siquiera `operacion` (el marcador de "solo lo lee un
  guion de ops/");
* (iii) el código de un servicio o motor lee un secreto que su fila no lista;
* además, lo contrario de (i) y (iii): una fila concede un secreto que el bloque no entrega (el servicio arrancaría sin
  su credencial) o que el código no lee (se concede de más); y cualquier bloque monta `secrets.env` por `env_file`.

Demuestra que muerde con 12 mutaciones sobre una copia del árbol (secreto ajeno en un servicio, interpolado bajo otro
nombre y en un motor; el fichero entero por `env_file`; una entrega quitada; lecturas no declaradas en un servicio y en
un motor; una fila que concede de más; un secreto sin destinatario; una fila sobre un servicio o un secreto que no
existen; una evidencia rota) y comprueba que una cita en un comentario o en un mensaje de error no cuenta como lectura.
`check-secrets.sh` pierde su regla 2 (la lista negra de `JWT_SIGNING_KEY` y `AUDIT_HASH_KEY*`), que ya no puede
cumplirse ni incumplirse; `check-db-credentials.sh` reconoce los servicios propios por su Dockerfile y no por
`secrets.env`; `make new-service` imprime el bloque nuevo con solo el token interno y su fila de reparto.

### 4. Verificación de arranque

Sobre las imágenes del commit `35fb349` (`--user 65532 --read-only --cap-drop ALL`, en una red interna con un Postgres y
un NATS desechables, `ENVIRONMENT=production`), cada servicio Go arranca con **solo** su fila y termina en los mismos
mensajes que con los 21 secretos (la base y el bus de la prueba están vacíos y Redis no existe: `relation ... does not
exist` y `lookup redis` son esperables). Como control, quitar de la fila un secreto que el servicio exige lo hace fallar con
su mensaje (`WEBMAIL_MASTER_PASSWORD`, `DOVEADM_API_KEY`, `MAIL_LINK_SIGNING_KEY`, `MAIL_ENCRYPTION_KEY`,
`JWT_SIGNING_KEY`) o degradar con su aviso (`AUDIT_HASH_KEY`), y sin ningún secreto el proceso se niega a arrancar
(`INTERNAL_GATEWAY_TOKEN is required`). Los motores no cambian: `docker compose config` con valores centinela confirma que
Dovecot, Postfix, el ejecutor de migración y el resto reciben exactamente lo de su fila. Lo que la prueba no alcanza: el
camino de Redis con TLS (mail-auth, webmail, access-control, reputation) y el `queue-agent` de Postfix, cubiertos por la
lectura del código y por `make check-secret-scope`, no por ejecución.

## Alternativas

**(b) Ficheros de secreto por servicio** (`secrets:` de Compose o montaje de un fichero 0400, y el código lee
`NOMBRE_FILE`). Es más fuerte: el valor no aparece en `docker inspect` ni en `/proc/<pid>/environ`. `pkg/config` no lo
soporta hoy (los `*_FILE` que existen son rutas de certificados), y habría que cambiar una veintena de lecturas de
`os.Getenv` en 15 servicios, `pkg/middleware` y los `entrypoint` de los motores, y resolver la propiedad de cada fichero
(imágenes `scratch` con usuario 65532, sin `uid/gid` en `secrets:` fuera de Swarm: habría que crear un fichero por
servicio con su dueño en el host). No es pequeño ni seguro de hacer aquí: se deja como siguiente paso (sección 7) con
esta ADR como base.

**Un `secrets.env` por servicio**, materializado por `fetch-secrets.sh` a partir de `reparto.tsv` y entregado por
`env_file`. Conserva la semántica literal de `env_file` (sin el shell de por medio), pero obliga a cambiar
`fetch-secrets.sh` y `load.sh`, que usan todos los despliegues, y añade N ficheros con vida propia en `/dev/shm`. Se
descartó frente a `environment:` porque este último es el patrón que las credenciales de base llevan meses usando en
producción y no toca el flujo de despliegue.

**Ampliar la lista negra** (vaciar cada secreto ajeno en cada servicio): es lo que había. Crece con el producto de
secretos por servicios y falla en silencio el día que se olvida una línea.

## Lo que esto NO protege (límites)

* **`docker inspect` y `/proc`.** Las variables de entorno son legibles por quien tenga el socket de Docker o sea root en
  el host, y por procesos del mismo usuario dentro del contenedor (`/proc/<pid>/environ`). Esta medida limita el
  **radio de un servicio comprometido** (una ejecución de código o una lectura de ficheros dentro de un contenedor ve sus
  secretos, no los de los demás); no protege frente a un atacante con el socket de Docker o root en el servidor, que ya es
  equivalente a todos los secretos. La opción (b) reduce lo segundo solo en parte (el fichero sigue en el host).
* **`INTERNAL_GATEWAY_TOKEN` lo leen los 22 servicios**, con el mismo valor: un servicio comprometido puede llamar a
  cualquier otro como si fuera el gateway (`X-Gateway-Token`) y, con él, saltarse la autenticación de esa llamada. Es
  intrínseco al diseño actual (`RequireGatewayToken`, `docs/Usuarios_Roles_y_Acceso.md`). El siguiente paso es un token por
  servicio destinatario, con el llamante identificado.
* **`REDIS_PASSWORD` comparte el Redis de la plataforma** entre cinco servicios y `MAIL_ENCRYPTION_KEY` está en cuatro: ya no
  llegan a los demás, pero entre ellos son la misma credencial. Un usuario de ACL de Redis por servicio es el siguiente
  paso para Redis.
* **El `.env` sigue por `env_file` a todos.** En producción no lleva credenciales (la fuente es el almacén,
  `check-secrets.sh` vigila el repositorio y `require-server-environment.sh` avisa de los marcadores `CHANGE_ME`); si el
  almacén no responde, `load.sh` cae al `.env` **solo** mientras este conserve las credenciales
  obligatorias (migración pendiente), y en ese caso llegarían a todos los servicios. En desarrollo, el `.env` local tiene
  los secretos y los ve todo contenedor: es un entorno desechable.
* **El proceso de despliegue** (`with-secrets.sh`) tiene todos los secretos en su entorno mientras corre `docker compose`.
* **Sin `with-secrets.sh` un `docker compose up` arranca sin secretos y sin aviso** (`${VAR:-}` no avisa). Ya era así con
  `required: false`; los servicios que exigen su secreto no arrancan (fallan cerrado) y `check-secret-sources.sh` vigila
  que el servidor use el envoltorio. Lo que arranca degradado sin ellos (`audit` sin `AUDIT_HASH_KEY`) sigue avisando en el
  arranque.

## Despliegue

Este cambio solo toca `docker-compose.yml`, `ops/` y documentos: `scripts/deploy-ecr.sh` sin argumentos lo detecta como
"solo ficheros", los sincroniza y **no recrea ningún servicio**. Los contenedores en marcha siguen con el fichero entero
hasta que se recrean; recrear un servicio cualquiera con la nueva tabla es seguro por separado (cada fila está completa),
así que un estado intermedio es válido y `verify-scope.sh contenedores` dice quién falta. En el servidor:

1. Tras el despliegue de ficheros: `ops/security/secrets/with-secrets.sh ops/security/secrets/verify-scope.sh entorno`.
   Debe decir que el entorno coincide con el fichero materializado; si nombra una variable, cambiar ese valor en el
   almacén antes de seguir.
2. Recrear los servicios Go de la plataforma: `scripts/deploy-ecr.sh` con la lista de los 22 servicios explícita
   (`ops/scaffold/service-paths.sh --class go`), que pasa por la guardia de retroceso, `esperar-sanos.sh` y la
   verificación de imagen. Se puede hacer en lotes; el orden entre servicios da igual porque cada uno arranca con lo suyo
   completo, pero conviene dejar `gateway` e `identity` para el final. `redis` no se recrea (su entorno no cambia) y los
   motores tampoco: ya recibían solo lo suyo.
3. `ops/security/secrets/verify-scope.sh contenedores` (sin secretos, solo `docker`): cada servicio debe salir `OK`. Un
   `recibe de mas` es un contenedor sin recrear; un `le faltan` es un obligatorio vacío.
4. Si se prefiere no reconstruir imágenes, recrear en el servidor con la etiqueta que corre cada servicio (`docker compose
   ... up -d --no-deps --no-build <svc>` con `DEPLOY_TAG` de ese servicio, como hace el despliegue): es lo mismo sin build.

Añadir un secreto nuevo: la variable en `secret-keys.txt`, su valor en el almacén, una fila por contenedor que la lea en
`reparto.tsv` y la línea `NOMBRE: ${NOMBRE:-}` en el `environment:` de cada uno; `make check-secret-scope` no deja
olvidar ninguna de las tres partes. Rotar uno: publicar y recrear **solo** los contenedores de su fila.

## Siguientes pasos (no implementados)

1. Ficheros de secreto por servicio con `NOMBRE_FILE` (opción b), empezando por `pkg/config` y `pkg/middleware`.
2. Token interno por servicio destinatario en lugar de uno global.
3. Usuario de ACL de Redis por servicio (`REDIS_PASSWORD`) y de cada motor (`MAIL_REDIS_PASSWORD`).
4. Retirar el respaldo de `load.sh` al `.env` cuando todos los despliegues tengan el almacén puesto
   (`docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`: el almacén ya existe como fichero cifrado
   local; falta que el servidor real lo tenga inicializado y el `.env` migrado).
