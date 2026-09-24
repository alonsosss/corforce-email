# Registro de contratos de eventos (NATS)

Generado por `ops/scaffold/gen-events.sh` desde el codigo. NO editar a mano.
Convencion de subject: `<dominio>.<entidad>.<accion>`. Un subject tiene UN dueno
(el servicio que lo publica); los demas solo lo consumen (regla no-fork).
Publicar incluye encolar en la outbox (`outbox.Enqueue`); un consumidor con comodin
(`*`, `>`) figura en cada subject publicado que recibe.

Resumen: 94 publicaciones, 43 suscripciones, 94 subjects distintos.

## Cruce por subject (dueno -> consumidores)

| Subject | Publica | Consumen |
|---|---|---|
| `access.api_key.created` | access-control | - |
| `access.api_key.revoked` | access-control | - |
| `audit.api.write` | gateway | audit |
| `audit.chain.anchored` | audit | - |
| `audit.security.alert` | audit | - |
| `automations.run.completed` | automations | - |
| `automations.run.failed` | automations | - |
| `automations.workflow.activated` | automations | - |
| `automations.workflow.archived` | automations | - |
| `automations.workflow.paused` | automations | - |
| `billing.limit.reached` | billing | - |
| `billing.period.closed` | billing | - |
| `billing.subscription.changed` | billing | - |
| `billing.subscription.created` | billing | - |
| `billing.subscription.suspended` | billing | - |
| `campaigns.campaign.ab_decided` | campaigns | analytics |
| `campaigns.campaign.cancelled` | campaigns | analytics |
| `campaigns.campaign.completed` | campaigns | analytics |
| `campaigns.campaign.failed` | campaigns | analytics |
| `campaigns.campaign.paused` | campaigns | analytics |
| `campaigns.campaign.resumed` | campaigns | analytics |
| `campaigns.campaign.scheduled` | campaigns | analytics |
| `campaigns.campaign.started` | campaigns | analytics |
| `contacts.consent.granted` | contacts | automations |
| `contacts.consent.requested` | contacts | automations |
| `contacts.consent.revoked` | contacts | - |
| `contacts.contact.created` | contacts | automations, billing |
| `contacts.contact.deleted` | contacts | billing |
| `contacts.contact.resubscribed` | contacts | suppression |
| `contacts.contact.updated` | contacts | - |
| `contacts.import.completed` | contacts | - |
| `domains.dns_provider.connected` | domain-service | - |
| `domains.dns_provider.disconnected` | domain-service | - |
| `domains.domain.created` | domain-service | billing, transactional |
| `domains.domain.deleted` | domain-service | billing, transactional |
| `domains.domain.dkim_revoked` | domain-service | transactional |
| `domains.domain.dkim_rotated` | domain-service | transactional |
| `domains.domain.dns_published` | domain-service | transactional |
| `domains.domain.failed` | domain-service | transactional |
| `domains.domain.sending_status_changed` | domain-service | transactional |
| `domains.domain.verified` | domain-service | transactional |
| `gateway.security.exfiltration` | gateway | - |
| `identity.session.revoked_by_admin` | identity | - |
| `identity.user.created` | identity | - |
| `identity.user.deleted` | identity | access-control |
| `identity.user.locked` | identity | - |
| `identity.user.logged_in` | identity | - |
| `identity.user.logged_out` | identity | - |
| `identity.user.login_failed` | identity | - |
| `identity.user.password_changed` | identity | - |
| `mail.alias.created` | mail-directory | mail-security |
| `mail.alias.deleted` | mail-directory | mail-security |
| `mail.alias.updated` | mail-directory | mail-security |
| `mail.alias_domain.created` | mail-directory | mail-security |
| `mail.alias_domain.deleted` | mail-directory | mail-security |
| `mail.alias_domain.updated` | mail-directory | mail-security |
| `mail.domain.activated` | mail-directory | mail-security |
| `mail.domain.created` | mail-directory | billing, mail-security |
| `mail.domain.deleted` | mail-directory | billing, mail-security |
| `mail.domain.updated` | mail-directory | mail-security |
| `mail.mailbox.created` | mail-directory | billing, mail-security, webmail |
| `mail.mailbox.credentials_changed` | mail-directory | mail-security, webmail |
| `mail.mailbox.deleted` | mail-directory | billing, mail-dav, mail-migration, mail-security, webmail |
| `mail.mailbox.updated` | mail-directory | mail-security, webmail |
| `mail_security.quarantine.released` | mail-security | - |
| `mail_security.quarantine.stored` | mail-security | - |
| `migration.job.cancel_requested` | mail-migration | - |
| `migration.job.cancelled` | mail-migration | - |
| `migration.job.completed` | mail-migration | - |
| `migration.job.created` | mail-migration | - |
| `migration.job.failed` | mail-migration | - |
| `migration.job.started` | mail-migration | - |
| `organization.tenant.created` | organization | billing |
| `organization.tenant.modules_changed` | organization | - |
| `organization.tenant.status_changed` | organization | billing |
| `reputation.tenant.state_changed` | reputation | - |
| `scheduler.job.completed` | scheduler | - |
| `scheduler.job.failed` | scheduler | - |
| `scheduler.job.started` | scheduler | analytics |
| `scheduler.task.started` | scheduler | analytics |
| `suppression.entry.added` | suppression | contacts |
| `suppression.entry.expired` | suppression | contacts |
| `suppression.entry.removed` | suppression | contacts |
| `templates.template.published` | templates | - |
| `transactional.email.bounced` | transactional | analytics, campaigns, reputation, suppression |
| `transactional.email.clicked` | transactional | analytics, automations, campaigns, contacts |
| `transactional.email.complained` | transactional | analytics, campaigns, reputation, suppression |
| `transactional.email.delivered` | transactional | analytics, campaigns, contacts |
| `transactional.email.failed` | transactional | analytics, campaigns |
| `transactional.email.opened` | transactional | analytics, automations, campaigns, contacts |
| `transactional.email.sent` | transactional | analytics, billing, campaigns, reputation |
| `transactional.email.unsubscribed` | transactional | analytics, campaigns |
| `transactional.marketing.queued` | transactional | transactional |
| `transactional.message.queued` | transactional | transactional |

## Por servicio

### access-control
- Publica: `access.api_key.created`, `access.api_key.revoked`
- Consume: `identity.user.deleted`

### analytics
- Consume: `campaigns.campaign.*`, `scheduler.job.started`, `scheduler.task.started`, `transactional.email.*`

### audit
- Publica: `audit.chain.anchored`, `audit.security.alert`
- Consume: `audit.api.write`

### automations
- Publica: `automations.run.completed`, `automations.run.failed`, `automations.workflow.activated`, `automations.workflow.archived`, `automations.workflow.paused`
- Consume: `contacts.consent.granted`, `contacts.consent.requested`, `contacts.contact.created`, `transactional.email.clicked`, `transactional.email.opened`

### billing
- Publica: `billing.limit.reached`, `billing.period.closed`, `billing.subscription.changed`, `billing.subscription.created`, `billing.subscription.suspended`
- Consume: `contacts.contact.created`, `contacts.contact.deleted`, `domains.domain.created`, `domains.domain.deleted`, `mail.domain.created`, `mail.domain.deleted`, `mail.mailbox.created`, `mail.mailbox.deleted`, `organization.tenant.created`, `organization.tenant.status_changed`, `transactional.email.sent`

### campaigns
- Publica: `campaigns.campaign.ab_decided`, `campaigns.campaign.cancelled`, `campaigns.campaign.completed`, `campaigns.campaign.failed`, `campaigns.campaign.paused`, `campaigns.campaign.resumed`, `campaigns.campaign.scheduled`, `campaigns.campaign.started`
- Consume: `transactional.email.>`

### contacts
- Publica: `contacts.consent.granted`, `contacts.consent.requested`, `contacts.consent.revoked`, `contacts.contact.created`, `contacts.contact.deleted`, `contacts.contact.resubscribed`, `contacts.contact.updated`, `contacts.import.completed`
- Consume: `suppression.entry.added`, `suppression.entry.expired`, `suppression.entry.removed`, `transactional.email.clicked`, `transactional.email.delivered`, `transactional.email.opened`

### domain-service
- Publica: `domains.dns_provider.connected`, `domains.dns_provider.disconnected`, `domains.domain.created`, `domains.domain.deleted`, `domains.domain.dkim_revoked`, `domains.domain.dkim_rotated`, `domains.domain.dns_published`, `domains.domain.failed`, `domains.domain.sending_status_changed`, `domains.domain.verified`

### gateway
- Publica: `audit.api.write`, `gateway.security.exfiltration`

### identity
- Publica: `identity.session.revoked_by_admin`, `identity.user.created`, `identity.user.deleted`, `identity.user.locked`, `identity.user.logged_in`, `identity.user.logged_out`, `identity.user.login_failed`, `identity.user.password_changed`

### mail-dav
- Consume: `mail.mailbox.deleted`

### mail-directory
- Publica: `mail.alias.created`, `mail.alias.deleted`, `mail.alias.updated`, `mail.alias_domain.created`, `mail.alias_domain.deleted`, `mail.alias_domain.updated`, `mail.domain.activated`, `mail.domain.created`, `mail.domain.deleted`, `mail.domain.updated`, `mail.mailbox.created`, `mail.mailbox.credentials_changed`, `mail.mailbox.deleted`, `mail.mailbox.updated`

### mail-migration
- Publica: `migration.job.cancel_requested`, `migration.job.cancelled`, `migration.job.completed`, `migration.job.created`, `migration.job.failed`, `migration.job.started`
- Consume: `mail.mailbox.deleted`

### mail-security
- Publica: `mail_security.quarantine.released`, `mail_security.quarantine.stored`
- Consume: `mail.>`, `mail.mailbox.>`

### organization
- Publica: `organization.tenant.created`, `organization.tenant.modules_changed`, `organization.tenant.status_changed`

### reputation
- Publica: `reputation.tenant.state_changed`
- Consume: `transactional.email.bounced`, `transactional.email.complained`, `transactional.email.sent`

### scheduler
- Publica: `scheduler.job.completed`, `scheduler.job.failed`, `scheduler.job.started`, `scheduler.task.started`

### suppression
- Publica: `suppression.entry.added`, `suppression.entry.expired`, `suppression.entry.removed`
- Consume: `contacts.contact.resubscribed`, `transactional.email.bounced`, `transactional.email.complained`

### templates
- Publica: `templates.template.published`

### transactional
- Publica: `transactional.email.bounced`, `transactional.email.clicked`, `transactional.email.complained`, `transactional.email.delivered`, `transactional.email.failed`, `transactional.email.opened`, `transactional.email.sent`, `transactional.email.unsubscribed`, `transactional.marketing.queued`, `transactional.message.queued`
- Consume: `domains.domain.*`, `transactional.marketing.queued`, `transactional.message.queued`

### webmail
- Consume: `mail.mailbox.>`

