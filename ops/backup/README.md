# Respaldo y recuperación por empresa

El diseño es **una base de datos por empresa**. El respaldo respeta eso: un archivo por
base, no una foto del servidor entero. Es la diferencia entre poder devolver a UNA empresa
al estado de ayer y tener que devolver a todas.

RDS ya hace snapshots del servidor, y sirven para el desastre completo. No sirven para
"a esta empresa le borraron el kardex el martes": restaurar ese snapshot devolvería al
martes también a las demás empresas.

## Los tres scripts

| Script | Qué hace |
|---|---|
| `backup-tenants.sh` | Vuelca cada base a `-Fc`, comprueba que el archivo se puede leer y lo sube a S3 |
| `restore-tenant.sh` | Restaura UNA base. Por defecto a una base nueva; sobrescribir exige `--force` y confirmación escrita |
| `verify-restore.sh` | Restaura el último respaldo en una base desechable, comprueba que trae filas de negocio y la borra |

`verify-restore.sh` es el que importa. Un respaldo que nadie restauró nunca es una
suposición, no una garantía; esto la convierte en un hecho comprobado cada semana.

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

ops/backup/verify-restore.sh                      # la base con el respaldo más grande
ops/backup/verify-restore.sh mail_tenant_demo
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

En producción es `cf-backups-188149024609`, creado para esto y configurado con:

- acceso público bloqueado en las cuatro dimensiones;
- cifrado en reposo por defecto (AES256 con clave de bucket);
- versionado, para que sobrescribir o borrar no destruya el histórico;
- ciclo de vida: a `STANDARD_IA` a los 30 días, expiración a los 180, versiones no
  vigentes a los 30, y limpieza de subidas multiparte incompletas a los 7.

El acceso sale del rol de instancia `core-force-mail-ec2-role`; no hay credenciales en disco.

## Programación

```bash
sudo cp ops/backup/systemd/*.service ops/backup/systemd/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now core-force-backup.timer core-force-backup-verify.timer
systemctl list-timers 'core-force-*'
```

Respaldo diario (03:15 hora de Lima) y prueba de restauración semanal (domingo 04:30).
Ambos con prioridad baja de CPU y disco para no competir con la operación.

**En el servidor actual están en el crontab del usuario `deploy`, no en systemd**: instalar
unidades exige root y el usuario de despliegue no lo tiene (`sudo` pide contrasena).

Cuidado con la zona horaria al comparar los dos: el `OnCalendar` de las unidades esta en
**UTC** y el crontab corre en la zona del servidor, que es **America/Lima**. Durante un
tiempo el crontab decia `15 8` -las 08:15 de Lima, con las tiendas abriendo- mientras la
unidad y esta pagina describian las 03:15. Hoy los dos hacen lo mismo: respaldo a las 03:15
y prueba de restauracion los domingos a las 04:30, hora de Lima.

Quien tenga root debería mover esto a systemd —queda mejor observado— y al hacerlo tiene
que **vaciar el crontab**, o el respaldo correría dos veces:

```bash
crontab -l   # ver lo que hay hoy
```

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
temporizador desactivado, servidor reinstalado, crontab vaciado por error. Un servidor sin
respaldo se ve exactamente igual que uno con respaldo, hasta el dia que hace falta.

Por eso hay dos alertas por trabajo: la de antiguedad no puede dispararse si la serie
desaparece, porque entonces no hay nada que comparar.

## Qué revisar cuando falle

Los scripts salen con código distinto de cero ante cualquier problema. Con el crontab
actual eso queda en `/opt/core-force-mail/logs/backup.log` y `backup-verify.log`; con systemd,
en `systemctl status core-force-backup.service`. Los fallos que importan:

- **`volcado ilegible o vacío`**: el archivo existe pero `pg_restore` no lo entiende. No
  es un respaldo; hay que repetirlo y averiguar por qué.
- **`quedó respaldado en disco pero no subió a S3`**: sobrevive a un borrado accidental,
  no a la pérdida del servidor.
- **`la base restaurada no tiene filas`**: el respaldo trae el esquema pero no los datos.
  Es el fallo más peligroso porque un `ls` del directorio se ve perfectamente normal.
