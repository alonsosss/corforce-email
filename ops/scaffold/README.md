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
| `test-integration.sh` | `make test-integration` y el job `integration` de CI: pruebas `//go:build integration` contra Postgres y Redis desechables, una base por variable `*_TEST_DSN`, paquetes en serie, y falla si una prueba se salta o lee una variable que el script no define |
| `check-migration-drops.sh` | Un `DROP CONSTRAINT IF EXISTS` con nombre mal escrito no falla: se detecta |
| `check-coupling.sh` | Un servicio no lee tablas de otro esquema; solo vistas `v_*`. Escrituras ajenas se vigilan aparte |
| `check-streams.sh` | Dos streams de JetStream no se solapan en subjects |
| `check-base-images.sh` | Imágenes fijadas por versión o digest |
| `check-sql-arity.sh` | `INSERT` con columnas y valores descuadrados |
| `check-silent-errors.sh` | Ningún 500 sin motivo registrado (`response.Unexpected`) |
| `check-clean-copy.sh` | Sin restos de las bases de referencia (ERP de origen y capa PHP/MySQL de mailcow) |
| `eventcontracts/` | Campos del payload por subject; un consumidor no lee lo que su emisor no publica |
| `gen-events.sh` | Regenera `docs/arquitectura/EVENTS.md`; CI falla si queda atrás |
| `service-paths.sh` | Mapa ruta -> servicio derivado de `docker-compose.yml`, base de la detección de cambios en el despliegue |

Las listas de excepciones (`*-allowlist.txt`) nacen vacías: lo que hoy pasa es la línea
base, y nada nuevo entra sin justificarse en la revisión.

## Puertos

Servicios Go: 8001-8099 (plano de control 80xx, correo 804x, marketing 805x). Motores de
correo: los estándar (25, 465, 587, 143, 993, 110, 995, 4190) por celda.
