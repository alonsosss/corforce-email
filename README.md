# Core Force Mail

Plataforma multitenant de correo empresarial: correo corporativo (Postfix, Dovecot, Rspamd),
correo transaccional y marketing (Amazon SES), bajo un plano de control propio en Go con
arquitectura hexagonal por microservicio y una aplicación web en React + TypeScript.

## Documentos rectores

Léelos en este orden antes de diseñar o implementar (`CLAUDE.md` los enumera y fija las
decisiones que no se negocian):

1. [Arquitectura](docs/Arquitectura_Core_Force_Mail.md)
2. [Modelo de datos y celdas](docs/Modelo_de_Datos_y_Celdas.md)
3. [Usuarios, roles y acceso](docs/Usuarios_Roles_y_Acceso.md)
4. [Organización del repositorio](docs/Organizacion_Repositorio.md)
5. [Estado de la fase 0](docs/Fase0_Estado.md)
6. [Operación y despliegue](docs/Operacion_Despliegue.md), [sesión y CSP](docs/arquitectura/CSP-Y-SESION.md), [observabilidad](docs/arquitectura/OBSERVABILIDAD.md), [eventos](docs/arquitectura/EVENTS.md)
7. [Motores de correo](deploy/mail/README.md)

El informe de partida se conserva en
[Informe_Arquitectura_Core_Force_Mail.md](Informe_Arquitectura_Core_Force_Mail.md).

## Desarrollo

```
cp .env.example .env          # y ajusta secretos de desarrollo
make dev                      # Postgres, PgBouncer, Redis, NATS y el plano de control
make checks                   # todo lo que corre CI sin docker
make test                     # go test -race
make clean-copy               # sin restos de las bases de referencia
make new-service name=<svc> port=<puerto> module=<modulo>
```

Los motores de correo se levantan por celda con `deploy/mail/docker-compose.mail.yml`.

## Estructura

```
pkg/          kernel compartido (db multitenant, eventos JetStream, auth, middleware, ...)
services/     gateway, identity, access-control, organization, audit, scheduler, ...
migrations/   registry/ (plano de control), cell/ (directorio de correo), tenant/ (empresa)
deploy/mail/  Postfix, Dovecot, Rspamd, Unbound, ClamAV y auxiliares, adaptados a PostgreSQL
ops/          scaffold y guardarraíles, secretos, respaldos, observabilidad, ECR, AWS
docs/         documentos rectores
```
