# Secretos de produccion

Las credenciales, llaves de cifrado y tokens de la plataforma **no viven en ningun fichero
del repositorio ni en el `.env` del servidor**. Su fuente unica es AWS Secrets Manager; en
el servidor se materializan en memoria solo el tiempo necesario para levantar los
contenedores.

## Por que

El `.env` del servidor concentraba mas de treinta credenciales de produccion (llave de
firma de sesiones, contrasena de la base, llaves de cifrado de credenciales de terceros y de
canales, tokens de pasarelas de pago) en un fichero de texto plano dentro del directorio de
despliegue. Tres consecuencias: cualquiera con acceso de lectura al host las obtiene todas,
no hay registro de quien las leyo, y rotar una obliga a editar el fichero a mano en cada
maquina.

Con el almacen de secretos: la identidad la aporta el rol IAM de la instancia (no hay
credencial de AWS en el servidor), cada lectura queda en CloudTrail, y rotar es publicar una
version nueva.

## Piezas

| Fichero | Que hace |
|---|---|
| `secret-keys.txt` | Lista canonica de variables que SON secreto. La usan los tres scripts y el guardarrail de CI. |
| `reparto.tsv` | Quien recibe cada secreto de `secret-keys.txt`: una fila (contenedor, secreto, evidencia de donde lo lee) por entrega. Ningun contenedor recibe el fichero entero, solo lo que aqui se le da (`docs/adr/0007-minimo-privilegio-en-secretos.md`). |
| `secret-keys-backup.txt` | Secretos del RESPALDO (credencial del bucket externo y frase de cifrado). Ningun contenedor los recibe: los leen solo los trabajos de `ops/backup` del entorno o de `BACKUP_SECRETS_FILE` (`ops/backup/README.md`). Para `check-secrets.sh` valen igual que los demas. |
| `fetch-secrets.sh` | Materializa los secretos en `/dev/shm/core-force-mail/secrets.env` (memoria, 0600). Atomico y todo-o-nada. |
| `with-secrets.sh` | Envoltorio: comprueba el entorno declarado, materializa, carga al entorno y ejecuta el comando (lo usan los despliegues). |
| `require-server-environment.sh` | Guarda que `with-secrets.sh` ejecuta antes que nada: si el `.env` del servidor no dice exactamente `ENVIRONMENT=production` o `staging`, no se despliega (`docs/Operacion_Despliegue.md`, 1). |
| `push-secrets.sh` | Migracion de una sola vez: sube el contenido del `.env` al almacen y deja el `.env` sin credenciales. |
| `add-secret.sh` | Anade o actualiza UNA clave. Es la via para introducir un secreto nuevo despues de la migracion, cuando el `.env` ya no tiene las demas y `push-secrets.sh` se niega. El valor no viaja por la linea de comandos. |
| `load.sh` | Resolvedor sourceable: materializa y carga los secretos al entorno. Lo usan `with-secrets.sh` y los trabajos que necesitan una credencial para si mismos (respaldo, migraciones). |
| `check-secrets.sh` | Guardarrail de CI: falla si una credencial canonica tiene valor en un fichero versionado. |
| `check-secret-sources.sh` | Guardarrail de CI: falla si un script se busca un secreto en el `.env`, o si un `docker compose` que crea contenedores no va por `with-secrets.sh`. |
| `verify-scope.sh` | Operacion, solo imprime nombres: `entorno` (bajo `with-secrets.sh`) comprueba que el valor que Compose interpola es el del fichero materializado; `contenedores` compara lo que recibe cada contenedor en marcha con su fila de `reparto.tsv`. |

El reparto lo comprueba `ops/scaffold/check-secret-scope.sh` (`make check-secret-scope`, dentro de `make checks`), que ata
`reparto.tsv` al compose, al codigo y a `secret-keys.txt` y demuestra con mutaciones que muerde.

Tras validar `ENVIRONMENT`, el mismo guion rechaza el `.env` de un servidor que conserve el
marcador `YOUR_DOMAIN` de `.env.example` (acabaria en los registros DNS que se indican a las
empresas, en los enlaces y en los origenes permitidos) y avisa, sin bloquear, de los `CHANGE_ME`:
los secretos del almacen tienen prioridad, pero si no responde el resolvedor recurre al `.env`.
Solo nombra claves, nunca valores (`ops/scaffold/check-deploy-preflight.sh`).

Los permisos del rol de la instancia sobre el almacen no viven aqui: los declara
`ops/aws/setup-iam.sh`, que los renderiza con la cuenta de quien lo ejecuta, `AWS_REGION` y
`SECRETS_PREFIX` (por defecto `core-force-mail`, el prefijo de `SECRETS_ID`).
`core-force-mail-secretos` es la lectura permanente; `core-force-mail-secretos-escritura`, la
escritura que piden `push-secrets.sh`, `add-secret.sh` y `rotate-key.sh`, solo existe
mientras se corre con `--escritura-secretos`, y la siguiente corrida sin la opcion la retira.

## Como lo consumen los servicios

`docker-compose.yml` da a cada servicio `.env` por `env_file` (configuracion no sensible) y, en su
`environment:`, SOLO los secretos de su fila de `reparto.tsv`, cada uno como `NOMBRE: ${NOMBRE:-}`. El
fichero `/dev/shm/core-force-mail/secrets.env` no llega por `env_file` a ningun contenedor: Compose interpola
cada `${NOMBRE}` contra el entorno que deja `with-secrets.sh`, que es lo unico que lo lee. En desarrollo,
sin ese fichero, la interpolacion lee el `.env`, de modo que levantar en local sigue necesitando solo `.env`.

Es lo que hacen desde antes las credenciales de base de datos (`secret-keys-db.txt`) y los motores de
`deploy/mail`. Un servicio comprometido ve sus secretos y no los de los demas; no protege frente a quien
tenga el socket de Docker o sea root del host (`docker inspect` muestra el entorno).

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

## Puesta en marcha

Pendiente en todos los entornos: la cuenta de AWS del proyecto aun no existe. Por entorno:

1. **Permiso de escritura temporal.** `ops/aws/setup-iam.sh --escritura-secretos`, con
   credenciales de administrador, concede al rol `core-force-mail-ec2-role` la escritura en
   `<SECRETS_PREFIX>/*`. La escritura solo hace falta para crear el secreto: desplegar
   necesita unicamente lectura, asi que el permiso se retira en cuanto termina el paso 2.
2. **Subir los valores actuales.** En el servidor, desde `/opt/core-force-mail/app`:

   ```bash
   ops/security/secrets/push-secrets.sh .env            # simulacion: lista claves, no toca nada
   ops/security/secrets/push-secrets.sh .env --apply    # sube, verifica y limpia el .env
   ```

   Se ejecuta EN EL SERVIDOR a proposito: los valores nunca salen de la maquina donde ya
   estaban. El script no reescribe el `.env` hasta releer el secreto y comprobar valor por
   valor que coincide con lo enviado. Deja una copia de rescate `.env.pre-secrets.<fecha>`
   con permisos 0600.
3. **Retirar el permiso de escritura:** `ops/aws/setup-iam.sh` sin la opcion deja solo la
   lectura.
4. **Verificar.** `fetch-secrets.sh` debe reportar los secretos materializados; recrear un
   servicio via `with-secrets.sh` debe levantarlo sin errores; y con el almacen inaccesible
   (`SECRETS_ID=core-force-mail/no-existe`) el envoltorio debe **abortar**, no continuar.
5. **Borrar la copia de rescate:** `shred -u .env.pre-secrets.*`.

## Rotar un secreto

1. Publicar el valor nuevo en el almacen (consola de AWS o `put-secret-value`).
2. Volver a levantar los servicios afectados (los de su fila en `reparto.tsv`, ninguno mas): `with-secrets.sh docker compose up -d --no-deps <servicios>`.

Las variables de entorno se fijan al crear el contenedor: reiniciarlo no basta, hay que
recrearlo. Las llaves de cifrado tienen ademas su propio procedimiento de re-cifrado de
datos existentes (ver ops/security/secrets/rotate-key.sh). La clave de firma del token de
acceso (`JWT_SIGNING_KEY`) tampoco se rota asi: tiene una clave publica que se publica
antes que ella y un orden propio (`ops/security/jwt-keygen.sh`,
`docs/Operacion_Despliegue.md` 2).

## Anadir un secreto nuevo

1. Anadir la variable a `secret-keys.txt`.
2. Publicar su valor en el almacen.
3. Una fila por contenedor que la lea en `reparto.tsv` (evidencia: fichero y linea donde la lee) y la linea
   `NOMBRE: ${NOMBRE:-}` en el `environment:` de ese contenedor. Sin ella no le llega: ya no hay `env_file` de
   secretos. `make check-secret-scope` falla si falta cualquiera de las tres partes.

Si falta el paso 2, `fetch-secrets.sh` falla en vez de escribir un fichero incompleto: un
servicio con la credencial vacia falla de formas mucho mas dificiles de diagnosticar.
