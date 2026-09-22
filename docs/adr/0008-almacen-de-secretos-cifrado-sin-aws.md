# ADR 0008: Almacén de secretos cifrado local, sin AWS Secrets Manager

## Estado

Implementado y probado en el repositorio (2026-09-21): scripts, guardarraíl y prueba de extremo a
extremo en un directorio temporal con gpg real. Sin desplegar: el servidor real sigue con el `.env`
como única fuente hasta que se corra `init-store.sh` y `push-secrets.sh` (sección "Migración del
servidor real", en el informe de esta tarea, y resumida en el README de este directorio).

## Contexto

`ops/security/secrets/fetch-secrets.sh` leía el almacén con `aws secretsmanager get-secret-value`.
CLAUDE.md prohíbe cualquier infraestructura de AWS que gestione secretos en este proyecto
("Infraestructura: servidor propio en Netcup, autoadministrado... sin Secrets Manager de AWS") y el
propio README de este directorio decía que la cuenta de AWS del proyecto **nunca llegó a existir**
("Pendiente en todos los entornos"). En el servidor real, sin CLI de AWS instalado, esa llamada
falla siempre. `load.sh` está diseñado para tolerar exactamente eso: si `fetch-secrets.sh` falla y
el `.env` del servidor todavía conserva todas las credenciales obligatorias, continúa con esas
(comentario de `load.sh`: "si el almacén no responde, la decisión se toma mirando el `.env`, no con
un interruptor que alguien deba acordarse de cambiar").

La consecuencia, verificada en producción: como la ruta de AWS nunca respondió, el servidor real
lleva las más de treinta credenciales de `secret-keys.txt` y `secret-keys-db.txt`
(`DOVECOT_MASTER_PASS`, `MAIL_ENCRYPTION_KEY`, `POSTGRES_PASSWORD`, etc.) **siempre** en el `.env`
de la aplicación. Y como `docker-compose.yml` da a cada servicio ese `.env` entero por `env_file:`
—el `environment:` de cada bloque solo controla qué interpola Compose, `env_file:` vuelca el
fichero completo sin mirar esa lista—, el reparto de mínimo privilegio de `docs/adr/0007` (cada
contenedor recibe solo los secretos de su fila en `reparto.tsv`) no protegía nada en este servidor:
`verify-scope.sh contenedores` daba "recibe de más" en los 22 servicios.

## Decisión

### 1. El backend deja de ser un servicio administrado: gpg simétrico, ya auditado en este repositorio

`ops/security/secrets/store.sh` reemplaza las llamadas a `aws secretsmanager` por un fichero
cifrado (`store.json.gpg`, un JSON plano con todas las claves de `secret-keys.txt` y
`secret-keys-db.txt`) descifrado con gpg simétrico: mismo algoritmo, mismos parámetros y mismo
patrón de paso de la frase que `ops/backup/destino-externo.sh` (`_cf_gpg`/`cf_cifrar`): AES-256,
`--s2k-digest-algo SHA512 --s2k-count 65011712`, la frase por `--passphrase-fd 0` (nunca un
argumento visible en `ps`, nunca el `.env`). Ese patrón ya está auditado, en producción desde los
respaldos, y `gnupg` ya lo instala `ops/server-template/bootstrap.sh`: no se añade ninguna
dependencia nueva al servidor.

La frase vive en `SECRETS_STORE_PASSPHRASE_FILE` (por defecto
`/opt/core-force-mail/secrets/passphrase`), 0600 del usuario que despliega, fuera del árbol que
`git archive`/rsync copian al servidor (`FICHEROS_SERVIDOR` de `scripts/deploy-ecr.sh`) y fuera de
`.env`. `store.json.gpg` vive junto a los scripts (`ops/security/secrets/`, en el servidor, fuera
de git) precisamente porque `rsync -a` (sin `--delete`) que usa `deploy-ecr.sh` nunca borra un
fichero que no está en el árbol sincronizado: un fichero ahí, no versionado, sobrevive a todos los
despliegues futuros.

### 2. Contrato de `fetch-secrets.sh`, `load.sh` y `with-secrets.sh`: sin cambios

`fetch-secrets.sh` sigue escribiendo los mismos dos ficheros
(`SECRETS_ENV_FILE`/`SECRETS_DB_ENV_FILE`, en `/dev/shm`, 0600, todo o nada) con el mismo formato;
solo cambia de dónde saca el JSON antes de repartirlo. `load.sh` y `with-secrets.sh` no se tocan:
su contrato con `fetch-secrets.sh` (mismas salidas, mismo comportamiento ante fallo, mismo respaldo
al `.env` si el almacén no responde) sigue siendo el mismo, así que ningún camino de despliegue que
los invoque (`scripts/deploy-ecr.sh`, `scripts/deploy-mail.sh`, `release.yml`) necesita cambiar.

### 3. `push-secrets.sh` migra las DOS listas, no solo `secret-keys.txt`

La versión con AWS solo subía `secret-keys.txt`; las credenciales de base
(`secret-keys-db.txt`: `POSTGRES_PASSWORD`, `CELL_DB_PASSWORD`, `MAIL_DB_PASSWORD`, etc.) se
quedaban sin camino de migración masiva. Eso no importaba mientras ninguna migración se hiciera de
verdad, pero es exactamente el vector de esta ADR: si `push-secrets.sh` no las saca del `.env`,
`env_file:` las sigue repartiendo a todos los contenedores aunque el resto de secretos ya estén en
el almacén. `push-secrets.sh` ahora lee las dos listas y dejar el `.env` limpio de ambas es
condición para que el aislamiento de `docs/adr/0007` sea real.

También cambia de reemplazar el secreto entero (como hacía `put-secret-value` con
`secret-string`) a **fusionar**: lee el almacén actual (o `{}` si no existe todavía), aplica los
valores del `.env` y vuelve a cifrar. Así una migración repetida, o una corrida después de que
`add-secret.sh` ya hubiera introducido una clave nueva, no la pierde.

### 4. `init-store.sh`: alta de un almacén nuevo

No existía equivalente con AWS (crear el secreto era un efecto lateral de la primera escritura).
Con un fichero local hace falta un paso explícito que además decide la frase: la reutiliza si
`SECRETS_STORE_PASSPHRASE_FILE` ya existe (0600, del usuario), o la genera con
`openssl rand -base64 48` —el mismo criterio que ya usa `BACKUP_ENCRYPTION_PASSPHRASE`— y avisa,
en la misma corrida, que es el único acceso a los secretos y que debe copiarse **fuera** del
servidor y **fuera** de cualquier respaldo cifrado con esa misma frase (si el servidor y ese
respaldo se pierden juntos, la copia de la frase dentro del respaldo tampoco se puede leer: hace
falta un canal aparte, sin dependencia circular).

### 5. `ops/aws/setup-iam.sh`: se retiran solo las políticas de Secrets Manager

`setup-iam.sh` no es un script dedicado a secretos: reconcilia también ECR, S3 de respaldos y
medios, el terminal por SSM y el usuario de despliegue local. Se retiran únicamente las políticas
`secretos` y `secretos-escritura` (y la opción `--escritura-secretos`) que concedían
`secretsmanager:*`; el resto del script, y `ops/aws/setup-auditoria.sh` (CloudTrail y GuardDuty de
la cuenta, sin relación con Secrets Manager), no cambian. `ops/aws/setup-ses.sh`,
`setup-buckets.sh`, `setup-ssm-local.sh`, `ssm-exec.sh`, `setup-github-deploy.sh` y
`setup-github-oidc.sh` tampoco: son usos de AWS que CLAUDE.md sí permite (SES para el envío
transaccional y de marketing, S3 para respaldos y medios, SSM y OIDC para el transporte del
despliegue), ninguno gestiona secretos de la plataforma.

## Alternativas consideradas

* **age/sops.** Cifrado moderno, con soporte nativo de "secrets ya en YAML" en el caso de sops.
  Se descarta porque añade una dependencia nueva al servidor (ninguna de las dos está instalada
  hoy) sin necesidad: gpg simétrico ya cubre el mismo caso de uso (un fichero cifrado con una
  frase, descifrado por un proceso de despliegue) y ya está auditado y en producción en
  `ops/backup`. CLAUDE.md pide evitar dependencias nuevas cuando una ya resuelve el problema.
* **HashiCorp Vault (u otro gestor de secretos autoalojado).** Es infraestructura nueva
  (un servicio más que operar, respaldar y asegurar) para un problema que un fichero cifrado
  resuelve con una superficie muchísimo menor. CLAUDE.md exige no añadir infraestructura nueva
  "sin métricas que lo exijan y sin ADR": aquí no hay métrica que lo pida (un solo servidor, un
  solo consumidor del almacén por despliegue) y el ADR que sí evalúa la alternativa concluye que
  no hace falta.
* **`systemd-creds`** (mencionado como pendiente en `docs/Operacion_Despliegue.md`, "Riesgos y
  pendientes", antes de esta ADR). Ata el descifrado a la TPM o a una clave del sistema de ESE
  host: rotar la frase, mover el almacén a otro servidor o restaurar en uno nuevo tras un
  desastre es más difícil que con una frase portable. Se prefiere gpg con una frase que el
  operador controla y puede copiar a un gestor de contraseñas del equipo.
* **Mantener AWS Secrets Manager y simplemente instalar el CLI de AWS en el servidor.** Es lo que
  la infraestructura ya intentaba hacer y CLAUDE.md lo prohíbe explícitamente para este proyecto:
  el servidor es autoalojado a propósito, y un secreto de la plataforma dependiendo de la
  disponibilidad y las credenciales de un servicio administrado de un tercero contradice esa
  decisión. AWS se conserva solo donde CLAUDE.md ya lo permite (SES, ECR, S3, SSM/OIDC de
  despliegue), nunca para gestionar secretos.

## Procedimiento de rotación de la frase

La frase de descifrado (`SECRETS_STORE_PASSPHRASE_FILE`) es distinta de las llaves de cifrado que
vive dentro del almacén (`MAIL_ENCRYPTION_KEY`, `AUDIT_HASH_KEY`, que se rotan con
`rotate-key.sh` y tienen su propio procedimiento de re-cifrado en el servicio). Rotar la FRASE no
re-cifra ningún dato de negocio: solo cambia cómo se abre el almacén.

1. Generar la frase nueva, fuera del árbol de la aplicación:
   `install -m 0600 /dev/null /opt/core-force-mail/secrets/passphrase.nueva &&
   openssl rand -base64 48 > /opt/core-force-mail/secrets/passphrase.nueva`.
2. Descifrar el almacén con la frase vieja y volver a cifrarlo con la nueva, en un solo paso
   (`store.sh` se sourcea; no se ejecuta como script):
   ```bash
   cd /opt/core-force-mail/app && . ops/security/secrets/store.sh
   json="$(store_leer_json)" || exit 1                       # descifra con la frase de siempre
   SECRETS_STORE_PASSPHRASE_FILE=/opt/core-force-mail/secrets/passphrase.nueva \
     bash -c '. ops/security/secrets/store.sh; store_escribir_json' <<<"$json"
   unset json
   ```
   No hace falta recrear ningún contenedor: el fichero cifrado no cambia de contenido, solo de
   frase, y `fetch-secrets.sh` sigue devolviendo lo mismo.
3. Reemplazar `/opt/core-force-mail/secrets/passphrase` por la frase nueva (0600) y borrar de forma
   segura la vieja (`shred -u`) de cualquier sitio donde haya quedado, incluida la copia externa
   que se hizo al crearla.
4. Verificar: `ops/security/secrets/with-secrets.sh ops/security/secrets/verify-scope.sh entorno`
   debe seguir dando OK. Si falla, la frase vieja seguía en algún sitio (por ejemplo, una variable
   de entorno exportada en una sesión de shell abierta): no se ha perdido nada, el fichero cifrado
   con la frase vieja no se toca hasta que el paso 2 confirma que la nueva descifra igual.
5. Copiar la frase nueva al mismo sitio externo (gestor de contraseñas del equipo) donde vivía la
   anterior, y confirmar que esa copia se actualizó antes de dar la rotación por terminada.

## Lo que esto NO resuelve (límites)

* **Sigue habiendo un secreto en disco: la frase.** gpg simétrico no elimina la necesidad de
  guardar algo que abre todo lo demás; solo la reduce a UNA credencial en vez de treinta, la saca
  del `.env` y la protege con permisos de fichero. Quien tenga esa frase Y acceso de lectura al
  fichero cifrado tiene todos los secretos; es el mismo límite que ya describe
  `docs/adr/0007-minimo-privilegio-en-secretos.md` para `docker inspect`/`/proc` con root en el
  host.
* **Un servidor nuevo nace sin almacén.** `init-store.sh` hay que correrlo una vez por servidor
  (o restaurar la frase y el fichero cifrado desde donde se respaldaron). Hasta entonces,
  `fetch-secrets.sh` falla y `load.sh` cae al `.env`: el mismo comportamiento de siempre, ahora
  documentado como el estado esperado de un servidor recién provisionado, no como un fallo
  silencioso de una integración con AWS que nunca se completó.
* **Perder la frase sin respaldo es perder los secretos**, no solo el acceso: no hay forma de
  recuperar un fichero cifrado con gpg simétrico sin su frase. De ahí que `init-store.sh` no deje
  continuar sin avisar explícitamente de copiarla fuera del servidor.
* **No cambia el límite ya conocido de `docs/adr/0007`**: un contenedor sigue viendo sus secretos
  en texto plano en su propio entorno (`docker inspect`, `/proc/<pid>/environ`); esta ADR corrige
  que esa exposición estuviera, en la práctica, abierta a TODOS los contenedores por el `.env` sin
  migrar, no la reduce por debajo de lo que ya diseñaba el reparto de `reparto.tsv`.
