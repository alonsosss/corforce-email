import type { MigrationJob, MigrationMeta } from '@/api/mailMigration';

// Datos de prueba de las pruebas de la pestana de migracion.
export const META: MigrationMeta = {
  configured: true,
  source_ports: [143, 993],
  source_tls_modes: ['ssl', 'starttls'],
  default_tls_for_port: { '993': 'ssl', '143': 'starttls' },
  max_active_jobs: 2,
  active_jobs: 0,
  phases: ['initial', 'catchup'],
};

export function jobOf(patch: Partial<MigrationJob> = {}): MigrationJob {
  return {
    id: 'job-1',
    mailbox_id: 'mb-1',
    mailbox_username: 'ana@empresa.test',
    source_host: 'imap.origen.test',
    source_port: 993,
    source_tls: 'ssl',
    source_username: 'ana@origen.test',
    status: 'running',
    phase: 'initial',
    progress: {
      folders_total: 0,
      folders_done: 0,
      messages_total: 0,
      messages_copied: 0,
      messages_skipped: 0,
      messages_failed: 0,
      bytes_copied: 0,
      folders: [],
    },
    attempt: 1,
    last_error: null,
    requested_by: 'user-1',
    created_at: '2026-09-21T10:00:00Z',
    started_at: null,
    finished_at: null,
    cancel_requested_at: null,
    heartbeat_at: null,
    ...patch,
  };
}
