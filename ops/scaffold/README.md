# ops/scaffold

Generador de microservicios y guardarraíles que corren en CI. Cada script explica en su
cabecera el fallo real que evita; aquí solo el mapa.

## Agregar un microservicio

```
make new-service name=domain-service port=8040 module=domains
```

Genera `services/<name>/` con el molde hexagonal que compila (`main.go`,
`internal/{domain,ports,app,adapters/{http,postgres,nats}}`), `Dockerfile` (`FROM scratch`
con `HEALTHCHECK` por el propio binario), la migración canónica de empresa
`migrations/tenant/canonical/<name>/01_<name>.sql` y el puerto en `.env.example`. Imprime,
sin editar ficheros frágiles, el bloque de `docker-compose.yml`, la entrada de
`services/gateway/routes.json` (el gateway lee la tabla al arrancar y no se recompila) y
la migración de permisos del módulo. `module=--ungated` para un servicio que no se gatea.

Si la tabla del servicio vive en la celda (la leen los motores de correo), la migración va
en `migrations/cell/canonical/<name>/` en vez de `tenant/`.

## Guardarraíles

| Script | Regla |
|---|---|
| `validate.sh` | Puertos sin colisión; todo módulo gateado en `routes.json` tiene permisos sembrados; delega en coupling, streams, base-images y sql-arity |
| `check-migrations.sh` | Migraciones idempotentes (por sentencia, siembras con `ON CONFLICT` o `NOT EXISTS`), sin depender de esquemas creados después, sin redefinir vistas que otra amplía. Revisa empresa, celda y registro; `CANON_DIR` acota a uno |
| `test-integration.sh` | `make test-integration` y el job `integration` de CI: pruebas `//go:build integration` contra Postgres y Redis desechables (y un Redis con `tls-port` que crea la prueba de `pkg/config` con su CA generada al vuelo), una base por variable `*_TEST_DSN`, paquetes en serie, y falla si una prueba se salta o lee una variable que el script no define |
| `check-migration-drops.sh` | Un `DROP CONSTRAINT IF EXISTS` con nombre mal escrito no falla: se detecta |
| `check-coupling.sh` | Un servicio no lee tablas de otro esquema; solo vistas `v_*`. Escrituras ajenas se vigilan aparte |
| `check-streams.sh` | Dos streams de JetStream no se solapan en subjects |
| `check-base-images.sh` | Imágenes fijadas por versión o digest |
| `check-sql-arity.sh` | `INSERT` con columnas y valores descuadrados |
| `check-dkim-grace.sh` | La gracia de una rotacion DKIM (minimo y defecto de domain-service, valor de `.env.example`) cubre `maximal_queue_lifetime` de Postfix mas un dia de TTL; lo corre `validate.sh` |
| `check-mail-size-limits.sh` | `max_message` de Rspamd, el `max_size` del antivirus, `StreamMaxLength` y `MaxFileSize` de clamd y el techo del cuerpo de `/pipe` de mail-security cubren `message_size_limit` de Postfix mas 1 MiB; `MaxScanSize` no queda por debajo de `MaxFileSize`, `AlertExceedsMax` es `yes` y `force_actions.conf` convierte `CLAM_VIRUS_FAIL` en `soft reject` (nada se entrega sin veredicto de ClamAV); el techo de `/pipe` no pasa de `max_message` mas 1 MiB y coincide con `MaxQuarantineMaxSizeBytes` y con el CHECK de la migracion 08; el `postfixMessageSizeLimit` del webmail es el de Postfix, y el defecto y `.env.example` de `MAIL_QUARANTINE_MAX_BODY_MB` caben en el techo; lo corre `validate.sh` |
| `check-silent-errors.sh` | Ningún 500 sin motivo registrado (`response.Unexpected`) |
| `check-clean-copy.sh` | Sin restos de las bases de referencia (ERP de origen y capa PHP/MySQL de mailcow) ni cuentas de AWS concretas en ARNs o registros de ECR |
| `eventcontracts/` | Campos del payload por subject; un consumidor no lee lo que su emisor no publica |
| `gen-events.sh` | Regenera `docs/arquitectura/EVENTS.md`; CI falla si queda atrás |
| `service-paths.sh` | Mapa ruta -> servicio derivado de `docker-compose.yml`, base de la detección de cambios en el despliegue |
| `check-pgbouncer-entrypoint.sh` | PgBouncer solo relaja TLS y credenciales en development y test; fuera exige `verify-full`, una CA legible (por defecto `pgbouncer/rds-global-bundle.pem`, que tiene que viajar en git; `DB_UPSTREAM_CA_FILE` la sustituye) y `userlist.txt`; lo corre `validate.sh` |
| `check-selfhosted-profile.sh` | Perfil autoalojado sin docker: `docker-compose.selfhosted.yml` conserva los parámetros de Postgres y Redis del compose base, Redis sin puerto en claro ni contraseña en la línea de órdenes, `pg_hba.conf` sin TCP en claro, todo servicio Go con `REDIS_TLS` y la CA, montajes de `internal-tls.sh`, mail-auth con certificado de la CA interna (directorio montado, `MAIL_AUTH_TLS_*`, `user:` sin root, SAN = host de `MAIL_AUTH_URL`) y el webmail verificándolo, cuerpo máximo del borde frente al webmail y la importación de contactos, rangos de Cloudflare, imagen del borde por digest, `perfil-despliegue.sh` ejecutado y los dos despliegues usándolo; lo corre `validate.sh` |
| `check-deploy-mail.sh` | Despliegue de los motores sin docker: todo motor con build tiene imagen `core-force-mail/<motor>:${MAIL_DEPLOY_TAG}` con `pull_policy: never` en `docker-compose.mail.images.yml`; todo motor privilegiado, en la red del host o con el socket de docker lleva `core-force-mail.solo-servidor`; `scripts/lib/despliegue.sh` ejecutado (guardia de retroceso en un repositorio de prueba, candado libre, ocupado, abandonado y liberado); `recursos-externos.sh` con un docker falso; `scripts/deploy-mail.sh` de punta a punta con ssh, docker y aws falsos (motor desconocido, solo-servidor contra el docker local, commit desconocido, candado ajeno, orden de dependencias, `--no-build` por `with-secrets.sh`, espera de arranque, `.deployed-tags`, nada que desplegar); `deploy-ecr.sh` usa las piezas comunes y exige los recursos externos; lo corre `validate.sh` |
| `test-selfhosted-profile.sh` | Con docker: levanta el perfil en `production` con certificados de `internal-tls.sh` y prueba con clientes reales `verify-full`, el rechazo de Postgres sin TLS y de Redis en claro, los servicios Go por TLS, mail-auth verificable como `mail-auth` con la CA interna y su clave no legible por otro uid, el borde (HTTPS, SNI, 80 a 443, HSTS, 413, solo Cloudflare e IP real) y la renovación sin reinicio de Postgres, Redis y mail-auth (`docs/Operacion_Despliegue.md` 11) |

Las listas de excepciones (`*-allowlist.txt`) nacen vacías: lo que hoy pasa es la línea
base, y nada nuevo entra sin justificarse en la revisión.

## Puertos

Servicios Go: 8001-8099 (plano de control 80xx, correo 804x, marketing 805x). Motores de
correo: los estándar (25, 465, 587, 143, 993, 110, 995, 4190) por celda.
