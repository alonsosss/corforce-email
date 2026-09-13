import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './client';
import { ApiError, ERROR_CODES } from './errors';
import {
  schedulerApi,
  type CreateJobRequest,
  type JobExecution,
  type SchedulerJob,
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
  created_at: '2026-09-13T10:00:00Z',
  updated_at: '2026-09-13T10:00:00Z',
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

  it('lista los trabajos con el filtro is_active y sin paginar; un slice nulo es []', async () => {
    const calls = mockFetch(() => jsonResponse(200, { data: null }));
    expect(await schedulerApi.listJobs({ is_active: false })).toEqual([]);
    await schedulerApi.listJobs({ is_active: true });
    await schedulerApi.listJobs({});
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'GET /api/v1/scheduler/jobs?is_active=false',
      'GET /api/v1/scheduler/jobs?is_active=true',
      'GET /api/v1/scheduler/jobs',
    ]);
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

  it('edita con PUT sobre el trabajo y codifica el id', async () => {
    const calls = mockFetch(() => jsonResponse(200, { data: JOB }));
    const { code: _code, ...update } = CREATE;
    await schedulerApi.updateJob('a/b', update);
    expect(`${calls[0]?.method} ${calls[0]?.url}`).toBe('PUT /api/v1/scheduler/jobs/a%2Fb');
    expect(JSON.parse(calls[0]?.body ?? '{}')).not.toHaveProperty('code');
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
    const meta = {
      timezone: { default: 'UTC', format: 'iana', pattern: '^x$', max_length: 64 },
      cron: { descriptors: ['@daily'], min_every_seconds: 60, max_length: 100 },
    };
    const calls = mockFetch((call) =>
      call.url.endsWith('/meta') ? jsonResponse(200, { data: meta }) : jsonResponse(200, {}),
    );
    expect(await schedulerApi.meta()).toEqual(meta);
    expect(await schedulerApi.handlers()).toEqual([]);
    expect(await schedulerApi.listPendingTasks()).toEqual([]);
    expect(calls.map((c) => c.url)).toEqual([
      '/api/v1/scheduler/meta',
      '/api/v1/scheduler/handlers',
      '/api/v1/scheduler/tasks',
    ]);
  });

  it('una zona invalida llega como 422 INVALID_TIMEZONE con el mensaje del servicio', async () => {
    const message = 'invalid time zone: "Mars/Olympus" is not in the time zone database';
    mockFetch(() => jsonResponse(422, { error: { code: 'INVALID_TIMEZONE', message } }));
    const err = await schedulerApi.createJob(CREATE).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: ERROR_CODES.INVALID_TIMEZONE, message });
  });
});
