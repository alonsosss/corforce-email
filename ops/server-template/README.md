# Plantilla de servidores - Core Force Mail

Procedimiento repetible para convertir una EC2 Ubuntu recien creada en un host de la
plataforma. Los servidores de cada ambiente (una cuenta de AWS por ambiente: dev, staging,
prod) quedan **identicos** porque se aprovisionan con el mismo `bootstrap.sh` versionado en
git.

`bootstrap.sh` es **idempotente**: puede ejecutarse varias veces sin efectos
secundarios.

## Que configura

| Area | Detalle |
| --- | --- |
| Docker | Engine + Compose V2 desde el repo oficial de Docker |
| daemon.json | Rotacion de logs (20m x5, comprimida), `live-restore`, ulimits, pools de red |
| Usuario deploy | Usuario dedicado en grupo `docker`, con llave SSH autorizada |
| SSH | Solo llaves, sin root, `MaxAuthTries`, timeouts (salvaguarda anti-bloqueo) |
| Firewall host | UFW: deny incoming + allow SSH (los puertos publicos los gobierna el Security Group) |
| fail2ban | Proteccion de fuerza bruta SSH (journald) |
| Updates | Parches de seguridad automaticos, sin reinicio automatico |
| Kernel | sysctl: swappiness, inotify, backlog de red, file limits |
| journald | tope de 300 MB (`journald.conf.d/99-core-force-mail.conf`); los logs de servicios van por Docker |
| Swap | Swapfile de seguridad si el host no tiene swap |
| Sistema | Zona horaria + hostname |
| Entorno | `ENVIRONMENT` de `server.env` (`production` o `staging`) en el `.env` de `DEPLOY_PATH` |
| Respaldo | Unidades `core-force-mail-backup*` de `ops/backup/systemd/` |

## Entorno declarado

Los servicios relajan controles solo con `ENVIRONMENT=development` o `test` (Redis en claro,
credencial de plataforma en la celda, clave de firma efimera, servicios sin
`INTERNAL_GATEWAY_TOKEN`, webmail sin TLS verificado o sin ClamAV:
`docs/Operacion_Despliegue.md`, 1), y `.env.example` trae `development` para el entorno
local. Por eso:

- `server.env` declara `ENVIRONMENT=production` o `staging`, tambien en el servidor de la
  cuenta de dev; sin uno de los dos, `bootstrap.sh` no aprovisiona;
- `bootstrap.sh` lo escribe en `$DEPLOY_PATH/.env`, el fichero que Compose pasa a los
  contenedores, y lo repone si alguien lo cambio;
- `ops/security/secrets/with-secrets.sh`, por donde pasa todo `docker compose` del servidor,
  se niega a desplegar si ese `.env` no dice exactamente `ENVIRONMENT=production` o
  `ENVIRONMENT=staging`.

Al completar el `.env` con la configuracion de `.env.example` no copies el fichero encima:
traeria `ENVIRONMENT=development` y el despliegue se detendria con el motivo.

## Modelo de seguridad de red

Esta plantilla **no** abre ni filtra los puertos publicos. Eso lo gobierna el **Security
Group de AWS**: 22 solo desde la IP de administracion y cada puerto publico solo desde su
origen (80/443 desde el proxy de borde de `docs/Arquitectura_Core_Force_Mail.md`, 1). UFW solo gobierna los
puertos del host (SSH) y no interfiere con los de Docker; el compose enlaza los servicios
internos a `127.0.0.1`.

## Alta de un servidor nuevo (paso a paso)

### 1. Crear la EC2

- AMI: **Ubuntu Server 24.04 LTS**, arquitectura **x86_64** (las imagenes se construyen
  para amd64).
- Tipo: `t3.large` (app-only, Postgres en RDS) o `t3.xlarge` si Postgres corre en
  la instancia.
- Disco: gp3, 80-100 GB.
- Key pair: crea/asocia uno; la llave publica entra en el usuario `ubuntu`.

### 2. Copiar la plantilla y ejecutar el bootstrap

Desde tu maquina, copia solo la carpeta de la plantilla (o el repo completo):

```bash
scp -i <tu-llave>.pem -r ops/server-template ubuntu@<ip>:/tmp/
ssh -i <tu-llave>.pem ubuntu@<ip>
```

En el servidor:

```bash
cd /tmp/server-template
cp server.env.example server.env
nano server.env            # ENVIRONMENT (production o staging), SERVER_ROLE, hostname, DEPLOY_USER...
sudo ./bootstrap.sh
```

Si no pones `DEPLOY_PUBKEY`, el bootstrap reutiliza la llave del usuario `ubuntu`
para el usuario de despliegue, de modo que nunca te quedas sin acceso.

### 3. Configurar el Security Group

22 solo desde tu IP de administracion; 80/443 solo desde el proxy de borde.

### 4. Configuracion y secretos

Completa `/opt/core-force-mail/app/.env` con la configuracion no sensible de `.env.example`
(sin tocar `ENVIRONMENT`) y publica los secretos en el almacen:
`ops/security/secrets/README.md`, Puesta en marcha.

### 5. Desplegar la plataforma

Desde tu maquina:

```bash
DEPLOY_HOST=<ip> \
DEPLOY_USER=deploy \
DEPLOY_PATH=/opt/core-force-mail/app \
DEPLOY_SSH_KEY=~/.ssh/<tu-llave>.pem \
  ./scripts/deploy-ecr.sh <servicios>
```

El primer despliegue lleva la lista de servicios: sin `.deployed-tag` en el servidor,
`deploy-ecr.sh` no sabe desde que commit comparar. Despues basta sin argumentos. Solo
admite llave (`DEPLOY_SSH_KEY`); el camino diario es SSM (`ops/aws/setup-ssm-local.sh`), y
el 22 queda como puerta de emergencia.

## Acceso por tunel SSH

El servidor no expone paneles ni base de datos: solo SSH a nivel host y los puertos
publicos que abre el Security Group. Para administrar o probar la plataforma **antes** de
tener DNS y proxy de borde, se usa un tunel SSH con reenvio de puertos local.

Entrada en `~/.ssh/config` (en tu maquina):

```
Host core-force-mail-prod
    HostName <ip-publica-o-elastic-ip>
    User ubuntu
    IdentityFile ~/.ssh/core-force-mail-prod.pem
    IdentitiesOnly yes
    ServerAliveInterval 60
    LocalForward 8443 127.0.0.1:443
    LocalForward 8080 127.0.0.1:80
```

Abrir el tunel y navegar:

```bash
ssh core-force-mail-prod            # abre tunel + sesion
# en otra pestana del navegador: https://localhost:8443
```

Requisitos para que el tunel funcione:
- Llave en `~/.ssh/` con permisos `600`.
- Security Group con **22 (SSH) abierto desde tu IP de administracion**.
- EC2 encendida y con la llave publica en el usuario (`ubuntu` de la AMI, o
  `deploy` tras el bootstrap).

Tras el bootstrap, para el pipeline usa el usuario `deploy`; para tunel/admin
puedes seguir con `ubuntu`.

## Verificacion post-bootstrap

```bash
docker --version && docker compose version
systemctl is-active docker fail2ban
sudo ufw status
sudo fail2ban-client status sshd
swapon --show
sudo sysctl vm.swappiness fs.inotify.max_user_watches
cat /etc/docker/daemon.json
sudo grep -E '^ENVIRONMENT=' /opt/core-force-mail/app/.env   # production o staging
systemctl list-timers 'core-force-mail-*'
```

## Notas

- **Secretos**: el `.env` de la plataforma solo lleva configuracion; las credenciales viven
  en AWS Secrets Manager y se materializan en memoria al desplegar
  (`ops/security/secrets/README.md`).
- **Base de datos**: con RDS, las migraciones de celda y los respaldos se hacen contra el
  endpoint (`ops/db/`, `ops/backup/`), no por `docker exec`.
- **Reinicio por updates**: deshabilitado a proposito. Programa ventanas de
  mantenimiento para aplicar reinicios de kernel manualmente.
