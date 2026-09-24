import type { AutomationsMeta } from '@/api/automations';

/** Catalogo como lo sirve GET /automations/meta, para las pruebas. */
export const automationsMetaFixture: AutomationsMeta = {
  statuses: [
    {
      status: 'draft',
      editable: true,
      can_activate: true,
      can_pause: false,
      can_archive: true,
      deletable: true,
    },
    {
      status: 'active',
      editable: false,
      can_activate: false,
      can_pause: true,
      can_archive: true,
      deletable: false,
    },
  ],
  trigger_types: [
    { type: 'contact.created', campaign_filter: false, date: false },
    { type: 'email.clicked', campaign_filter: true, date: false },
    { type: 'contact.date', campaign_filter: false, date: true },
  ],
  step_types: ['wait', 'send_email', 'add_to_list', 'remove_from_list', 'branch'],
  condition_kinds: [
    { kind: 'email_opened', step: true },
    { kind: 'email_clicked', step: true },
    { kind: 'segment', step: false },
    { kind: 'attribute', step: false },
  ],
  wait: {
    units: [
      { unit: 'm', seconds: 60 },
      { unit: 'h', seconds: 3600 },
      { unit: 'd', seconds: 86400 },
    ],
    min_seconds: 60,
    max_seconds: 7776000,
  },
  run_statuses: ['waiting', 'running', 'completed', 'failed', 'cancelled', 'skipped'],
  doi_statuses: ['pending', 'sent', 'skipped', 'failed'],
  pause_reason_manual: 'manual',
  template_kinds: { send_email: 'marketing', double_opt_in: 'transactional' },
  doi_limits: { per_day: 1, per_30_days: 3 },
  limits: {
    min_steps: 1,
    max_steps: 40,
    max_name_length: 200,
    max_description_length: 2000,
    max_pause_reason_length: 500,
    max_search_length: 200,
    max_depth: 20,
    max_step_id_length: 32,
    max_trigger_hour: 23,
    max_condition_value_bytes: 8192,
  },
  pagination: { default_page_size: 25, max_page_size: 100 },
};
