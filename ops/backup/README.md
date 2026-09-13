# Respaldo y recuperación por empresa

El diseño es **una base de datos por empresa**. El respaldo respeta eso: un archivo por
base, no una foto del servidor entero. Es la diferencia entre poder devolver a UNA empresa
al estado de ayer y tener que devolver a todas.

RDS ya hace snapshots del servidor, y sirven para el desastre completo. No sirven para
"a esta empresa le borraron una lista de contactos el martes": restaurar ese snapshot
devolvería al martes también a las demás empresas.

## Los tres scripts

| Script | Qué hace |
|---|---|
| `backup-tenants.sh` | Vuelca cada base a `-Fc`, comprueba que el archivo se puede leer y lo sube a S3 |
| `restore-tenant.sh` | Restaura UNA base. Por defecto a una base nueva; sobrescribir exige `--force` y confirmación escrita |
| `verify-restore.sh` | Restaura el último respaldo en una base desechable, comprueba que trae todo lo que el volcado lista y que es una base de la plataforma, y la borra |

`verify-restore.sh` es el que importa. Un respaldo que nadie restauró nunca es una
suposición, no una garantía; esto la convierte en un hecho comprobado cada semana.

Qué comprueba, sin suponer filas de negocio (una empresa recién dada de alta no tiene
ninguna y su respaldo vale lo mismo):

- que cada esquema y cada tabla del índice del volcado (`pg_restore --list`) existe en la
  base restaurada: `restore-tenant.sh` muestra los errores de `pg_restore`, pero no se
  detiene en ellos;
- `platform.event_outbox`, que existe en las tres clases de base;
- empresa y registro: el historial `public.schema_migrations` con filas (son datos, no
  esquema) y el esquema que declara la cabecera `-- Schema:` de cada migración registrada,
  leída de `migrations/` del árbol desplegado; en el registro, además, al menos un usuario;
- celda: sus migraciones se aplican a mano y no dejan historial, así que se exigen los
  esquemas que declaran todas las migraciones de celda del árbol desplegado. Una migración
  de celda con un esquema nuevo, desplegada y aún sin aplicar, hace fallar la verificación
  de esa celda hasta que se aplica.

## Uso

```bash
cd /opt/core-force-mail/app

ops/backup/backup-tenants.sh                      # todas las bases
ops/backup/backup-tenants.sh mail_tenant_demo      # una sola

# Recuperar: por defecto NO toca la base viva, crea mail_tenant_demo_restore_<fecha>
ops/backup/restore-tenant.sh mail_tenant_demo
ops/backup/restore-tenant.sh mail_tenant_demo s3://<bucket>/postgres/mail_tenant_demo/<sello>.dump
ops/backup/restore-tenant.sh mail_tenant_demo --into mail_prueba

# Reemplazar la base viva (irreversible; pide escribir el nombre para confirmar)
ops/backup/restore-tenant.sh mail_tenant_demo --force

ops/backup/verify-restore.sh                      # la empresa con el respaldo más grande
ops/backup/verify-restore.sh mail_tenant_demo
ops/backup/verify-restore.sh mail_registry        # también el registro o una celda
```

Casi siempre lo correcto es restaurar a una base nueva y sacar de ahí lo que falta con
SQL. Sobrescribir la viva descarta todo lo que la empresa hizo desde el respaldo.

## Configuración

Las credenciales salen del resolvedor comun `ops/db/pg-credentials.sh`: la **contrasena**
del almacen de secretos (`ops/security/secrets`) y el **host y usuario** del `.env`, que es
configuracion y no secreto. Estos scripts no leen el `.env` para la contrasena: lo hacian, y
el dia que las credenciales pasaron al almacen se quedaron con la variable vacia.

| Variable | Por defecto | Para qué |
|---|---|---|
| `BACKUP_DIR` | `/opt/core-force-mail/backups` | Destino local |
| `BACKUP_S3_BUCKET` | *(sin definir)* | Copia externa. **Sin esto solo hay copia local, que se pierde con el servidor** |
| `BACKUP_KEEP_DAYS` | `3` | Retención local; el histórico vive en S3 |

`BACKUP_S3_BUCKET` apunta a un bucket **distinto** del de medios (`MINIO_BUCKET`): un
respaldo de base de datos y un archivo que sube un usuario no merecen la misma política de
acceso ni el mismo ciclo de vida.

Por defecto se llama `cf-backups-<id-de-cuenta>`: `ops/aws/setup-iam.sh`,
`ops/aws/setup-buckets.sh` y `ops/aws/setup-auditoria.sh` lo derivan de la cuenta que los
ejecuta y leen el mismo `BACKUP_S3_BUCKET` para cambiarlo. Debe estar configurado con:

- acceso público bloqueado en las cuatro dimensiones;
- cifrado en reposo por defecto (AES256 con clave de bucket);
- versionado, para que sobrescribir o borrar no destruya el histórico;
- ciclo de vida: a `STANDARD_IA` a los 30 días, expiración a los 180, versiones no
  vigentes a los 30, y limpieza de subidas multiparte incompletas a los 7.

El acceso sale del rol de instancia `core-force-mail-ec2-role`; no hay credenciales en disco.

## Programación

`ops/server-template/bootstrap.sh` instala las unidades de `ops/backup/systemd/` y retira
las entradas de cron que hubiera de antes, que correrían el respaldo dos veces. A mano:

```bash
sudo cp ops/backup/systemd/*.service ops/backup/systemd/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now core-force-mail-backup.timer core-force-mail-backup-verify.timer
systemctl list-timers 'core-force-mail-*'
```

Respaldo diario a las 03:15 hora de Lima y prueba de restauración los domingos a las 04:30.
El `OnCalendar` de las unidades está en **UTC** (08:15 y 09:30): al cambiarlo, no leerlo
como hora local. Ambos con prioridad baja de CPU y disco para no competir con el servicio.

## Vigilancia

Cada corrida publica su resultado en `/opt/core-force-mail/metrics/*.prom`, que node-exporter
monta y Prometheus recoge. Cuatro alertas en
`ops/observability/prometheus/rules/plataforma.yml`:

| Alerta | Cuando |
|---|---|
| `RespaldoSinExitoReciente` | mas de 36 h sin un respaldo bueno |
| `RespaldoNoSeRegistra` | el trabajo no publica nada: no llega a ejecutarse |
| `VerificacionDeRespaldoSinExitoReciente` | mas de 10 dias sin comprobar que restaura |
| `VerificacionDeRespaldoNoSeRegistra` | idem, sin serie que vigilar |

Lo que se vigila es la **marca de tiempo del ultimo exito**, no el codigo de salida. El caso
peligroso no es el respaldo que falla -ese deja un error- sino el que dejo de ejecutarse:
temporizador desactivado, servidor reinstalado. Un servidor sin respaldo se ve exactamente
igual que uno con respaldo, hasta el dia que hace falta.

Por eso hay dos alertas por trabajo: la de antiguedad no puede dispararse si la serie
desaparece, porque entonces no hay nada que comparar.

## Qué revisar cuando falle

Los scripts salen con código distinto de cero ante cualquier problema; la salida queda en
el journal (`journalctl -u core-force-mail-backup.service`,
`journalctl -u core-force-mail-backup-verify.service`). Los fallos que importan:

- **`volcado ilegible o vacío`**: el archivo existe pero `pg_restore` no lo entiende. No
  es un respaldo; hay que repetirlo y averiguar por qué.
- **`quedó respaldado en disco pero no subió a S3`**: sobrevive a un borrado accidental,
  no a la pérdida del servidor.
- **`la base restaurada no tiene lo que el volcado trae`**: la restauración terminó pero
  faltan esquemas o tablas del índice. Es el fallo más peligroso porque un `ls` del
  directorio se ve perfectamente normal.
- **`public.schema_migrations está vacía`** o **`faltan esquemas que declaran sus
  migraciones`**: la base restaurada no es la que su historial dice que era.
