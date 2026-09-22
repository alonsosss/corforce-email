# Secretos de produccion

Las credenciales, llaves de cifrado y tokens de la plataforma **no viven en ningun fichero
del repositorio ni en el `.env` del servidor**. Su fuente unica es un almacen cifrado local
(`store.json.gpg`, gpg simetrico); en el servidor se materializan en memoria solo el tiempo
necesario para levantar los contenedores.

## Por que

El `.env` del servidor concentraba mas de treinta credenciales de produccion (llave de
firma de sesiones, contrasena de la base, llaves de cifrado de credenciales de terceros y de
canales, tokens de pasarelas de pago) en un fichero de texto plano dentro del directorio de
despliegue. Tres consecuencias: cualquiera con acceso de lectura al host las obtiene todas,
no hay registro de quien las leyo, y rotar una obliga a editar el fichero a mano en cada
maquina.

Con el almacen de secretos: la unica credencial que da acceso a todas las demas es la frase
de descifrado, que vive en un fichero aparte del `.env` y fuera del arbol que el despliegue
sincroniza al servidor; y rotar una clave es reescribir un JSON cifrado, no editar un
fichero de texto en produccion.

Este almacen sustituyo a AWS Secrets Manager (`docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`):
la cuenta de AWS del proyecto nunca llego a existir y, sin el CLI de AWS instalado en el
servidor, esa via fallaba siempre; `load.sh` caia entonces a su respaldo documentado -las
credenciales del `.env`- y el reparto de minimo privilegio de
`docs/adr/0007-minimo-privilegio-en-secretos.md` no protegia nada en ese servidor. CLAUDE.md
prohibe ademas cualquier infraestructura de AWS que gestione secretos en este proyecto.

## Piezas

| Fichero | Que hace |
|---|---|
| `secret-keys.txt` | Lista canonica de variables que SON secreto. La usan los scripts y el guardarrail de CI. |
| `reparto.tsv` | Quien recibe cada secreto de `secret-keys.txt`: una fila (contenedor, secreto, evidencia de donde lo lee) por entrega. Ningun contenedor recibe el fichero entero, solo lo que aqui se le da (`docs/adr/0007-minimo-privilegio-en-secretos.md`). |
| `secret-keys-db.txt` | Credenciales de BASE, con el mismo formato. Se publican en el mismo almacen que `secret-keys.txt` pero se materializan en un fichero aparte (ver "Como lo consumen los servicios"). |
| `secret-keys-backup.txt` | Secretos del RESPALDO (credencial del bucket externo y frase de cifrado). Ningun contenedor los recibe: los leen solo los trabajos de `ops/backup` del entorno o de `BACKUP_SECRETS_FILE` (`ops/backup/README.md`). No pasan por este almacen: viven en `BACKUP_SECRETS_FILE`, fuera del `.env`. Para `check-secrets.sh` valen igual que los demas. |
| `store.sh` | Resuelve el almacen cifrado: descifra y cifra `store.json.gpg` con gpg simetrico y la frase de `SECRETS_STORE_PASSPHRASE_FILE`. Lo sourcean los cinco scripts de abajo. |
| `init-store.sh` | Crea el almacen (vacio, o migrando un `.env` con `--from-env`) y, si hace falta, genera la frase. Un solo uso por servidor. |
| `fetch-secrets.sh` | Materializa los secretos en `/dev/shm/core-force-mail/secrets.env` y `secrets-db.env` (memoria, 0600). Atomico y todo-o-nada. |
| `with-secrets.sh` | Envoltorio: comprueba el entorno declarado, materializa, carga al entorno y ejecuta el comando (lo usan los despliegues). |
| `require-server-environment.sh` | Guarda que `with-secrets.sh` ejecuta antes que nada: si el `.env` del servidor no dice exactamente `ENVIRONMENT=production` o `staging`, no se despliega (`docs/Operacion_Despliegue.md`, 1). |
| `push-secrets.sh` | Migracion de una sola vez: sube el contenido del `.env` (las dos listas, `secret-keys.txt` y `secret-keys-db.txt`) al almacen y deja el `.env` sin credenciales. |
| `add-secret.sh` | Anade o actualiza UNA clave. Es la via para introducir un secreto nuevo despues de la migracion, cuando el `.env` ya no tiene las demas y `push-secrets.sh` se niega. El valor no viaja por la linea de comandos. |
| `rotate-key.sh` | Rota una llave de cifrado (activa + lista de retiradas) sin dejar ilegible lo ya cifrado con la anterior. |
| `load.sh` | Resolvedor sourceable: materializa y carga los secretos al entorno. Lo usan `with-secrets.sh` y los trabajos que necesitan una credencial para si mismos (respaldo, migraciones). |
| `check-secrets.sh` | Guardarrail de CI: falla si una credencial canonica tiene valor en un fichero versionado, y si un `.env.example` fija cualquier valor (ni siquiera un placeholder) para una clave del almacen. |
| `check-secret-sources.sh` | Guardarrail de CI: falla si un script se busca un secreto en el `.env`, o si un `docker compose` que crea contenedores no va por `with-secrets.sh`. |
| `check-secrets-store.sh` | Prueba de extremo a extremo del almacen (`make check-secrets-store`): init, fetch, add, rotate y push, con gpg real en un directorio temporal, incluidas la frase incorrecta y el fichero corrompido. |
| `verify-scope.sh` | Operacion, solo imprime nombres: `entorno` (bajo `with-secrets.sh`) comprueba que el valor que Compose interpola es el del fichero materializado; `contenedores` compara lo que recibe cada contenedor en marcha con su fila de `reparto.tsv`. |

El reparto lo comprueba `ops/scaffold/check-secret-scope.sh` (`make check-secret-scope`, dentro de `make checks`), que ata
`reparto.tsv` al compose, al codigo y a `secret-keys.txt` y demuestra con mutaciones que muerde.

Tras validar `ENVIRONMENT`, el mismo guion rechaza el `.env` de un servidor que conserve el
marcador `YOUR_DOMAIN` de `.env.example` (acabaria en los registros DNS que se indican a las
empresas, en los enlaces y en los origenes permitidos) y avisa, sin bloquear, de los `CHANGE_ME`:
los secretos del almacen tienen prioridad, pero si no responde el resolvedor recurre al `.env`.
Solo nombra claves, nunca valores (`ops/scaffold/check-deploy-preflight.sh`).

## El almacen: un fichero cifrado, no un servicio administrado

`store.json.gpg` es un JSON plano (`{"CLAVE": "valor", ...}`) con todas las filas de
`secret-keys.txt` y `secret-keys-db.txt`, cifrado con gpg simetrico: AES-256,
`--s2k-digest-algo SHA512 --s2k-count 65011712`, la frase por `--passphrase-fd 0` (nunca un
argumento visible en `ps`). Es el mismo patron, ya auditado, que usa
`ops/backup/destino-externo.sh` para cifrar los respaldos que salen del servidor: no se anade
ninguna dependencia nueva (`gnupg` ya lo instala `ops/server-template/bootstrap.sh`).

Dos ficheros, ambos fuera de git:

* `SECRETS_STORE_FILE` (por defecto `store.json.gpg` en el mismo directorio que la frase:
  `/opt/core-force-mail/secrets/store.json.gpg`). Fuera del arbol de la aplicacion y de git. No
  vive junto a estos scripts a proposito: el servidor tiene DOS copias de `ops/security/secrets`
  (la de la plataforma en `/opt/core-force-mail/app` y la de los motores en
  `/opt/core-force-mail/mail-src`, que rellena `scripts/deploy-mail.sh` y desde la que ejecuta
  `with-secrets.sh`); un almacen relativo al script solo existia en una y el despliegue de un
  motor abortaba con "no existe el almacen". Junto a la frase hay una sola copia para las dos y
  ningun rsync ni `git archive` la toca.
* `SECRETS_STORE_PASSPHRASE_FILE` (por defecto `/opt/core-force-mail/secrets/passphrase`), la
  frase de descifrado, 0600 del usuario que despliega, **fuera** del arbol de la aplicacion
  (`/opt/core-force-mail/app`) y fuera del `.env`. Es el UNICO acceso a los secretos: perderla es
  perder el almacen entero, ni siquiera con acceso root al servidor. `init-store.sh` la genera
  con `openssl rand -base64 48` si no existe, y hay que copiarla a un gestor de contrasenas del
  equipo o a otro sitio fuera de este servidor -y fuera de cualquier respaldo cifrado con esa
  misma frase, para no crear una dependencia circular: si el servidor y ese respaldo se
  pierden juntos, la copia de la frase dentro del respaldo tampoco se puede leer-.

## Como lo consumen los servicios

`docker-compose.yml` da a cada servicio `.env` por `env_file` (configuracion no sensible) y, en su
`environment:`, SOLO los secretos de su fila de `reparto.tsv`, cada uno como `NOMBRE: ${NOMBRE:-}`. El
fichero `/dev/shm/core-force-mail/secrets.env` no llega por `env_file` a ningun contenedor: Compose interpola
cada `${NOMBRE}` contra el entorno que deja `with-secrets.sh`, que es lo unico que lo lee. En desarrollo,
sin ese fichero, la interpolacion lee el `.env`, de modo que levantar en local sigue necesitando solo `.env`.

Es lo que hacen desde antes las credenciales de base de datos (`secret-keys-db.txt`) y los motores de
`deploy/mail`. Un servicio comprometido ve sus secretos y no los de los demas; no protege frente a quien
tenga el socket de Docker o sea root del host (`docker inspect` muestra el entorno).

> **Esto depende por completo de que el `.env` no lleve ningun secreto.** `env_file: .env` vuelca el
> fichero ENTERO al contenedor sin mirar lo que declara `environment:`; ese bloque solo controla que
> interpola Compose. Si una credencial de `secret-keys.txt` o `secret-keys-db.txt` tuviera valor en el
> `.env` del servidor -por ejemplo, porque el almacen nunca se llego a poblar-, `env_file` la reparte
> a los 22 servicios igual, y el reparto de `reparto.tsv` deja de proteger nada. Es exactamente lo que
> pasaba con AWS Secrets Manager nunca disponible (`docs/adr/0008`): la comprobacion de esto es
> `verify-scope.sh contenedores` (mas abajo) y el guardarrail nuevo de `check-secrets.sh` sobre
> `.env.example`.
>
> **En produccion, todo `docker compose` que cree o recree contenedores va por
> `with-secrets.sh`,** y `check-secret-sources.sh` lo comprueba en cada PR: la precaucion
> dejo de depender de que alguien se acuerde.
>
> **Fuera de los contenedores, una credencial se obtiene por el resolvedor comun**
> (`ops/db/pg-credentials.sh` para Postgres, `load.sh` para el resto) y nunca leyendo el
> `.env`. Tres scripts de respaldo lo hacian y se quedaron con la variable vacia el dia de
> la migracion: el respaldo nocturno habria abortado con un mensaje que no apuntaba a la
> causa. Desde que el `.env` no tiene credenciales, un `docker compose up`
> suelto interpola cadenas vacias y levanta el servicio SIN secretos y sin ningun aviso (`${VAR:-}`
> no lo emite); los servicios que exigen su secreto no arrancan, y `verify-scope.sh contenedores` lo delata. Los dos caminos de despliegue (`scripts/deploy-ecr.sh` y
> `release.yml`) ya lo hacen; la precaucion es para el uso manual.
> Consultar estado (`ps`, `logs`) es seguro sin el envoltorio.
>
> **Un secreto tampoco viaja como argumento de `psql`.** Los guiones de `ops/db` que fijan una
> contrasena (el verificador SCRAM de un rol, la contrasena del primer superadmin) la pasan por el
> entorno con `cf_pg_pasar_entorno` y el SQL la lee con `\getenv`: con `-v` quedaria en `ps` y, en
> el perfil autoalojado, en `docker inspect` del contenedor efimero que alcanza la base
> (`ops/backup/README.md`). Lo comprueba `ops/scaffold/check-backups.sh`.

## Que NO va al almacen

El almacen resuelve donde viven los secretos de arranque, no todos los secretos.

- Las **credenciales de terceros por empresa** (relayhosts, claves DKIM, proveedores de envio,
  Meta) viven cifradas en la base de datos y se administran desde el panel de cada modulo.
  Aqui solo esta la llave que las descifra.
- Esa llave, y las demas llaves de cifrado, **no pueden guardarse en la base de datos**: son
  justamente las que la descifran. Tampoco `POSTGRES_PASSWORD`, que da acceso a la base.
  Siempre queda un piso minimo fuera de la aplicacion, y este almacen es ese piso.
- Los **secretos del respaldo** (`secret-keys-backup.txt`): viven en `BACKUP_SECRETS_FILE`, no en
  este almacen (`ops/backup/README.md`).

## Puesta en marcha (un servidor nuevo)

1. **Crear el almacen.** En el servidor, desde `/opt/core-force-mail/app`:

   ```bash
   ops/security/secrets/init-store.sh
   ```

   Sin frase previa en `/opt/core-force-mail/secrets/passphrase`, la genera y la imprime la
   RUTA (nunca el valor) con el aviso de copiarla fuera del servidor. Hacerlo antes de seguir:
   sin esa copia, perder el disco del servidor es perder los secretos.

2. **Migrar los valores actuales.**

   ```bash
   ops/security/secrets/push-secrets.sh .env            # simulacion: lista claves, no toca nada
   ops/security/secrets/push-secrets.sh .env --apply    # sube, verifica y limpia el .env
   ```

   Se ejecuta EN EL SERVIDOR a proposito: los valores nunca salen de la maquina donde ya
   estaban. El script no reescribe el `.env` hasta releer el almacen y comprobar valor por
   valor que coincide con lo enviado. Deja una copia de rescate `.env.pre-secrets.<fecha>`
   con permisos 0600. Migra las DOS listas (`secret-keys.txt` y `secret-keys-db.txt`): antes de
   esto, ninguna credencial de base sale del `.env` aunque las demas ya esten en el almacen.

3. **Verificar.** `fetch-secrets.sh` debe reportar los secretos materializados; recrear un
   servicio via `with-secrets.sh` debe levantarlo sin errores; y con el almacen inaccesible
   (`SECRETS_STORE_FILE=/no/existe`) el envoltorio debe **abortar**, no continuar en silencio.
4. **Borrar la copia de rescate:** `shred -u .env.pre-secrets.*`.
5. **Recrear los contenedores y comprobar el aislamiento:**
   `ops/security/secrets/verify-scope.sh contenedores` debe dar `OK` en todos, no `recibe de mas`.

Procedimiento detallado para migrar un servidor que ya esta en marcha (sin cortar el
servicio), incluida la lista de contenedores a recrear y en que orden:
`docs/adr/0008-almacen-de-secretos-cifrado-sin-aws.md`, seccion "Migración del servidor real"
del historial de esta tarea, y `docs/Operacion_Despliegue.md`, seccion 2.

## Rotar un secreto

1. Publicar el valor nuevo en el almacen: `ops/security/secrets/add-secret.sh CLAVE --apply`
   (el valor por `VALOR=...` o `--desde-env`, nunca por argumento).
2. Volver a levantar los servicios afectados (los de su fila en `reparto.tsv`, ninguno mas): `with-secrets.sh docker compose up -d --no-deps <servicios>`.

Las variables de entorno se fijan al crear el contenedor: reiniciarlo no basta, hay que
recrearlo. Las llaves de cifrado tienen ademas su propio procedimiento de re-cifrado de
datos existentes (ver `ops/security/secrets/rotate-key.sh`). La clave de firma del token de
acceso (`JWT_SIGNING_KEY`) tampoco se rota asi: tiene una clave publica que se publica
antes que ella y un orden propio (`ops/security/jwt-keygen.sh`,
`docs/Operacion_Despliegue.md` 2). Rotar la FRASE del almacen (distinta de las llaves de
cifrado de arriba) tiene su propio procedimiento en `docs/adr/0008`.

## Anadir un secreto nuevo

1. Anadir la variable a `secret-keys.txt` (o `secret-keys-db.txt` si es una credencial de base).
2. Publicar su valor en el almacen con `add-secret.sh`.
3. Una fila por contenedor que la lea en `reparto.tsv` (evidencia: fichero y linea donde la lee) y la linea
   `NOMBRE: ${NOMBRE:-}` en el `environment:` de ese contenedor. Sin ella no le llega: ya no hay `env_file` de
   secretos. `make check-secret-scope` falla si falta cualquiera de las tres partes.

Si falta el paso 2, `fetch-secrets.sh` falla en vez de escribir un fichero incompleto: un
servicio con la credencial vacia falla de formas mucho mas dificiles de diagnosticar.
