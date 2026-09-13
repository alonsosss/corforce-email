import type { SchedulerHandler, SchedulerJob, SchedulerMeta } from '@/api/scheduler';

/** Reglas como las sirve GET /scheduler/meta (meta.go), para las pruebas. */
export const schedulerMetaFixture: SchedulerMeta = {
  timezone: {
    default: 'UTC',
    format: 'iana',
    pattern: '^[A-Za-z][A-Za-z0-9_+\\-]*(/[A-Za-z0-9_+\\-]+)*$',
    max_length: 64,
  },
  cron: {
    descriptors: ['@daily', '@hourly', '@monthly', '@weekly'],
    min_every_seconds: 60,
    max_length: 100,
  },
  job_types: ['cron', 'interval', 'one_time'],
  limits: {
    max_name_length: 255,
    max_code_length: 100,
    max_description_length: 2000,
    max_handler_length: 255,
    max_payload_bytes: 65536,
    min_interval_minutes: 1,
    max_interval_minutes: 525600,
    max_retries: 10,
    max_timeout_seconds: 604800,
  },
  pagination: { default_per_page: 20, max_per_page: 100 },
};

export const tenantHandlerFixture: SchedulerHandler = {
  name: 'reports.daily_digest',
  service: 'analytics',
  description: 'Resumen diario de envios.',
  max_timeout_seconds: 600,
  scopes: ['tenant'],
};

export const platformHandlerFixture: SchedulerHandler = {
  name: 'platform.cleanup',
  service: 'organization',
  description: 'Limpieza de la plataforma.',
  max_timeout_seconds: 3600,
  scopes: ['platform'],
};

export function jobFixture(extra: Partial<SchedulerJob> = {}): SchedulerJob {
  return {
    id: '5b0f7c9e-4a57-4d8e-9d55-2d5f7b6f0a11',
    tenant_id: '0d9c1f7a-1f3e-4a9b-8c2d-3e4f5a6b7c8d',
    name: 'Resumen diario',
    code: 'daily-digest',
    description: null,
    job_type: 'cron',
    cron_expression: '0 7 * * *',
    timezone: 'America/Lima',
    interval_minutes: null,
    handler: tenantHandlerFixture.name,
    payload: null,
    is_active: true,
    max_retries: 2,
    timeout_seconds: 0,
    created_at: '2026-09-13T10:00:00Z',
    updated_at: '2026-09-13T10:00:00Z',
    next_run_at: '2026-09-14T12:00:00Z',
    last_run_at: null,
    last_execution: null,
    ...extra,
  };
}
