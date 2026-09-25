# Contratos de eventos

Generado — no editar a mano. Regenera con `make gen-event-contracts`.

Campos del payload (`Event.Data`) por subject, extraidos del codigo. Un cambio en
esta tabla es un cambio de contrato: quitar o renombrar un campo rompe a sus
consumidores en tiempo de ejecucion, no de compilacion. Cuenta lo publicado en el
bus y lo encolado en la outbox (`outbox.Enqueue`), que el rele entrega con el mismo
subject y payload. `opaco` = el payload no se puede leer estaticamente.

## Publicado

| Subject | Servicio | Campos |
|---|---|---|
| `access.api_key.created` | access-control | _opaco_ |
| `access.api_key.revoked` | access-control | _opaco_ |
| `audit.api.write` | gateway | `api_key_id`, `ip`, `method`, `module`, `path`, `request_id`, `roles`, `status`, `target_cell`, `tenant_id`, `user_agent`, `user_id` |
| `audit.chain.anchored` | audit | `anchored_at`, `chain`, `hash_version`, `head_hash`, `head_seq`, `tenant_id` |
| `audit.security.alert` | audit | `detail`, `event_type`, `ip`, `risk_level`, `user_id` |
| `automations.run.completed` | automations | `contact_id`, `reason`, `run_id`, `tenant_id`, `workflow_id` |
| `automations.run.failed` | automations | `contact_id`, `reason`, `run_id`, `tenant_id`, `workflow_id` |
| `automations.workflow.activated` | automations | `reason`, `tenant_id`, `workflow_id` |
| `automations.workflow.archived` | automations | `reason`, `tenant_id`, `workflow_id` |
| `automations.workflow.paused` | automations | `reason`, `tenant_id`, `workflow_id` |
| `billing.limit.reached` | billing | `limit`, `period_start`, `resource`, `tenant_id`, `used` |
| `billing.period.closed` | billing | `period_end`, `period_start`, `plan_code`, `subscription_id`, `tenant_id`, `usage` |
| `billing.subscription.changed` | billing | `cancel_at`, `current_period_end`, `current_period_start`, `plan_code`, `plan_id`, `previous_plan_code`, `previous_status`, `status`, `subscription_id`, `tenant_id`, `trial_ends_at` |
| `billing.subscription.created` | billing | `cancel_at`, `current_period_end`, `current_period_start`, `plan_code`, `plan_id`, `status`, `subscription_id`, `tenant_id`, `trial_ends_at` |
| `billing.subscription.suspended` | billing | `cancel_at`, `current_period_end`, `current_period_start`, `plan_code`, `plan_id`, `previous_status`, `status`, `subscription_id`, `tenant_id`, `trial_ends_at` |
| `campaigns.campaign.ab_decided` | campaigns | `campaign_id`, `criterion`, `occurred_at`, `reason`, `results`, `status`, `tenant_id`, `winner` |
| `campaigns.campaign.cancelled` | campaigns | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `campaigns.campaign.completed` | campaigns | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `campaigns.campaign.failed` | campaigns | `campaign_id`, `occurred_at`, `reason`, `status`, `tenant_id` |
| `campaigns.campaign.paused` | campaigns | `campaign_id`, `occurred_at`, `reason`, `status`, `tenant_id` |
| `campaigns.campaign.resumed` | campaigns | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `campaigns.campaign.scheduled` | campaigns | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `campaigns.campaign.started` | campaigns | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `contacts.consent.granted` | contacts | `consent_id`, `contact_id`, `method`, `purpose`, `tenant_id` |
| `contacts.consent.requested` | contacts | `confirm_url`, `contact_id`, `email`, `first_name`, `tenant_id` |
| `contacts.consent.revoked` | contacts | `consent_id`, `contact_id`, `method`, `purpose`, `tenant_id` |
| `contacts.contact.created` | contacts | `contact_id`, `source`, `tenant_id` |
| `contacts.contact.deleted` | contacts | `contact_id`, `tenant_id` |
| `contacts.contact.resubscribed` | contacts | `consented_at`, `email`, `tenant_id` |
| `contacts.contact.updated` | contacts | `changed`, `contact_id`, `status`, `tenant_id` |
| `contacts.import.completed` | contacts | `created`, `import_id`, `list_id`, `skipped`, `tenant_id`, `total`, `updated` |
| `domains.dns_provider.connected` | domain-service | `connected_at`, `provider`, `tenant_id`, `zones_visible` |
| `domains.dns_provider.disconnected` | domain-service | `disconnected_at`, `domains_reset`, `provider`, `tenant_id` |
| `domains.domain.created` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.deleted` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.dkim_revoked` | domain-service | `domain`, `domain_id`, `purpose`, `reason`, `remove_dns_records`, `revoked_selectors`, `rotated_at`, `selector`, `status`, `tenant_id` |
| `domains.domain.dkim_rotated` | domain-service | `domain`, `domain_id`, `previous_selector`, `purpose`, `rotated_at`, `selector`, `status`, `tenant_id` |
| `domains.domain.dns_published` | domain-service | `conflicts`, `created`, `domain`, `domain_id`, `failed`, `provider`, `published_at`, `purpose`, `removed`, `replaced`, `status`, `tenant_id`, `unchanged`, `updated`, `zone` |
| `domains.domain.failed` | domain-service | `domain`, `domain_id`, `purpose`, `status`, `tenant_id` |
| `domains.domain.sending_status_changed` | domain-service | `domain`, `domain_id`, `purpose`, `sending_ready`, `ses_identity_status`, `status`, `tenant_id` |
| `domains.domain.verified` | domain-service | `domain`, `domain_id`, `purpose`, `sending_ready`, `status`, `tenant_id` |
| `gateway.security.exfiltration` | gateway | `count`, `ip`, `tenant_id`, `user_id`, `window` |
| `identity.session.revoked_by_admin` | identity | `ip`, `session_id`, `target_user_id` |
| `identity.user.created` | identity | `email` |
| `identity.user.deleted` | identity | `deleted_at`, `tenant_id`, `user_id` |
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
| `mail.mailbox.credentials_changed` | mail-directory | `changed`, `changed_at`, `credential`, `id`, `tenant_id`, `username` |
| `mail.mailbox.deleted` | mail-directory | `active`, `domain`, `id`, `kind`, `tenant_id`, `username` |
| `mail.mailbox.forwarding_changed` | mail-directory | `at`, `external_added`, `external_removed`, `forwarding_enabled`, `id`, `tenant_id`, `username` |
| `mail.mailbox.mfa_disabled` | mail-directory | `actor_id`, `at`, `by`, `id`, `tenant_id`, `username` |
| `mail.mailbox.mfa_enabled` | mail-directory | `at`, `id`, `tenant_id`, `username` |
| `mail.mailbox.updated` | mail-directory | `active`, `changed`, `domain`, `id`, `kind`, `tenant_id`, `username` |
| `mail.policy.updated` | mail-directory | `external_forwarding_allowed`, `removed_mailboxes`, `tenant_id`, `updated_by` |
| `mail_security.quarantine.released` | mail-security | `id`, `rcpt`, `tenant_id`, `user_id` |
| `mail_security.quarantine.stored` | mail-security | `id`, `qid`, `rcpt`, `score`, `sender`, `subject`, `tenant_id` |
| `migration.job.cancel_requested` | mail-migration | `job_id`, `mailbox_id`, `status`, `tenant_id` |
| `migration.job.cancelled` | mail-migration | `bytes_copied`, `error_code`, `folders_done`, `job_id`, `mailbox_id`, `messages_copied`, `messages_failed`, `messages_skipped`, `status`, `tenant_id` |
| `migration.job.completed` | mail-migration | `bytes_copied`, `error_code`, `folders_done`, `job_id`, `mailbox_id`, `messages_copied`, `messages_failed`, `messages_skipped`, `status`, `tenant_id` |
| `migration.job.created` | mail-migration | `job_id`, `mailbox_id`, `mailbox_username`, `source_host`, `source_port`, `source_tls`, `status`, `tenant_id` |
| `migration.job.failed` | mail-migration | `bytes_copied`, `error_code`, `folders_done`, `job_id`, `mailbox_id`, `messages_copied`, `messages_failed`, `messages_skipped`, `status`, `tenant_id` |
| `migration.job.started` | mail-migration | `attempt`, `job_id`, `mailbox_id`, `status`, `tenant_id` |
| `organization.tenant.created` | organization | `cell_id`, `db_name`, `name`, `slug`, `status`, `tenant_id` |
| `organization.tenant.modules_changed` | organization | `disabled`, `enabled`, `tenant_id` |
| `organization.tenant.status_changed` | organization | `previous_status`, `slug`, `status`, `tenant_id` |
| `reputation.tenant.state_changed` | reputation | `bounce_rate`, `class`, `complaint_rate`, `from`, `manual`, `reason`, `tenant_id`, `to` |
| `scheduler.job.completed` | scheduler | `attempt`, `duration_ms`, `execution_id`, `handler`, `job_id`, `tenant_id` |
| `scheduler.job.failed` | scheduler | `attempt`, `error`, `execution_id`, `handler`, `job_id`, `reason`, `retry_at`, `retry_execution_id`, `tenant_id` |
| `scheduler.job.started` | scheduler | `attempt`, `execution_id`, `handler`, `job_id`, `payload`, `tenant_id`, `timeout_seconds` |
| `scheduler.task.started` | scheduler | `handler`, `name`, `payload`, `task_id`, `tenant_id` |
| `suppression.entry.added` | suppression | `email`, `reason`, `reasons`, `source`, `tenant_id` |
| `suppression.entry.expired` | suppression | `email`, `expires_at`, `reason`, `reasons`, `source`, `tenant_id` |
| `suppression.entry.removed` | suppression | `email`, `reason`, `reasons`, `source`, `tenant_id` |
| `templates.template.published` | templates | `template_id`, `tenant_id`, `version` |
| `transactional.email.bounced` | transactional | `bounce_type`, `campaign_id`, `class`, `contact_id`, `detail`, `email`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.clicked` | transactional | `campaign_id`, `class`, `contact_id`, `email`, `link`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.complained` | transactional | `campaign_id`, `class`, `contact_id`, `detail`, `email`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.delivered` | transactional | `campaign_id`, `class`, `contact_id`, `email`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.failed` | transactional | `campaign_id`, `class`, `code`, `contact_id`, `error`, `message_id`, `occurred_at`, `tenant_id`, `test`, `to` |
| `transactional.email.opened` | transactional | `campaign_id`, `class`, `contact_id`, `email`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.sent` | transactional | `campaign_id`, `class`, `contact_id`, `message_id`, `occurred_at`, `ses_message_id`, `tenant_id`, `test`, `to` |
| `transactional.email.unsubscribed` | transactional | `campaign_id`, `class`, `contact_id`, `email`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.marketing.queued` | transactional | `message_id`, `tenant_id` |
| `transactional.message.queued` | transactional | `message_id`, `tenant_id` |
| `webmail.assistant.used` | webmail | `action`, `at`, `input_chars`, `input_tokens`, `mailbox_id`, `messages`, `model`, `outcome`, `output_chars`, `output_tokens`, `tenant_id`, `username` |

## Consumido

| Subject | Servicio | Campos que lee |
|---|---|---|
| `audit.api.write` | audit | `api_key_id`, `ip`, `method`, `module`, `path`, `request_id`, `roles`, `status`, `target_cell`, `tenant_id`, `user_agent`, `user_id` |
| `campaigns.campaign.*` | analytics | `campaign_id`, `occurred_at`, `status`, `tenant_id` |
| `contacts.consent.granted` | automations | `contact_id`, `purpose`, `tenant_id` |
| `contacts.consent.requested` | automations | `confirm_url`, `contact_id`, `email`, `first_name`, `tenant_id` |
| `contacts.contact.created` | automations | `contact_id`, `tenant_id` |
| `contacts.contact.created` | billing | `tenant_id` |
| `contacts.contact.deleted` | billing | `tenant_id` |
| `contacts.contact.resubscribed` | suppression | `consented_at`, `email`, `tenant_id` |
| `domains.domain.*` | transactional | `domain`, `purpose`, `sending_ready`, `status`, `tenant_id` |
| `domains.domain.created` | billing | `domain`, `tenant_id` |
| `domains.domain.deleted` | billing | `domain`, `tenant_id` |
| `identity.user.deleted` | access-control | `tenant_id`, `user_id` |
| `mail.>` | mail-security | `domain`, `username` |
| `mail.domain.created` | billing | `domain`, `tenant_id` |
| `mail.domain.deleted` | billing | `domain`, `tenant_id` |
| `mail.mailbox.>` | mail-security | `username` |
| `mail.mailbox.>` | webmail | `changed`, `changed_at`, `credential`, `username` |
| `mail.mailbox.created` | billing | `tenant_id` |
| `mail.mailbox.deleted` | billing | `tenant_id` |
| `mail.mailbox.deleted` | mail-dav | `id`, `tenant_id` |
| `mail.mailbox.deleted` | mail-migration | `id`, `tenant_id` |
| `organization.tenant.created` | billing | `tenant_id` |
| `organization.tenant.status_changed` | billing | `status`, `tenant_id` |
| `scheduler.job.started` | analytics | _opaco_ |
| `scheduler.task.started` | analytics | _opaco_ |
| `suppression.entry.added` | contacts | `email`, `reason`, `reasons`, `source`, `tenant_id` |
| `suppression.entry.expired` | contacts | `email`, `reason`, `reasons`, `source`, `tenant_id` |
| `suppression.entry.removed` | contacts | `email`, `reason`, `reasons`, `source`, `tenant_id` |
| `transactional.email.*` | analytics | `bounce_type`, `campaign_id`, `class`, `contact_id`, `email`, `link`, `message_id`, `occurred_at`, `tenant_id`, `test`, `to` |
| `transactional.email.>` | campaigns | _opaco_ |
| `transactional.email.bounced` | reputation | _opaco_ |
| `transactional.email.bounced` | suppression | `bounce_type`, `detail`, `email`, `message_id`, `tenant_id` |
| `transactional.email.clicked` | automations | `campaign_id`, `class`, `contact_id`, `tenant_id` |
| `transactional.email.clicked` | automations | `campaign_id`, `class`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.clicked` | contacts | `campaign_id`, `class`, `contact_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.complained` | reputation | _opaco_ |
| `transactional.email.complained` | suppression | `detail`, `email`, `message_id`, `tenant_id` |
| `transactional.email.delivered` | contacts | `campaign_id`, `class`, `contact_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.opened` | automations | `campaign_id`, `class`, `message_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.opened` | contacts | `campaign_id`, `class`, `contact_id`, `occurred_at`, `tenant_id`, `test` |
| `transactional.email.sent` | billing | `class`, `tenant_id`, `test`, `to` |
| `transactional.email.sent` | reputation | _opaco_ |
| `transactional.marketing.queued` | transactional | _opaco_ |
| `transactional.message.queued` | transactional | _opaco_ |
