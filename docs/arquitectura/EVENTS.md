# Registro de contratos de eventos (NATS)

Generado por `ops/scaffold/gen-events.sh` desde el codigo. NO editar a mano.
Convencion de subject: `<dominio>.<entidad>.<accion>`. Un subject tiene UN dueno
(el servicio que lo publica); los demas solo lo consumen (regla no-fork).

Resumen: 20 publicaciones, 2 suscripciones, 20 subjects distintos.

## Cruce por subject (dueno -> consumidores)

| Subject | Publica | Consumen |
|---|---|---|
| `audit.api.write` | gateway | audit |
| `audit.security.alert` | audit | - |
| `gateway.security.exfiltration` | gateway | - |
| `identity.session.revoked_by_admin` | identity | - |
| `identity.user.created` | identity | - |
| `identity.user.locked` | identity | - |
| `identity.user.logged_in` | identity | - |
| `identity.user.logged_out` | identity | - |
| `identity.user.login_failed` | identity | - |
| `identity.user.password_changed` | identity | - |
| `scheduler.job.completed` | scheduler | - |
| `scheduler.job.failed` | scheduler | - |
| `scheduler.job.started` | scheduler | - |
| `templates.template.published` | templates | - |
| `transactional.email.bounced` | transactional | - |
| `transactional.email.complained` | transactional | - |
| `transactional.email.failed` | transactional | - |
| `transactional.email.sent` | transactional | - |
| `transactional.email.unsubscribed` | transactional | - |
| `transactional.message.queued` | transactional | transactional |

## Por servicio

### audit
- Publica: `audit.security.alert`
- Consume: `audit.api.write`

### gateway
- Publica: `audit.api.write`,`gateway.security.exfiltration`

### identity
- Publica: `identity.session.revoked_by_admin`,`identity.user.created` `identity.user.locked`,`identity.user.logged_in` `identity.user.logged_out`,`identity.user.login_failed` `identity.user.password_changed`

### scheduler
- Publica: `scheduler.job.completed`,`scheduler.job.failed` `scheduler.job.started`

### templates
- Publica: `templates.template.published`

### transactional
- Publica: `transactional.email.bounced`,`transactional.email.complained` `transactional.email.failed`,`transactional.email.sent` `transactional.email.unsubscribed`,`transactional.message.queued`
- Consume: `transactional.message.queued`

