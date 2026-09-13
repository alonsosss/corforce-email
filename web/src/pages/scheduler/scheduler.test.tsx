import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent, { type UserEvent } from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { ApiError, ERROR_CODES } from '@/api/errors';
import {
  schedulerApi,
  schedulerHandlers,
  schedulerMeta,
  type JobExecution,
  type ScheduledTask,
  type SchedulerHandler,
} from '@/api/scheduler';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';
import JobDetailPage from './JobDetailPage';
import { JobForm } from './JobForm';
import SchedulerPage from './SchedulerPage';
import {
  jobFixture,
  platformHandlerFixture,
  schedulerMetaFixture as META,
  tenantHandlerFixture,
} from './metaFixture';
import { formatDurationMs, formatSeconds } from './schedulerFormat';

type Triple = readonly [string, string, string];

const ALL: Triple[] = [
  ...Object.values(PERMISSIONS.schedulerJobs),
  ...Object.values(PERMISSIONS.schedulerExecutions),
  ...Object.values(PERMISSIONS.schedulerTasks),
];

function grant(...triples: Triple[]) {
  const permissions: PermissionTriple[] = triples.map(([module, resource, action]) => ({
    module,
    resource,
    action,
  }));
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.scheduler],
    permissions,
  });
}

function mockCatalogs(
  handlers: SchedulerHandler[] = [tenantHandlerFixture, platformHandlerFixture],
) {
  vi.spyOn(schedulerMeta, 'get').mockResolvedValue(META);
  return vi.spyOn(schedulerHandlers, 'get').mockResolvedValue(handlers);
}

function renderWith(ui: JSX.Element) {
  return render(
    <MemoryRouter>
      <ToastProvider>{ui}</ToastProvider>
    </MemoryRouter>,
  );
}

const escape = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const field = (label: string) => screen.getByLabelText(new RegExp(`^${escape(label)}`));
const submitButton = () => screen.getByRole('button', { name: t('common.create') });

async function fillCronForm(user: UserEvent, cron: string) {
  await user.type(
    await screen.findByLabelText(new RegExp(`^${escape(t('common.name'))}`)),
    'Resumen diario',
  );
  await user.type(field(t('scheduler.detail.code')), 'daily-digest');
  await user.selectOptions(field(t('scheduler.column.handler')), tenantHandlerFixture.name);
  await user.selectOptions(field(t('scheduler.column.type')), 'cron');
  await user.type(field(t('scheduler.form.cron')), cron);
}

afterEach(() => {
  useAccessStore.getState().reset();
  vi.restoreAllMocks();
});

describe('formulario de un trabajo', () => {
  it('valida con las reglas de la meta y pinta cada error junto a su campo sin llamar al API', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    const create = vi.spyOn(schedulerApi, 'createJob');
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '@yearly');
    await user.click(submitButton());
    const descriptor = t('scheduler.validation.descriptor', {
      list: META.cron.descriptors.join(', '),
    });
    expect(await screen.findByText(descriptor)).toHaveAttribute('id', 'job-cron-error');
    expect(field(t('scheduler.form.cron'))).toHaveAttribute('aria-invalid', 'true');
    expect(field(t('scheduler.form.cron'))).toHaveAttribute('aria-describedby', 'job-cron-error');

    await user.clear(field(t('scheduler.form.cron')));
    await user.type(field(t('scheduler.form.cron')), '@every 30s');
    await user.clear(field(t('scheduler.form.timezone')));
    await user.type(field(t('scheduler.form.timezone')), '+05:00');
    await user.click(submitButton());
    expect(
      await screen.findByText(
        t('scheduler.validation.everyMin', { min: formatSeconds(META.cron.min_every_seconds) }),
      ),
    ).toHaveAttribute('id', 'job-cron-error');
    expect(screen.getByText(t('scheduler.validation.timezoneFormat'))).toHaveAttribute(
      'id',
      'job-timezone-error',
    );
    expect(create).not.toHaveBeenCalled();
  });

  it('el 422 del servidor va al campo que lo causa, una sola vez, y se retira al corregirlo', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    const detail = 'expected exactly 5 fields, found 3: [0 7 *]';
    const create = vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(
      new ApiError(422, {
        code: ERROR_CODES.VALIDATION_ERROR,
        message: `invalid cron expression: ${detail}`,
      }),
    );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 *');
    await user.click(submitButton());

    const message = t('scheduler.form.cronRejected', { detail });
    expect(await screen.findByText(message)).toHaveAttribute('id', 'job-cron-error');
    expect(screen.getAllByText(message)).toHaveLength(1);
    expect(create).toHaveBeenCalledWith({
      name: 'Resumen diario',
      code: 'daily-digest',
      description: null,
      job_type: 'cron',
      cron_expression: '0 7 *',
      timezone: META.timezone.default,
      interval_minutes: null,
      handler: tenantHandlerFixture.name,
      payload: null,
      max_retries: 0,
      timeout_seconds: 0,
    });

    await user.type(field(t('scheduler.form.cron')), ' *');
    expect(screen.queryByText(message)).toBeNull();
  });

  it('una zona que el servidor no carga (INVALID_TIMEZONE) se pinta junto a la zona', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(
      new ApiError(422, {
        code: ERROR_CODES.INVALID_TIMEZONE,
        message: 'invalid time zone: "Mars/Olympus" is not in the time zone database',
      }),
    );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    await user.clear(field(t('scheduler.form.timezone')));
    await user.type(field(t('scheduler.form.timezone')), 'Mars/Olympus');
    await user.click(submitButton());

    expect(
      await screen.findByText(
        t('scheduler.form.timezoneRejected', {
          detail: '"Mars/Olympus" is not in the time zone database',
        }),
      ),
    ).toHaveAttribute('id', 'job-timezone-error');
    expect(field(t('scheduler.form.timezone'))).toHaveAttribute('aria-invalid', 'true');
  });

  it('sin manejadores de empresa en el catalogo lo explica y no deja enviar', async () => {
    mockCatalogs([platformHandlerFixture]);
    const create = vi.spyOn(schedulerApi, 'createJob');
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    expect(await screen.findByText(t('scheduler.catalogEmpty.title'))).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();
    expect(field(t('scheduler.column.handler'))).toBeDisabled();
    expect(
      within(field(t('scheduler.column.handler'))).queryByRole('option', {
        name: new RegExp(escape(platformHandlerFixture.name)),
      }),
    ).toBeNull();
    expect(create).not.toHaveBeenCalled();
  });

  it('al editar conserva visible un manejador que salio del catalogo y envia sin code', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    const job = jobFixture({ handler: 'retirado.handler' });
    const update = vi.spyOn(schedulerApi, 'updateJob').mockResolvedValue({ data: job });
    const onSaved = vi.fn();
    renderWith(<JobForm job={job} onClose={vi.fn()} onSaved={onSaved} />);

    const handler = await screen.findByLabelText(
      new RegExp(`^${escape(t('scheduler.column.handler'))}`),
    );
    expect(handler).toHaveValue('retirado.handler');
    expect(
      screen.getByRole('option', {
        name: t('scheduler.handler.optionOutOfCatalog', { name: 'retirado.handler' }),
      }),
    ).toBeInTheDocument();
    expect(field(t('scheduler.detail.code'))).toBeDisabled();

    await user.click(screen.getByRole('button', { name: t('common.save') }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(job));
    expect(update.mock.calls[0]?.[0]).toBe(job.id);
    expect(update.mock.calls[0]?.[1]).not.toHaveProperty('code');
    expect(update.mock.calls[0]?.[1]).toMatchObject({
      timezone: job.timezone,
      handler: job.handler,
    });
  });
});

describe('listado de trabajos y permisos', () => {
  const TASK: ScheduledTask = {
    id: 'task-1',
    tenant_id: 't',
    name: 'Aviso de renovacion',
    description: null,
    trigger_at: '2026-09-14T08:00:00Z',
    handler: tenantHandlerFixture.name,
    payload: null,
    status: 'scheduled',
    executed_at: null,
    created_at: '2026-09-13T10:00:00Z',
  };

  it('sin permisos de lectura del scheduler lo dice y no pide nada', () => {
    grant();
    const jobs = vi.spyOn(schedulerApi, 'listJobs');
    const tasks = vi.spyOn(schedulerApi, 'listPendingTasks');
    renderWith(<SchedulerPage />);
    expect(screen.getByText(t('common.missingPermission'))).toBeInTheDocument();
    expect(jobs).not.toHaveBeenCalled();
    expect(tasks).not.toHaveBeenCalled();
  });

  it('con jobs/read lista la programacion sin ofrecer crear ni pedir el catalogo', async () => {
    grant(PERMISSIONS.schedulerJobs.read);
    const handlers = mockCatalogs();
    const tasks = vi.spyOn(schedulerApi, 'listPendingTasks');
    vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue([
      jobFixture(),
      jobFixture({
        id: 'p-1',
        tenant_id: null,
        name: 'Limpieza',
        code: 'cleanup',
        job_type: 'interval',
        cron_expression: null,
        interval_minutes: 30,
        is_active: false,
        handler: platformHandlerFixture.name,
      }),
    ]);
    renderWith(<SchedulerPage />);

    const table = await screen.findByRole('table');
    await within(table).findByText('Resumen diario');
    expect(within(table).getByText('0 7 * * *')).toBeInTheDocument();
    expect(within(table).getByText('America/Lima')).toBeInTheDocument();
    expect(
      within(table).getByText(t('scheduler.schedule.interval', { n: 30 })),
    ).toBeInTheDocument();
    expect(within(table).getByText(t('scheduler.platform'))).toBeInTheDocument();
    expect(within(table).getByText(t('common.inactive'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('scheduler.new') })).toBeNull();
    expect(screen.queryByRole('tab')).toBeNull();
    expect(handlers).not.toHaveBeenCalled();
    expect(tasks).not.toHaveBeenCalled();
  });

  it('el filtro de estado viaja como is_active', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read);
    const list = vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue([]);
    renderWith(<SchedulerPage />);

    expect(await screen.findByText(t('scheduler.empty'))).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith({ is_active: undefined });
    await user.selectOptions(screen.getByLabelText(t('common.status')), 'false');
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ is_active: false }));
  });

  it('con jobs/create ofrece crear y avisa si el catalogo esta vacio', async () => {
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerJobs.create);
    mockCatalogs([]);
    vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue([]);
    renderWith(<SchedulerPage />);

    expect(await screen.findByText(t('scheduler.catalogEmpty.title'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('scheduler.new') })).toBeInTheDocument();
  });

  it('las tareas puntuales exigen tasks/read, y cancelarlas tasks/cancel', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerTasks.read);
    vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue([]);
    const tasks = vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue([TASK]);
    renderWith(<SchedulerPage />);

    expect(tasks).not.toHaveBeenCalled();
    await user.click(screen.getByRole('tab', { name: t('scheduler.tab.tasks') }));
    expect(await screen.findByText(TASK.name)).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.taskStatus.scheduled'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('scheduler.tasks.cancelLabel', { name: TASK.name }) }),
    ).toBeNull();
  });

  it('con tasks/cancel se cancela una tarea tras confirmarlo', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerTasks.read, PERMISSIONS.schedulerTasks.cancel);
    const jobs = vi.spyOn(schedulerApi, 'listJobs');
    vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue([TASK]);
    const cancel = vi.spyOn(schedulerApi, 'cancelTask').mockResolvedValue({ data: null });
    renderWith(<SchedulerPage />);

    await user.click(
      await screen.findByRole('button', {
        name: t('scheduler.tasks.cancelLabel', { name: TASK.name }),
      }),
    );
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: t('scheduler.tasks.cancel') }),
    );
    await waitFor(() => expect(cancel).toHaveBeenCalledWith(TASK.id));
    expect(jobs).not.toHaveBeenCalled();
  });
});

describe('detalle de un trabajo', () => {
  const JOB = jobFixture();

  function execution(extra: Partial<JobExecution>): JobExecution {
    return {
      id: 'e',
      job_id: JOB.id,
      tenant_id: JOB.tenant_id,
      status: 'completed',
      started_at: '2026-09-13T10:00:00Z',
      completed_at: '2026-09-13T10:00:02Z',
      duration_ms: 250,
      result: null,
      error_message: null,
      retry_count: 0,
      created_at: '2026-09-13T10:00:00Z',
      deadline_at: null,
      next_attempt_at: null,
      retry_of: null,
      failure_reason: null,
      ...extra,
    };
  }

  const RUNNING = execution({
    id: 'e-running',
    status: 'running',
    completed_at: null,
    duration_ms: null,
    deadline_at: '2026-09-13T10:10:00Z',
    created_at: '2026-09-13T11:00:00Z',
  });
  const FAILED = execution({
    id: 'e-failed',
    status: 'failed',
    duration_ms: 1500,
    retry_count: 1,
    failure_reason: 'timeout',
    error_message: 'deadline exceeded',
    created_at: '2026-09-13T09:00:00Z',
  });
  const COMPLETED = execution({ id: 'e-done', created_at: '2026-09-13T08:00:00Z' });

  function mockHistory(items: JobExecution[]) {
    return vi.spyOn(schedulerApi, 'history').mockResolvedValue({
      items,
      page: 1,
      perPage: 20,
      total: items.length,
      totalPages: 1,
    });
  }

  function renderDetail() {
    return render(
      <MemoryRouter initialEntries={[paths.schedulerJob(JOB.id)]}>
        <ToastProvider>
          <Routes>
            <Route path={`${paths.scheduler}/:id`} element={<JobDetailPage />} />
          </Routes>
        </ToastProvider>
      </MemoryRouter>,
    );
  }

  const label = (
    key: 'scheduler.executions.cancelLabel' | 'scheduler.executions.retryLabel',
    e: JobExecution,
  ) => t(key, { n: e.retry_count + 1, date: formatDateTime(e.created_at) });

  it('con solo jobs/read no ofrece acciones ni pide el historial', async () => {
    grant(PERMISSIONS.schedulerJobs.read);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({ data: JOB });
    const history = mockHistory([]);
    renderDetail();

    expect(await screen.findByRole('heading', { name: JOB.name })).toBeInTheDocument();
    for (const name of [t('common.edit'), t('common.deactivate'), t('scheduler.run')]) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
    expect(screen.getByText(t('common.missingPermission'))).toBeInTheDocument();
    expect(history).not.toHaveBeenCalled();
  });

  it('muestra estado, intento, duracion y motivo, y cada accion con su permiso', async () => {
    const user = userEvent.setup();
    grant(...ALL);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({ data: JOB });
    const history = mockHistory([RUNNING, FAILED, COMPLETED]);
    const run = vi
      .spyOn(schedulerApi, 'runJob')
      .mockResolvedValue({ data: execution({ id: 'e-new', status: 'running' }) });
    renderDetail();

    const table = await screen.findByRole('table');
    await within(table).findByText(t('scheduler.executionStatus.failed'));
    expect(within(table).getByText(t('scheduler.executionStatus.running'))).toBeInTheDocument();
    expect(within(table).getByText(t('scheduler.failureReason.timeout'))).toBeInTheDocument();
    expect(within(table).getByText('deadline exceeded')).toBeInTheDocument();
    expect(within(table).getByText(formatDurationMs(1500))).toBeInTheDocument();
    expect(within(table).getByText('2')).toBeInTheDocument();
    expect(
      within(table).getByRole('button', {
        name: label('scheduler.executions.cancelLabel', RUNNING),
      }),
    ).toBeInTheDocument();
    expect(
      within(table).getByRole('button', { name: label('scheduler.executions.retryLabel', FAILED) }),
    ).toBeInTheDocument();
    expect(within(table).getAllByRole('button')).toHaveLength(2);
    expect(screen.getByRole('button', { name: t('common.edit') })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('common.deactivate') })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: t('scheduler.run') }));
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: t('scheduler.run') }),
    );
    await waitFor(() => expect(run).toHaveBeenCalledWith(JOB.id));
    await waitFor(() => expect(history).toHaveBeenCalledTimes(2));
  });

  it('sin executions/cancel ni retry no hay acciones por ejecucion', async () => {
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerExecutions.read);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({ data: JOB });
    mockHistory([RUNNING, FAILED]);
    renderDetail();

    const table = await screen.findByRole('table');
    await within(table).findByText(t('scheduler.executionStatus.failed'));
    expect(within(table).queryAllByRole('button')).toHaveLength(0);
  });

  it('un trabajo de plataforma se lee sin acciones aunque haya permiso', async () => {
    grant(...ALL);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({
      data: jobFixture({ tenant_id: null, handler: platformHandlerFixture.name }),
    });
    mockHistory([FAILED]);
    renderDetail();

    expect(await screen.findByText(t('scheduler.platformHint'))).toBeInTheDocument();
    for (const name of [t('common.edit'), t('common.deactivate'), t('scheduler.run')]) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
    const table = await screen.findByRole('table');
    await within(table).findByText(t('scheduler.executionStatus.failed'));
    expect(within(table).queryAllByRole('button')).toHaveLength(0);
    expect(screen.queryByText(t('scheduler.handler.outOfCatalog'))).toBeNull();
  });

  it('avisa cuando el manejador del trabajo ya no esta en el catalogo', async () => {
    grant(PERMISSIONS.schedulerJobs.read);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({
      data: jobFixture({ handler: 'retirado.handler' }),
    });
    renderDetail();

    expect(await screen.findByText(t('scheduler.handler.outOfCatalog'))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.handler.outOfCatalogHint'))).toBeInTheDocument();
  });
});
