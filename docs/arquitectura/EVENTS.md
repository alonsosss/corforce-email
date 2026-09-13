# Registro de contratos de eventos (NATS)

Generado por `ops/scaffold/gen-events.sh` desde el codigo. NO editar a mano.
Convencion de subject: `<dominio>.<entidad>.<accion>`. Un subject tiene UN dueno
(el servicio que lo publica); los demas solo lo consumen (regla no-fork).

Resumen: 36 publicaciones, 7 suscripciones, 39 subjects distintos.

## Cruce por subject (dueno -> consumidores)

| Subject | Publica | Consumen |
|---|---|---|
| `audit.api.write` | gateway | audit |
| `audit.security.alert` | audit | - |
| `automations.run.completed` | automations | - |
| `automations.run.failed` | automations | - |
| `automations.workflow.activated` | automations | - |
| `automations.workflow.archived` | automations | - |
| `automations.workflow.paused` | automations | - |
| `campaigns.campaign.cancelled` | campaigns | - |
| `campaigns.campaign.completed` | campaigns | - |
| `campaigns.campaign.failed` | campaigns | - |
| `campaigns.campaign.paused` | campaigns | - |
| `campaigns.campaign.resumed` | campaigns | - |
| `campaigns.campaign.scheduled` | campaigns | - |
| `campaigns.campaign.started` | campaigns | - |
| `contacts.consent.granted` | (ninguno: subject huerfano) | automations |
| `contacts.consent.requested` | (ninguno: subject huerfano) | automations |
| `contacts.contact.created` | (ninguno: subject huerfano) | automations |
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
| `transactional.email.clicked` | transactional | automations |
| `transactional.email.complained` | transactional | - |
| `transactional.email.delivered` | transactional | - |
| `transactional.email.failed` | transactional | - |
| `transactional.email.opened` | transactional | - |
| `transactional.email.sent` | transactional | - |
| `transactional.email.unsubscribed` | transactional | - |
| `transactional.marketing.queued` | transactional | transactional |
| `transactional.message.queued` | transactional | transactional |

## Por servicio

### audit
- Publica: `audit.security.alert`
- Consume: `audit.api.write`

### automations
- Publica: `automations.run.completed`,`automations.run.failed` `automations.workflow.activated`,`automations.workflow.archived` `automations.workflow.paused`
- Consume: `contacts.consent.granted`,`contacts.consent.requested` `contacts.contact.created`,`transactional.email.clicked`

### campaigns
- Publica: `campaigns.campaign.cancelled`,`campaigns.campaign.completed` `campaigns.campaign.failed`,`campaigns.campaign.paused` `campaigns.campaign.resumed`,`campaigns.campaign.scheduled` `campaigns.campaign.started`

### gateway
- Publica: `audit.api.write`,`gateway.security.exfiltration`

### identity
- Publica: `identity.session.revoked_by_admin`,`identity.user.created` `identity.user.locked`,`identity.user.logged_in` `identity.user.logged_out`,`identity.user.login_failed` `identity.user.password_changed`

### scheduler
- Publica: `scheduler.job.completed`,`scheduler.job.failed` `scheduler.job.started`

### templates
- Publica: `templates.template.published`

### transactional
- Publica: `transactional.email.bounced`,`transactional.email.clicked` `transactional.email.complained`,`transactional.email.delivered` `transactional.email.failed`,`transactional.email.opened` `transactional.email.sent`,`transactional.email.unsubscribed` `transactional.marketing.queued`,`transactional.message.queued`
- Consume: `transactional.marketing.queued`,`transactional.message.queued`

