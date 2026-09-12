# Operación y despliegue

Reglas de operación heredadas del ERP de referencia, ya como documento propio. Las razones
largas de cada guardarraíl están en `ops/scaffold/README.md`, `ops/security/secrets/README.md`,
`ops/backup/README.md` y `docs/arquitectura/OBSERVABILIDAD.md`.

## 1. Entornos

* Desarrollo: `make dev` levanta Postgres (perfil `embedded-db`), PgBouncer, Redis, NATS y
  el plano de control con `docker-compose.yml`. Los motores de correo se levantan aparte
  con `deploy/mail/docker-compose.mail.yml` cuando se trabaje en la fase 2.
* Producción (AWS): una cuenta por ambiente (dev, staging, prod). RDS PostgreSQL Multi-AZ
  detrás de PgBouncer, ElastiCache, SES, S3, Secrets Manager. Servidor de aplicación
  endurecido con `ops/server-template/bootstrap.sh`.

## 2. Secretos

Ninguna credencial vive en el repositorio ni en el `.env` del servidor: la fuente es AWS
Secrets Manager (`core-force-mail/prod`). `ops/security/secrets/fetch-secrets.sh` los
materializa en `/dev/shm/core-force-mail/secrets.env` (tmpfs, 0600, todo o nada) y
`with-secrets.sh` envuelve cualquier `docker compose` que cree contenedores. La lista
canónica es `ops/security/secrets/secret-keys.txt`; añadir una variable ahí es parte de
introducir el secreto. CI: `make check-secrets` y `make check-secret-sources`.

## 3. Migraciones

* Registro: las aplica `organization` al arrancar (`RegistryMigrator`, idempotente).
* Empresa: se aplican al crear la empresa y en un barrido de fondo (`RUN_TENANT_MIGRATIONS`)
  por conexión directa con advisory lock; `POST /api/v1/organizations/migrate` para forzar.
* Celda: `ops/db/apply-migration.sh` contra `mail_cell_<code>` (P: runner propio en
  `mail-directory`).
* Reglas: idempotentes y aditivas (`make check-migrations` cubre empresa y celda), cabecera
  `-- Schema | Service`, nunca cambiar el tipo de una columna sin
  `ops/maintenance/pgbouncer-reconnect.sh` después (los planes preparados viven en el pooler).

## 4. Despliegue

* `scripts/deploy-ecr.sh` desde el PC: compila en local, publica a ECR etiquetando por
  commit y el servidor solo hace `pull`. **Nunca `docker compose build` en el servidor** ni
  recrear un contenedor a mano: usaría una imagen local y el servicio correría otro código
  (`scripts/check-image-drift.sh` lo delata).
* GitHub Actions (`release.yml`): construye en paralelo las imágenes de los servicios
  afectados (detectados con `ops/scaffold/service-paths.sh`), con OIDC hacia AWS y sin
  claves estáticas; el despliegue es un `workflow_dispatch` con `deploy=true` que entra por
  SSM, nunca por SSH.
* Todo lo que el servidor ejecuta o monta viaja en el rsync de `stage_head_files` desde
  `git archive` de HEAD: compose, `migrations/`, `ops/security`, `ops/ecr`,
  `ops/observability`, `ops/maintenance`, `ops/backup`, `pgbouncer`.
* Commits siempre con pathspec (`git commit -- <rutas>`): el índice puede estar compartido
  con otra sesión.
* `docker-compose.images.yml` es generado (`make gen-compose-images`); CI falla si queda
  atrás.

## 5. Respaldos

`ops/backup/backup-tenants.sh` vuelca cada base (`mail_%`) por separado con `pg_dump -Fc`,
verifica que `pg_restore --list` lo lee, sube a `BACKUP_S3_BUCKET` (bucket distinto al de
medios, con Object Lock) y conserva 3 días en local. `verify-restore.sh` restaura el último
volcado en una base desechable cada semana. Las alertas vigilan la antigüedad del último
éxito y la ausencia de la serie. Programación: `ops/backup/systemd/`.

## 6. Observabilidad

`docker-compose.observability.yml` (Prometheus, Grafana, Loki, Promtail, node-exporter,
docker-socket-proxy) se une a la red `mail_mail-internal` como externa. Los objetivos se
generan desde el compose (`make gen-observability-targets`); las reglas de alerta tienen
pruebas de `promtool` (`make check-alertas`). Cada servicio expone `/healthz` y `/metrics`
fuera de su cadena de middlewares; las imágenes son `FROM scratch` y el `HEALTHCHECK` usa
el propio binario con `--healthcheck <puerto>`.

## 7. Alta de un servicio

1. `make new-service name=<svc> port=<puerto> module=<modulo>`: molde hexagonal que compila,
   Dockerfile, migración canónica de empresa, puerto en `.env.example`.
2. Ruta en `services/gateway/routes.json` (`prefix`, `service`, `module`).
3. Permisos del módulo en `migrations/registry/NNN_<svc>_permissions.sql` y, si es de
   correo, su módulo de catálogo en `organization.module_catalog.permission_modules`.
4. Bloque en `docker-compose.yml` (el generador imprime el fragmento).
5. `make validate-scaffold` comprueba puertos, permisos, catálogo, acoplamiento, streams e
   imágenes. `make gen-events` y `make gen-observability-targets` regeneran lo derivado.

## 8. Checks antes de dar por terminada una tarea

`make checks` (build, vet, migraciones, acoplamiento, errores mudos, aridad SQL, streams,
contratos de eventos, secretos, scaffold) y `make clean-copy`. Con docker:
`make test` ya corre con `-race`.
