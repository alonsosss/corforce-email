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
| `fetch-secrets.sh` | Materializa los secretos en `/dev/shm/core-force-mail/secrets.env` (memoria, 0600). Atomico y todo-o-nada. |
| `with-secrets.sh` | Envoltorio: materializa, carga al entorno y ejecuta el comando (lo usan los despliegues). |
| `push-secrets.sh` | Migracion de una sola vez: sube el contenido del `.env` al almacen y deja el `.env` sin credenciales. |
| `add-secret.sh` | Anade o actualiza UNA clave. Es la via para introducir un secreto nuevo despues de la migracion, cuando el `.env` ya no tiene las demas y `push-secrets.sh` se niega. El valor no viaja por la linea de comandos. |
| `load.sh` | Resolvedor sourceable: materializa y carga los secretos al entorno. Lo usan `with-secrets.sh` y los trabajos que necesitan una credencial para si mismos (respaldo, migraciones). |
| `check-secrets.sh` | Guardarrail de CI: falla si una credencial canonica tiene valor en un fichero versionado. |
| `check-secret-sources.sh` | Guardarrail de CI: falla si un script se busca un secreto en el `.env`, o si un `docker compose` que crea contenedores no va por `with-secrets.sh`. |
| `iam-policy.json` | Politica de LECTURA para el rol de la instancia (estado permanente). |
| `iam-policy-migracion.json` | Politica de ESCRITURA, solo para la migracion inicial. Se retira despues. |

## Como lo consumen los servicios

`docker-compose.yml` da a cada servicio dos ficheros de entorno: `.env` (configuracion no
sensible) y `/dev/shm/core-force-mail/secrets.env` con `required: false`. En desarrollo ese
segundo fichero no existe y Compose lo omite, de modo que levantar en local sigue
necesitando solo `.env`.

Los despliegues invocan `with-secrets.sh` porque hacen falta las dos vias: `env_file`
alimenta al contenedor, pero la interpolacion de `${VAR}` dentro de `docker-compose.yml` la
resuelve Compose contra el entorno del proceso.

> **En produccion, todo `docker compose` que cree o recree contenedores va por
> `with-secrets.sh`,** y `check-secret-sources.sh` lo comprueba en cada PR: la precaucion
> dejo de depender de que alguien se acuerde.
>
> **Fuera de los contenedores, una credencial se obtiene por el resolvedor comun**
> (`ops/db/pg-credentials.sh` para Postgres, `load.sh` para el resto) y nunca leyendo el
> `.env`. Tres scripts de respaldo lo hacian y se quedaron con la variable vacia el dia de
> la migracion: el respaldo nocturno habria abortado con un mensaje que no apuntaba a la
> causa. Desde que el `.env` no tiene credenciales, un `docker compose up`
> suelto interpola cadenas vacias y levanta el servicio SIN secretos, avisando solo con un
> `warning` facil de pasar por alto. Los tres caminos de despliegue (`deploy-fast.sh`,
> `deploy-ecr.sh` y `release.yml`) ya lo hacen; la precaucion es para el uso manual.
> Consultar estado (`ps`, `logs`) es seguro sin el envoltorio: solo molestan los avisos.

## Que NO va al almacen

El almacen resuelve donde viven los secretos de arranque, no todos los secretos.

- Las **credenciales de terceros por empresa** (relayhosts, claves DKIM, proveedores de envio,
  Meta) viven cifradas en la base de datos y se administran desde el panel de cada modulo.
  Aqui solo esta la llave que las descifra.
- Esa llave, y las demas llaves de cifrado, **no pueden guardarse en la base de datos**: son
  justamente las que la descifran. Tampoco `POSTGRES_PASSWORD`, que da acceso a la base.
  Siempre queda un piso minimo fuera de la aplicacion, y este almacen es ese piso.

## Puesta en marcha

**Hecha en produccion el 2026-08-02**: `core-force-mail/prod` existe con 18 secretos, el `.env`
del servidor ya no tiene credenciales y el rol de la instancia quedo en solo lectura. Lo que
sigue queda como referencia para otro entorno.

1. **Permiso de escritura temporal.** Adjuntar `iam-policy-migracion.json` al rol
   `core-force-mail-ec2-role`. La escritura solo hace falta para crear el secreto: desplegar
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
3. **Retirar el permiso de escritura**, dejando solo `iam-policy.json`.
4. **Verificar.** `fetch-secrets.sh` debe reportar los secretos materializados; recrear un
   servicio via `with-secrets.sh` debe levantarlo sin errores; y con el almacen inaccesible
   (`SECRETS_ID=core-force/no-existe`) el envoltorio debe **abortar**, no continuar.
5. **Borrar la copia de rescate:** `shred -u .env.pre-secrets.*`.

## Rotar un secreto

1. Publicar el valor nuevo en el almacen (consola de AWS o `put-secret-value`).
2. Volver a levantar los servicios afectados: `with-secrets.sh docker compose up -d --no-deps <servicios>`.

Las variables de entorno se fijan al crear el contenedor: reiniciarlo no basta, hay que
recrearlo. Las llaves de cifrado tienen ademas su propio procedimiento de re-cifrado de
datos existentes (ver ops/security/secrets/rotate-key.sh).

## Anadir un secreto nuevo

1. Anadir la variable a `secret-keys.txt`.
2. Publicar su valor en el almacen.
3. Usarla en el servicio como cualquier variable de entorno.

Si falta el paso 2, `fetch-secrets.sh` falla en vez de escribir un fichero incompleto: un
servicio con la credencial vacia falla de formas mucho mas dificiles de diagnosticar.
