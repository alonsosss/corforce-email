import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { t } from '@/i18n';
import { setAccessToken } from './client';
import { ApiError, ERROR_CODES, errorDetail } from './errors';
import { errorMessage } from './messages';
import {
  FIELD_RULES,
  schedulerApi,
  type CreateJobRequest,
  type JobExecution,
  type ScheduledTask,
  type SchedulerJob,
  type SchedulerMeta,
} from './scheduler';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface RecordedCall {
  url: string;
  method: string;
  body: string | undefined;
}

function mockFetch(handler: (call: RecordedCall) => Response): RecordedCall[] {
  const calls: RecordedCall[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const call: RecordedCall = {
        url: String(input),
        method: init?.method ?? 'GET',
        body: typeof init?.body === 'string' ? init.body : undefined,
      };
      calls.push(call);
      return handler(call);
    }),
  );
  return calls;
}

// Forma exacta de jobDTO y executionDTO (dto_test.go la fija en el servicio).
const JOB: SchedulerJob = {
  id: 'job-1',
  tenant_id: 'tenant-1',
  name: 'Limpieza',
  code: 'cleanup',
  description: null,
  job_type: 'cron',
  cron_expression: '0 * * * *',
  timezone: 'America/Lima',
  interval_minutes: null,
  handler: 'cleanup',
  payload: '{"k":"v"}',
  is_active: true,
  max_retries: 3,
  timeout_seconds: 60,
  version: 4,
  created_at: '2026-09-13T10:00:00Z',
  updated_at: '2026-09-13T10:00:00Z',
  next_run_at: '2026-09-13T11:00:00Z',
  last_run_at: '2026-09-13T09:00:00Z',
  last_execution: {
    id: 'exec-1',
    status: 'failed',
    completed_at: '2026-09-13T10:00:00Z',
    failure_reason: 'timeout',
  },
  already_run: false,
};

const EXECUTION: JobExecution = {
  id: 'exec-1',
  job_id: 'job-1',
  tenant_id: 'tenant-1',
  status: 'completed',
  started_at: '2026-09-13T10:00:00Z',
  completed_at: '2026-09-13T10:00:01Z',
  duration_ms: 1500,
  result: 'ok',
  error_message: null,
  retry_count: 1,
  created_at: '2026-09-13T10:00:00Z',
  deadline_at: null,
  next_attempt_at: null,
  retry_of: null,
  failure_reason: null,
};

const CREATE: CreateJobRequest = {
  name: 'Limpieza',
  code: 'cleanup',
  description: null,
  job_type: 'cron',
  cron_expression: '0 * * * *',
  timezone: 'America/Lima',
  interval_minutes: null,
  handler: 'cleanup',
  payload: null,
  max_retries: 3,
  timeout_seconds: 60,
};

describe('cliente del scheduler', () => {
  beforeEach(() => setAccessToken(null));
  afterEach(() => vi.unstubAllGlobals());

  it('pagina los trabajos con is_active, page y per_page y manda el meta del servicio', async () => {
    const calls = mockFetch(() =>
      jsonResponse(200, {
        data: [JOB],
        meta: { page: 2, per_page: 20, total: 21, total_pages: 2 },
      }),
    );
    const page = await schedulerApi.listJobs({ is_active: false, page: 2, per_page: 20 });
    await schedulerApi.listJobs({ page: 1, per_page: 20 });
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'GET /api/v1/scheduler/jobs?is_active=false&page=2&per_page=20',
      'GET /api/v1/scheduler/jobs?page=1&per_page=20',
    ]);
    expect(page).toMatchObject({ page: 2, perPage: 20, total: 21, totalPages: 2 });
    expect(page.items[0]).toEqual(JOB);
  });

  it('sin trabajos el meta omite total y total_pages (omitempty): la pagina queda en 0', async () => {
    mockFetch(() => jsonResponse(200, { data: [], meta: { page: 1, per_page: 20 } }));
    expect(await schedulerApi.listJobs({ page: 1, per_page: 20 })).toEqual({
      items: [],
      page: 1,
      perPage: 20,
      total: 0,
      totalPages: 0,
    });
  });

  it('crea con los campos exactos de createJobReq y devuelve el trabajo', async () => {
    const calls = mockFetch(() => jsonResponse(201, { data: JOB }));
    const { data } = await schedulerApi.createJob(CREATE);
    expect(`${calls[0]?.method} ${calls[0]?.url}`).toBe('POST /api/v1/scheduler/jobs');
    expect(Object.keys(JSON.parse(calls[0]?.body ?? '{}')).sort()).toEqual([
      'code',
      'cron_expression',
      'description',
      'handler',
      'interval_minutes',
      'job_type',
      'max_retries',
      'name',
      'payload',
      'timeout_seconds',
      'timezone',
    ]);
    expect(data).toEqual(JOB);
  });

  it('edita con PUT sobre el trabajo, codifica el id y lleva la version leida', async () => {
    const calls = mockFetch(() => jsonResponse(200, { data: JOB }));
    const { code: _code, ...update } = CREATE;
    await schedulerApi.updateJob('a/b', { ...update, version: JOB.version });
    expect(`${calls[0]?.method} ${calls[0]?.url}`).toBe('PUT /api/v1/scheduler/jobs/a%2Fb');
    const body = JSON.parse(calls[0]?.body ?? '{}') as Record<string, unknown>;
    expect(body).not.toHaveProperty('code');
    expect(body.version).toBe(JOB.version);
  });

  it('lee el historial con page y per_page y toma la pagina del meta del servicio', async () => {
    const calls = mockFetch(() =>
      jsonResponse(200, {
        data: [EXECUTION],
        meta: { page: 2, per_page: 20, total: 21, total_pages: 2 },
      }),
    );
    const page = await schedulerApi.history('job-1', { page: 2, per_page: 20 });
    expect(calls[0]?.url).toBe('/api/v1/scheduler/jobs/job-1/history?page=2&per_page=20');
    expect(page).toMatchObject({ page: 2, perPage: 20, total: 21, totalPages: 2 });
    expect(page.items[0]?.duration_ms).toBe(1500);
  });

  it('activar, desactivar y cancelar son POST a su ruta y un 204 no es un error', async () => {
    const calls = mockFetch(() => new Response(null, { status: 204 }));
    await schedulerApi.enableJob('job-1');
    await schedulerApi.disableJob('job-1');
    await schedulerApi.cancelExecution('exec-1');
    await schedulerApi.cancelTask('task-1');
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'POST /api/v1/scheduler/jobs/job-1/enable',
      'POST /api/v1/scheduler/jobs/job-1/disable',
      'POST /api/v1/scheduler/executions/exec-1/cancel',
      'POST /api/v1/scheduler/tasks/task-1/cancel',
    ]);
    expect(calls.every((c) => c.body === undefined)).toBe(true);
  });

  it('lanzar y reintentar devuelven la ejecucion creada', async () => {
    const calls = mockFetch(() => jsonResponse(201, { data: EXECUTION }));
    expect((await schedulerApi.runJob('job-1')).data).toEqual(EXECUTION);
    expect((await schedulerApi.retryExecution('exec-1')).data).toEqual(EXECUTION);
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'POST /api/v1/scheduler/jobs/job-1/run',
      'POST /api/v1/scheduler/executions/exec-1/retry',
    ]);
  });

  it('lee la meta, el catalogo de manejadores y las tareas pendientes', async () => {
    const meta: SchedulerMeta = {
      timezone: { default: 'UTC', format: 'iana', pattern: '^x$', max_length: 64 },
      cron: { descriptors: ['@daily'], min_every_seconds: 60, max_length: 100 },
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
    const calls = mockFetch((call) =>
      call.url.endsWith('/meta') ? jsonResponse(200, { data: meta }) : jsonResponse(200, {}),
    );
    expect(await schedulerApi.meta()).toEqual(meta);
    expect(await schedulerApi.handlers()).toEqual([]);
    expect(await schedulerApi.listPendingTasks({ page: 1, per_page: 20 })).toEqual({
      items: [],
      page: 1,
      perPage: 20,
      total: 0,
      totalPages: 0,
      pendingWindowSeconds: null,
    });
    expect(calls.map((c) => c.url)).toEqual([
      '/api/v1/scheduler/meta',
      '/api/v1/scheduler/handlers',
      '/api/v1/scheduler/tasks?page=1&per_page=20',
    ]);
  });

  it('pagina las tareas pendientes y lee la ventana de la meta de la propia respuesta', async () => {
    const task: ScheduledTask = {
      id: 'task-1',
      tenant_id: 'tenant-1',
      name: 'Aviso',
      description: null,
      trigger_at: '2026-09-14T08:00:00Z',
      handler: 'reports.daily',
      payload: null,
      status: 'scheduled',
      executed_at: null,
    failure_reason: null,
      created_at: '2026-09-13T10:00:00Z',
    };
    const calls = mockFetch(() =>
      jsonResponse(200, {
        data: [task],
        meta: { page: 2, per_page: 20, total: 21, total_pages: 2, pending_window_seconds: 86400 },
      }),
    );
    const page = await schedulerApi.listPendingTasks({ page: 2, per_page: 20 });
    expect(calls[0]?.url).toBe('/api/v1/scheduler/tasks?page=2&per_page=20');
    expect(page).toEqual({
      items: [task],
      page: 2,
      perPage: 20,
      total: 21,
      totalPages: 2,
      pendingWindowSeconds: 86400,
    });

    for (const window of [0, -60, '86400', null]) {
      mockFetch(() =>
        jsonResponse(200, {
          data: [],
          meta: { page: 1, per_page: 20, pending_window_seconds: window },
        }),
      );
      expect(
        (await schedulerApi.listPendingTasks({ page: 1, per_page: 20 })).pendingWindowSeconds,
      ).toBe(null);
    }
  });

  it('una zona invalida llega como 422 INVALID_TIMEZONE con el campo y la regla en error.details', async () => {
    const message = 'invalid time zone: "Mars/Olympus" is not in the time zone database';
    mockFetch(() =>
      jsonResponse(422, {
        error: {
          code: 'INVALID_TIMEZONE',
          message,
          details: { field: 'timezone', rule: FIELD_RULES.notAllowed },
        },
      }),
    );
    const err = await schedulerApi.createJob(CREATE).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: ERROR_CODES.INVALID_TIMEZONE, message });
    expect(errorDetail(err, 'field')).toBe('timezone');
    expect(errorDetail(err, 'rule')).toBe('not_allowed');
  });

  it('el codigo repetido es un 409 CONFLICT que nombra el campo code con la regla duplicate', async () => {
    mockFetch(() =>
      jsonResponse(409, {
        error: {
          code: 'CONFLICT',
          message: 'job already exists',
          details: { field: 'code', rule: FIELD_RULES.duplicate },
        },
      }),
    );
    const err = await schedulerApi.createJob(CREATE).catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 409, code: ERROR_CODES.CONFLICT });
    expect(errorDetail(err, 'field')).toBe('code');
    expect(errorDetail(err, 'rule')).toBe('duplicate');
  });

  it('reactivar un one_time ya despachado es un 409 JOB_ALREADY_RUN con texto propio', async () => {
    mockFetch(() =>
      jsonResponse(409, {
        error: {
          code: 'JOB_ALREADY_RUN',
          message: 'a one_time job that already ran cannot be re-enabled',
        },
      }),
    );
    const err = await schedulerApi.enableJob('job-1').catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 409, code: ERROR_CODES.JOB_ALREADY_RUN });
    expect(errorMessage(err)).toBe(t('error.code.JOB_ALREADY_RUN'));
  });

  it('una version que ya no es la guardada es 409 VERSION_CONFLICT y sin version 428, con texto propio', async () => {
    const { code: _code, ...update } = CREATE;
    for (const [status, code] of [
      [409, ERROR_CODES.VERSION_CONFLICT],
      [428, ERROR_CODES.VERSION_REQUIRED],
    ] as const) {
      mockFetch(() => jsonResponse(status, { error: { code, message: 'from the service' } }));
      const err = await schedulerApi
        .updateJob('job-1', { ...update, version: 1 })
        .catch((e: unknown) => e);
      expect(err).toMatchObject({ status, code });
      expect(errorMessage(err)).toBe(t(`error.code.${code}`));
    }
  });
});
