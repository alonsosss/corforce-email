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
| `mail_security.quarantine.released` | mail-security | `id`, `rcpt`, `tenant_id`, `user_id` |
| `mail_security.quarantine.stored` | mail-security | `id`, `qid`, `rcpt`, `score`, `sender`, `subject`, `tenant_id` |
| `organization.tenant.created` | organization | `cell_id`, `db_name`, `name`, `slug`, `status`, `tenant_id` |
| `organization.tenant.modules_changed` | organization | `disabled`, `enabled`, `tenant_id` |
| `organization.tenant.status_changed` | organization | `previous_status`, `slug`, `status`, `tenant_id` |
| `scheduler.job.completed` | scheduler | `execution_id`, `job_id` |
| `scheduler.job.failed` | scheduler | `error`, `execution_id`, `job_id` |
| `scheduler.job.started` | scheduler | `execution_id`, `job_id` |
| `templates.template.published` | templates | `template_id`, `tenant_id`, `version` |

## Consumido

| Subject | Servicio | Campos que lee |
|---|---|---|
| `audit.api.write` | audit | `ip`, `method`, `module`, `path`, `request_id`, `roles`, `status`, `tenant_id`, `user_agent`, `user_id` |
| `campaigns.campaign.*` | analytics | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `contacts.contact.created` | billing | `tenant_id` |
| `contacts.contact.deleted` | billing | `tenant_id` |
| `domains.domain.*` | transactional | `domain`, `purpose`, `status`, `tenant_id` |
| `domains.domain.created` | billing | `domain`, `tenant_id` |
| `domains.domain.deleted` | billing | `domain`, `tenant_id` |
| `mail.domain.created` | billing | `domain`, `tenant_id` |
| `mail.domain.deleted` | billing | `domain`, `tenant_id` |
| `mail.mailbox.created` | billing | `tenant_id` |
| `mail.mailbox.deleted` | billing | `tenant_id` |
| `organization.tenant.created` | billing | `tenant_id` |
| `organization.tenant.status_changed` | billing | `status`, `tenant_id` |
| `transactional.email.*` | analytics | `bounce_type`, `campaign_id`, `class`, `email`, `message_id`, `occurred_at`, `tenant_id`, `to` |
| `transactional.email.sent` | billing | `class`, `tenant_id`, `to` |
| `transactional.marketing.queued` | transactional | _opaco_ |
| `transactional.message.queued` | transactional | _opaco_ |
