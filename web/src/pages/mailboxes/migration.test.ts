import { describe, expect, it } from 'vitest';
import { ApiError } from '@/api/errors';
import type { MigrationJob } from '@/api/mailMigration';
import { t } from '@/i18n';
import {
  failureMessage,
  findActiveJob,
  launchBlock,
  migrationErrorMessage,
  progressPercent,
  progressRatio,
  shouldPoll,
} from './migration';
import { jobOf, META } from './migrationFixtures';

function withProgress(patch: Partial<MigrationJob['progress']>, job: Partial<MigrationJob> = {}) {
  const base = jobOf();
  return jobOf({ ...job, progress: { ...base.progress, ...patch } });
}

describe('avance de la migracion', () => {
  it('sin ningun dato no hay porcentaje', () => {
    expect(progressRatio(jobOf())).toBeNull();
    expect(progressPercent(jobOf())).toBeNull();
  });

  it('mientras no hay mensajes contados avanza por carpetas', () => {
    const job = withProgress({ folders_total: 8, folders_done: 2 });
    expect(progressRatio(job)).toBe(0.25);
    expect(progressPercent(job)).toBe(25);
  });

  it('con mensajes cuenta copiados, omitidos y fallidos sobre el total', () => {
    const job = withProgress({
      folders_total: 8,
      folders_done: 1,
      messages_total: 200,
      messages_copied: 60,
      messages_skipped: 30,
      messages_failed: 10,
    });
    expect(progressPercent(job)).toBe(50);
  });

  it('nunca pasa del 100 % ni baja del 0 % aunque el ejecutor informe de mas', () => {
    expect(
      progressRatio(withProgress({ messages_total: 10, messages_copied: 15, messages_failed: 5 })),
    ).toBe(1);
    expect(progressRatio(withProgress({ messages_total: 10, messages_copied: -3 }))).toBe(0);
  });

  it('un trabajo completado esta al 100 % aunque no informara totales', () => {
    expect(progressPercent(jobOf({ status: 'succeeded' }))).toBe(100);
  });

  it('un valor no finito no produce un porcentaje', () => {
    expect(
      progressRatio(withProgress({ messages_total: 10, messages_copied: Number.NaN })),
    ).toBeNull();
  });
});

describe('sondeo', () => {
  it('sondea mientras haya un trabajo pendiente o en curso', () => {
    expect(shouldPoll([jobOf({ status: 'pending' })], false)).toBe(true);
    expect(shouldPoll([jobOf({ status: 'running' })], false)).toBe(true);
    expect(shouldPoll([jobOf({ status: 'succeeded' }), jobOf({ status: 'running' })], false)).toBe(
      true,
    );
  });

  it('se detiene cuando todos terminaron', () => {
    for (const status of ['succeeded', 'failed', 'cancelled'] as const) {
      expect(shouldPoll([jobOf({ status })], false), status).toBe(false);
    }
    expect(shouldPoll([], false)).toBe(false);
  });

  it('no sondea sin datos ni despues de un fallo de lectura', () => {
    expect(shouldPoll(null, false)).toBe(false);
    expect(shouldPoll([jobOf({ status: 'running' })], true)).toBe(false);
  });
});

describe('trabajo activo y bloqueos', () => {
  it('encuentra el trabajo activo', () => {
    const running = jobOf({ id: 'b', status: 'running' });
    expect(findActiveJob([jobOf({ id: 'a', status: 'failed' }), running])).toBe(running);
    expect(findActiveJob([jobOf({ status: 'cancelled' })])).toBeNull();
  });

  it('se puede lanzar cuando esta configurado, sin trabajo activo y bajo el limite', () => {
    expect(launchBlock(META, [])).toBeNull();
    expect(launchBlock(META, [jobOf({ status: 'failed' })])).toBeNull();
  });

  it('explica por que no se puede lanzar', () => {
    expect(launchBlock({ ...META, configured: false }, [])).toBe('notConfigured');
    expect(launchBlock(META, [jobOf({ status: 'pending' })])).toBe('jobActive');
    expect(launchBlock({ ...META, active_jobs: 2 }, [])).toBe('tenantLimit');
  });

  it('con la lista sin leer no decide sobre el trabajo activo', () => {
    expect(launchBlock(META, null)).toBeNull();
  });
});

describe('textos de error', () => {
  it('cada codigo de last_error tiene su texto y uno desconocido, el generico', () => {
    const codes = [
      'source_auth_failed',
      'source_unreachable',
      'source_blocked_address',
      'source_tls_failed',
      'destination_failed',
      'quota_exceeded',
      'timeout',
      'virus_found',
      'imapsync_failed',
      'runner_lost',
      'cancelled',
    ];
    const texts = codes.map(failureMessage);
    expect(new Set(texts).size).toBe(codes.length);
    expect(texts).not.toContain(t('migration.failure.unknown'));
    expect(failureMessage('otro_codigo')).toBe(t('migration.failure.unknown'));
  });

  it('los codigos del API de administracion se traducen', () => {
    expect(
      migrationErrorMessage(new ApiError(409, { code: 'JOB_ALREADY_ACTIVE', message: 'x' })),
    ).toBe(t('migration.error.JOB_ALREADY_ACTIVE'));
    expect(
      migrationErrorMessage(new ApiError(422, { code: 'SOURCE_HOST_NOT_ALLOWED', message: 'x' })),
    ).toBe(t('migration.error.SOURCE_HOST_NOT_ALLOWED'));
    expect(migrationErrorMessage(new ApiError(503, { code: 'NOT_CONFIGURED', message: 'x' }))).toBe(
      t('migration.error.NOT_CONFIGURED'),
    );
  });

  it('un error de validacion nombra el campo que fallo', () => {
    const err = new ApiError(
      422,
      { code: 'VALIDATION_ERROR', message: 'invalido' },
      { error: { code: 'VALIDATION_ERROR', details: { field: 'source_username' } } },
    );
    expect(migrationErrorMessage(err)).toBe(t('migration.error.field.source_username'));
    expect(
      migrationErrorMessage(new ApiError(422, { code: 'VALIDATION_ERROR', message: 'x' })),
    ).toBe(t('migration.error.VALIDATION_ERROR'));
  });

  it('nunca muestra el mensaje del servidor', () => {
    const leaky = new ApiError(500, { code: 'ALGO_NUEVO', message: 'clave-secreta-123' });
    expect(migrationErrorMessage(leaky)).not.toContain('clave-secreta-123');
    expect(migrationErrorMessage(new Error('clave-secreta-123'))).not.toContain(
      'clave-secreta-123',
    );
  });
});
