# ADR 0011: El almacén de secretos pasa a OpenBao en el propio servidor

## Estado

Implementado y probado (2026-09-23): `ops/security/openbao/` (instalación, migración, verificación de la
instantánea), backend `openbao` en `ops/security/secrets/store.sh` y `make check-openbao` en CI, que recorre
todo el ciclo con la imagen fijada. Sustituye como fuente de producción al fichero gpg de
`docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`, que queda como backend de un servidor sin OpenBao y como
camino de vuelta.

## Contexto

La ADR 0008 sacó los secretos del `.env` a un fichero cifrado con gpg (`store.json.gpg`) y una frase en el mismo
servidor. Resolvió lo urgente —ningún contenedor recibe secretos que no son suyos— pero dejó tres huecos que un
gestor de secretos cierra y un fichero no:

* **Nadie sabe quién leyó qué.** Descifrar el fichero no deja rastro. Si una credencial aparece fuera, no hay
  forma de saber cuándo ni desde dónde se leyó el almacén.
* **Una sola llave para todo.** La frase sirve igual para desplegar (leer) que para cambiar cualquier secreto
  (escribir). Todo lo que despliega tiene, de hecho, permiso de reescribir el almacén.
* **Sin historial.** Un `add-secret.sh` con el valor equivocado reemplaza el anterior; recuperar el bueno exige
  un respaldo del fichero.

CLAUDE.md prohíbe cualquier servicio de AWS para secretos y exige que vivan en el almacén propio
(`ops/security/secrets`). La opción es un gestor autoalojado.

## Decisión

### 1. OpenBao, no Vault ni Infisical

OpenBao es el fork de HashiCorp Vault de la Linux Foundation, con licencia MPL 2.0 (Vault pasó a BSL en 2023).
Misma API y mismo modelo que Vault, el estándar de facto: KV versionado, AppRole, políticas por ruta y
auditoría. Un solo binario sin dependencias: no necesita base de datos (almacenamiento raft integrado) ni Redis,
a diferencia de Infisical. Imagen oficial `openbao/openbao:2.6.2`, fijada por digest en
`ops/security/openbao/docker-compose.yml`.

### 2. Desbloqueo automático con llave estática

OpenBao arranca sellado y necesita una llave para leer sus datos. Se eligió el sello `static` (OpenBao ≥ 2.2):
una llave de 32 bytes en `/opt/core-force-mail/secrets/openbao-llave/desbloqueo.key` (0600 del usuario que
despliega), montada de solo lectura en el contenedor. Tras un reinicio del servidor, Docker levanta OpenBao
(`restart: unless-stopped`) y se desbloquea solo: ningún despliegue queda esperando a una persona.

La alternativa, desbloqueo manual con claves de Shamir, protege frente a quien roba el disco apagado, pero deja
la plataforma sin poder desplegar ni recrear un contenedor hasta que alguien teclee las claves después de cada
reinicio. Se descartó (decisión del responsable del producto, 2026-09-23): el servidor no se reinicia solo
(`Unattended-Upgrade::Automatic-Reboot "false"`), pero cuando lo hace, la plataforma tiene que volver sin nadie.

### 3. Fuera de la plataforma, solo en loopback

* **Proyecto de Compose propio** (`core-force-mail-openbao`), fuera de `docker-compose.yml`. Es de donde salen los
  secretos del resto: no puede arrancar a través de `with-secrets.sh`, que depende de él, ni recrearse en un
  despliegue de la plataforma. No interpola ningún secreto. Lo levanta y actualiza solo
  `ops/security/openbao/instalar.sh`; `check-secret-sources.sh` lo exime por nombre, con el motivo.
* **Red del host y listener en `127.0.0.1:8200`**. Ningún contenedor de la plataforma lo alcanza: sus redes no
  llegan al loopback del host. Sin TLS, porque el tráfico no sale de la máquina y leer loopback exige root, que
  ya lee la llave. El cliente (`openbao.py`) se niega a hablar con una dirección que no sea loopback.
* **Contenedor endurecido**: el UID del usuario que despliega (nada corre como root, ni la instalación), sistema
  de ficheros de solo lectura, `cap_drop: ALL`, `no-new-privileges`, 256 MB y medio procesador.

### 4. Tres credenciales, tres permisos

AppRole, con `secret_id` y `token` atados a `127.0.0.1/32` y tokens que viven minutos (cada uso inicia sesión y
revoca su token al terminar). Las políticas están en `ops/security/openbao/politicas/`:

| Rol | Quién lo usa | Qué puede |
|---|---|---|
| `despliegue` | `fetch-secrets.sh` (todo despliegue, vía `with-secrets.sh` y `load.sh`) | Leer el documento `cf/plataforma`. Nada más. |
| `administracion` | `add-secret.sh`, `remove-secret.sh`, `rotate-key.sh`, `push-secrets.sh`, `migrar.sh`, `instalar.sh` al reaplicar | Escribir el documento, ver versiones, y mantener las políticas y roles `cf-*`. No puede montar otros motores, otros métodos de acceso ni tocar la auditoría. |
| `respaldo` | `ops/backup/backup-tenants.sh` | Sacar la instantánea de raft. No lee ningún secreto. |

El token root existe solo mientras dura la primera instalación y se revoca al terminar; si la configuración
falla a medias, queda en un fichero 0600 para que la siguiente corrida la reanude, y se borra al completarla.
La clave de recuperación (una parte, umbral uno) permite generar un root nuevo si se pierden las credenciales de
administración.

### 5. Un documento, el mismo contrato

Los secretos viven en un único documento KV v2 (`cf/plataforma`), el mismo JSON plano que el fichero gpg. Así,
`store.sh` cambia de backend sin que cambie nada por encima: `store_leer_json`/`store_escribir_json` hablan con
OpenBao o con gpg según `SECRETS_BACKEND` o el fichero `backend` junto a la frase, y `fetch-secrets.sh`,
`load.sh`, `with-secrets.sh`, `deploy-ecr.sh`, `deploy-mail.sh` y `release.yml` siguen igual. Cada escritura se
relee con la credencial de despliegue y se compara antes de darla por buena. Se conservan 30 versiones:
`openbao.py versiones` las lista y `openbao.py volver-a-version N` republica una anterior.

Un documento y no un secreto por clave: el reparto por servicio ya lo hace `reparto.tsv` en Compose
(`docs/adr/0007`), porque los contenedores no hablan con el almacén; partirlo solo multiplicaría las
peticiones por despliegue sin dar ningún permiso nuevo que se pueda usar.

### 6. Auditoría declarativa a stdout

`config.hcl.tmpl` declara un dispositivo de auditoría `file` a stdout. Declarativo porque OpenBao 2.6 no deja
crearlo por la API, lo que tiene una consecuencia buena: un token comprometido no puede apagarlo. Cada petición
queda con el rol, la ruta y el resultado; los valores y los tokens van con HMAC, nunca en claro. El registro del
contenedor se rota (10 × 50 MB) y promtail lo lleva a Loki con el resto: `{contenedor="core-force-mail-openbao"}`.

### 7. Respaldo que se comprueba

`backup-tenants.sh` saca la instantánea de raft (`openbao.snap`, con su suma) en la misma corrida que las
bases, cada seis horas, con la misma retención y, si hay destino externo, cifrada y subida con ellas. Restaurar
las bases sin las llaves que descifran las credenciales de terceros guardadas en ellas no sirve de nada.

La instantánea va cifrada por OpenBao: sin la llave de desbloqueo no se lee. `verify-restore.sh` la comprueba
cada semana con `verificar-instantanea.sh`: levanta una instancia desechable en otro puerto, restaura, y lee el
documento con la credencial de despliegue que viaja dentro. El cliente se niega a restaurar sobre una
instancia ya inicializada, así que la verificación no puede devolver la de producción al pasado.

### 8. Migración sin corte y camino de vuelta

`migrar.sh --apply` copia el documento del fichero gpg a OpenBao, lo relee, ejecuta `fetch-secrets.sh` contra
los dos backends a ficheros temporales en `/dev/shm` y los compara byte a byte. Solo si son idénticos escribe
`backend` (`openbao`) y aparta el fichero gpg como `store.json.gpg.pre-openbao`, para que un
`SECRETS_BACKEND=gpg` suelto no despliegue secretos que ya no son los vigentes. Ningún contenedor se recrea:
reciben lo mismo. `migrar.sh --volver-a-gpg --apply` hace lo inverso, regenerando el fichero desde OpenBao.

## Qué no protege

* **Root del servidor.** Lee la llave de desbloqueo y las credenciales AppRole, igual que antes leía la frase de
  gpg. Ningún almacén en la misma máquina protege de eso; para eso haría falta la llave fuera (un KMS o un
  segundo servidor con el sello `transit`), que esta decisión no toma.
* **El usuario que despliega.** Es dueño de las tres credenciales: la separación de roles acota lo que hace cada
  script y deja rastro de cada uso, no lo que puede hacer esa cuenta si se compromete.
* **El entorno de los contenedores.** Siguen recibiendo sus secretos como variables (`docker inspect` los
  muestra a quien tenga el socket de Docker), como en la ADR 0007.
* **Swap.** Con `disable_mlock` (raft lo desaconseja y el contenedor no tiene `IPC_LOCK`), una página con un
  secreto puede acabar en el swapfile, que solo lee root.

## Consecuencias

* Un proceso más en el servidor (unos 60 MB de memoria).
* Si OpenBao no responde, ningún despliegue puede materializar secretos: `fetch-secrets.sh` falla con
  "OpenBao no responde" y `load.sh` aborta, igual que con el fichero gpg ilegible. **Lo que ya está en marcha no
  se ve afectado**: los contenedores tienen sus variables desde que se crearon y Docker los reinicia sin pasar
  por el almacén. Una instantánea fallida hace fallar el respaldo y dispara `RespaldoFallido`.
* Hay que custodiar fuera del servidor **dos** cosas, además de la frase de gpg mientras exista el fichero
  apartado: la llave de desbloqueo (`base64 -w0 .../openbao-llave/desbloqueo.key`) y la clave de recuperación.
  Sin la llave, una instantánea no se puede abrir en otra máquina.
* Rotar la llave de desbloqueo: el sello `static` admite `previous_key`; el procedimiento (añadir la nueva como
  `current_key`, la anterior como `previous_key`, reiniciar, y retirar la anterior tras la siguiente
  instantánea) no está automatizado.

## Puesta en marcha en un servidor

Desde `/opt/core-force-mail/app`, como el usuario que despliega, después de desplegar los ficheros:

1. `ops/security/openbao/instalar.sh`. Copiar fuera del servidor la llave y la clave de recuperación que indica,
   y borrar la clave de recuperación del servidor.
2. `ops/security/openbao/migrar.sh` (simulación) y `ops/security/openbao/migrar.sh --apply`.
3. Comprobar: `ops/security/secrets/with-secrets.sh ops/security/secrets/verify-scope.sh entorno`, un
   despliegue normal, y `ops/backup/backup-tenants.sh` seguido de `ops/backup/verify-restore.sh`.
4. Tras unos días con OpenBao y su instantánea en el respaldo: `shred -u /opt/core-force-mail/secrets/store.json.gpg.pre-openbao`.
