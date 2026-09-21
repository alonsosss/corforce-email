import { ApiError, errorDetail, isApiError } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import type { MigrationJob, MigrationMeta, MigrationStatus } from '@/api/mailMigration';
import type { BadgeTone } from '@/design/components';
import { hasMessage, t, type MessageKey } from '@/i18n';

const ACTIVE_STATUSES: readonly MigrationStatus[] = ['pending', 'running'];

const STATUS_TONES: Record<MigrationStatus, BadgeTone> = {
  pending: 'neutral',
  running: 'info',
  succeeded: 'success',
  failed: 'danger',
  cancelled: 'warning',
};

export function isActiveJob(job: MigrationJob): boolean {
  return ACTIVE_STATUSES.includes(job.status);
}

export function statusTone(status: MigrationStatus): BadgeTone {
  return STATUS_TONES[status] ?? 'neutral';
}

/** El trabajo activo del buzon (el servicio admite uno solo). */
export function findActiveJob(jobs: readonly MigrationJob[]): MigrationJob | null {
  return jobs.find(isActiveJob) ?? null;
}

/** Se sigue sondeando mientras haya un trabajo sin terminar y la ultima lectura no haya fallado. */
export function shouldPoll(jobs: readonly MigrationJob[] | null, failed: boolean): boolean {
  return !failed && jobs !== null && jobs.some(isActiveJob);
}

/** Mensajes ya tratados (copiados, omitidos o fallidos). */
export function messagesProcessed(job: MigrationJob): number {
  const { messages_copied, messages_skipped, messages_failed } = job.progress;
  return messages_copied + messages_skipped + messages_failed;
}

/**
 * Avance entre 0 y 1. Los mensajes mandan; mientras el ejecutor aun no los ha contado se usa
 * el de carpetas. null: todavia no hay base para calcularlo.
 */
export function progressRatio(job: MigrationJob): number | null {
  if (job.status === 'succeeded') return 1;
  const p = job.progress;
  const [done, total] =
    p.messages_total > 0
      ? [messagesProcessed(job), p.messages_total]
      : [p.folders_done, p.folders_total];
  if (!Number.isFinite(done) || !Number.isFinite(total) || total <= 0) return null;
  return Math.min(Math.max(done / total, 0), 1);
}

export function progressPercent(job: MigrationJob): number | null {
  const ratio = progressRatio(job);
  return ratio === null ? null : Math.round(ratio * 100);
}

export type LaunchBlock = 'notConfigured' | 'jobActive' | 'tenantLimit';

/** Por que no se puede lanzar otra migracion ahora; null si se puede. */
export function launchBlock(
  meta: MigrationMeta,
  jobs: readonly MigrationJob[] | null,
): LaunchBlock | null {
  if (!meta.configured) return 'notConfigured';
  if (jobs !== null && findActiveJob(jobs)) return 'jobActive';
  if (meta.max_active_jobs > 0 && meta.active_jobs >= meta.max_active_jobs) return 'tenantLimit';
  return null;
}

/** Codigos de error del API de administracion de mail-migration y su texto. */
export const MIGRATION_ERRORS: Partial<Record<string, MessageKey>> = {
  SOURCE_HOST_NOT_ALLOWED: 'migration.error.SOURCE_HOST_NOT_ALLOWED',
  MAILBOX_NOT_FOUND: 'migration.error.MAILBOX_NOT_FOUND',
  MAILBOX_INACTIVE: 'migration.error.MAILBOX_INACTIVE',
  JOB_ALREADY_ACTIVE: 'migration.error.JOB_ALREADY_ACTIVE',
  TENANT_LIMIT_REACHED: 'migration.error.TENANT_LIMIT_REACHED',
  NOT_CONFIGURED: 'migration.error.NOT_CONFIGURED',
  JOB_NOT_CANCELLABLE: 'migration.error.JOB_NOT_CANCELLABLE',
  NOT_FOUND: 'migration.error.NOT_FOUND',
};

/**
 * Texto de un error de una accion de migracion. Nunca muestra el mensaje del servidor: la
 * peticion de creacion lleva una contrasena y un error no debe poder devolverla a la pantalla.
 */
export function migrationErrorMessage(err: unknown): string {
  if (!isApiError(err)) return errorMessage(err);
  if (err.code === 'VALIDATION_ERROR') {
    const field = errorDetail(err, 'field');
    const key = field ? `migration.error.field.${field}` : '';
    return hasMessage(key) ? t(key) : t('migration.error.VALIDATION_ERROR');
  }
  return errorMessage(new ApiError(err.status, { code: err.code, message: '' }), MIGRATION_ERRORS);
}

/** Texto de last_error.code; un codigo desconocido tiene un texto generico. */
export function failureMessage(code: string): string {
  const key = `migration.failure.${code}`;
  return hasMessage(key) ? t(key) : t('migration.failure.unknown');
}
