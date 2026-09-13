# Contratos de eventos

Generado — no editar a mano. Regenera con `make gen-event-contracts`.

Campos del payload (`Event.Data`) por subject, extraidos del codigo. Un cambio en
esta tabla es un cambio de contrato: quitar o renombrar un campo rompe a sus
consumidores en tiempo de ejecucion, no de compilacion. `opaco` = el payload se
construye fuera del literal y no se puede leer estaticamente.

## Publicado

| Subject | Servicio | Campos |
|---|---|---|
| `audit.security.alert` | audit | `detail`, `event_type`, `ip`, `risk_level`, `user_id` |
| `domains.domain.created` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.deleted` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.dkim_rotated` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.failed` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.verified` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `identity.session.revoked_by_admin` | identity | `ip`, `session_id`, `target_user_id` |
| `identity.user.created` | identity | `email` |
| `identity.user.locked` | identity | _opaco_ |
| `identity.user.logged_in` | identity | `ip`, `user_agent` |
| `identity.user.logged_out` | identity | _opaco_ |
| `identity.user.login_failed` | identity | `email`, `ip`, `user_agent` |
| `identity.user.password_changed` | identity | _opaco_ |
| `mail.alias.created` | mail-directory | `active`, `address`, `domain`, `goto`, `id`, `internal`, `tenant_id` |
| `mail.alias.deleted` | mail-directory | `active`, `address`, `domain`, `goto`, `id`, `internal`, `tenant_id` |
| `mail.alias.updated` | mail-directory | `active`, `address`, `domain`, `goto`, `id`, `internal`, `tenant_id` |
| `mail.alias_domain.created` | mail-directory | `active`, `alias_domain`, `id`, `target_domain`, `tenant_id` |
| `mail.alias_domain.deleted` | mail-directory | `active`, `alias_domain`, `id`, `target_domain`, `tenant_id` |
| `mail.alias_domain.updated` | mail-directory | `active`, `alias_domain`, `id`, `target_domain`, `tenant_id` |
| `mail.domain.activated` | mail-directory | `active`, `backupmx`, `domain`, `id`, `tenant_id` |
| `mail.domain.created` | mail-directory | `active`, `backupmx`, `domain`, `id`, `tenant_id` |
| `mail.domain.deleted` | mail-directory | `active`, `backupmx`, `domain`, `id`, `tenant_id` |
| `mail.domain.updated` | mail-directory | `active`, `backupmx`, `domain`, `id`, `tenant_id` |
| `mail.mailbox.created` | mail-directory | `active`, `domain`, `id`, `kind`, `tenant_id`, `username` |
| `mail.mailbox.credentials_changed` | mail-directory | `changed_at`, `id`, `tenant_id`, `username` |
| `mail.mailbox.deleted` | mail-directory | `active`, `domain`, `id`, `kind`, `tenant_id`, `username` |
| `mail.mailbox.updated` | mail-directory | `active`, `domain`, `id`, `kind`, `tenant_id`, `username` |
| `organization.tenant.created` | organization | `cell_id`, `db_name`, `name`, `slug`, `status`, `tenant_id` |
| `organization.tenant.modules_changed` | organization | `disabled`, `enabled`, `tenant_id` |
| `organization.tenant.status_changed` | organization | `previous_status`, `slug`, `status`, `tenant_id` |
| `scheduler.job.completed` | scheduler | `attempt`, `duration_ms`, `execution_id`, `handler`, `job_id`, `tenant_id` |
| `scheduler.job.failed` | scheduler | `attempt`, `error`, `execution_id`, `handler`, `job_id`, `reason`, `retry_at`, `retry_execution_id`, `tenant_id` |
| `scheduler.job.started` | scheduler | `attempt`, `execution_id`, `handler`, `job_id`, `payload`, `tenant_id`, `timeout_seconds` |
| `templates.template.published` | templates | `template_id`, `tenant_id`, `version` |

## Consumido

| Subject | Servicio | Campos que lee |
|---|---|---|
| `audit.api.write` | audit | `ip`, `method`, `module`, `path`, `request_id`, `roles`, `status`, `tenant_id`, `user_agent`, `user_id` |
| `campaigns.campaign.*` | analytics | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `contacts.consent.granted` | automations | `contact_id`, `purpose` |
| `contacts.consent.requested` | automations | `confirm_url`, `contact_id`, `email`, `first_name` |
| `contacts.contact.created` | automations | `contact_id` |
| `contacts.contact.created` | billing | `tenant_id` |
| `contacts.contact.deleted` | billing | `tenant_id` |
| `domains.domain.*` | transactional | `domain`, `purpose`, `status`, `tenant_id` |
| `domains.domain.created` | billing | `domain`, `tenant_id` |
| `domains.domain.deleted` | billing | `domain`, `tenant_id` |
| `mail.domain.created` | billing | `domain`, `tenant_id` |
| `mail.domain.deleted` | billing | `domain`, `tenant_id` |
| `mail.mailbox.>` | webmail | `username` |
| `mail.mailbox.created` | billing | `tenant_id` |
| `mail.mailbox.deleted` | billing | `tenant_id` |
| `organization.tenant.created` | billing | `tenant_id` |
| `organization.tenant.status_changed` | billing | `status`, `tenant_id` |
| `transactional.email.*` | analytics | `bounce_type`, `campaign_id`, `class`, `email`, `message_id`, `occurred_at`, `tenant_id`, `to` |
| `transactional.email.>` | campaigns | _opaco_ |
| `transactional.email.clicked` | automations | `campaign_id`, `class`, `contact_id` |
| `transactional.email.sent` | billing | `class`, `tenant_id`, `to` |
| `transactional.marketing.queued` | transactional | _opaco_ |
| `transactional.message.queued` | transactional | _opaco_ |
