# Plantilla oficial de servidores - Core Force

Procedimiento repetible para convertir una EC2 Ubuntu recien creada en un host de
produccion estandar. Todos los servidores (prod, dev, ia, reportes) quedan
**identicos** porque se aprovisionan con el mismo `bootstrap.sh` versionado en git.

`bootstrap.sh` es **idempotente**: puede ejecutarse varias veces sin efectos
secundarios.

## Que configura

| Area | Detalle |
| --- | --- |
| Docker | Engine + Compose V2 desde el repo oficial de Docker |
| daemon.json | Rotacion de logs (20m x5, comprimida), `live-restore`, ulimits, pools de red |
| Usuario deploy | Usuario dedicado en grupo `docker`, con llave SSH autorizada |
| SSH | Solo llaves, sin root, `MaxAuthTries`, timeouts (salvaguarda anti-bloqueo) |
| Firewall host | UFW: deny incoming + allow SSH (los puertos web los gobierna el SG/Cloudflare) |
| fail2ban | Proteccion de fuerza bruta SSH (journald) |
| Updates | Parches de seguridad automaticos, sin reinicio automatico |
| Kernel | sysctl: swappiness, inotify, backlog de red, file limits |
| journald | tope de 300 MB (`journald.conf.d/99-core-force-mail.conf`); los logs de servicios van por Docker |
| Swap | Swapfile de seguridad si el host no tiene swap |
| Sistema | Zona horaria + hostname |

## Modelo de seguridad de red (importante)

Esta plantilla **no** abre ni filtra 80/443. Esa es responsabilidad de capas
externas, por diseno:

1. **Security Group de AWS (lock primario):** 22 solo desde tu IP de admin,
   80/443 solo desde los rangos de Cloudflare (prefix list). Ver
   [../security/AWS-DEPLOYMENT.md](../security/AWS-DEPLOYMENT.md).
2. **`cloudflare-firewall.sh` (defensa en profundidad):** restringe los puertos
   publicados por Docker a Cloudflare via `DOCKER-USER`. Ver
   [../security/ORIGIN-LOCKDOWN.md](../security/ORIGIN-LOCKDOWN.md).
3. **UFW (host):** solo gobierna puertos del host (SSH). No interfiere con los
   puertos Docker; el compose ya bindea los servicios internos a `127.0.0.1`.

## Alta de un servidor nuevo (paso a paso)

### 1. Crear la EC2

- AMI: **Ubuntu Server 24.04 LTS**, arquitectura **x86_64** (la imagen de Postfix
  `lycet` es amd64-only).
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
nano server.env            # ajusta SERVER_ROLE, hostname, DEPLOY_USER, etc.
sudo ./bootstrap.sh
```

Si no pones `DEPLOY_PUBKEY`, el bootstrap reutiliza la llave del usuario `ubuntu`
para el usuario de despliegue, de modo que nunca te quedas sin acceso.

### 3. Configurar el Security Group

Aplica las reglas de [../security/AWS-DEPLOYMENT.md](../security/AWS-DEPLOYMENT.md)
(prefix list de Cloudflare para 80/443, tu IP para 22).

### 4. (Opcional) Lock de Cloudflare a nivel host

```bash
# en el repo desplegado, tras el primer deploy:
sudo ops/security/systemd/install.sh
```

### 5. Desplegar la plataforma

Desde tu maquina:

```bash
DEPLOY_HOST=<ip> \
DEPLOY_USER=deploy \
DEPLOY_PATH=/opt/core-force-mail/app \
DEPLOY_SSH_KEY=~/.ssh/<tu-llave>.pem \
  ./scripts/deploy-fast.sh
```

`deploy-fast.sh` soporta `DEPLOY_SSH_KEY` (llave) o `DEPLOY_SSH_PASSWORD`
(password). Con la plantilla, usa siempre llave.

## Acceso por tunel SSH

El servidor no expone paneles ni base de datos: solo SSH a nivel host y 80/443
(restringidos a Cloudflare). Para administrar o probar la plataforma **antes** de tener
DNS/Cloudflare, se usa un tunel SSH con reenvio de puertos local.

Entrada en `~/.ssh/config` (en tu maquina):

```
Host core-force-prod
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
ssh core-force-prod                 # abre tunel + sesion
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
```

## Notas

- **Secretos**: el `.env` de la plataforma no se versiona ni lo crea esta plantilla. En AWS,
  inyecta los secretos desde Secrets Manager / SSM (ver AWS-DEPLOYMENT.md).
- **Base de datos**: si usas RDS, las migraciones se aplican al endpoint, no por
  `docker exec` (ver AWS-DEPLOYMENT.md, seccion RDS).
- **Reinicio por updates**: deshabilitado a proposito. Programa ventanas de
  mantenimiento para aplicar reinicios de kernel manualmente.
