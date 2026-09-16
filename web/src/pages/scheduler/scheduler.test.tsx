import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent, { type UserEvent } from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import {
  FIELD_RULES,
  schedulerApi,
  schedulerHandlers,
  schedulerMeta,
  type JobExecution,
  type LastExecution,
  type ScheduledTask,
  type SchedulerHandler,
  type SchedulerJob,
  type SchedulerMeta,
  type TasksPage,
} from '@/api/scheduler';
import type { Page } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { DEFAULT_PER_PAGE } from '@/hooks/usePagination';
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
  meta: SchedulerMeta = META,
) {
  vi.spyOn(schedulerMeta, 'get').mockResolvedValue(meta);
  return vi.spyOn(schedulerHandlers, 'get').mockResolvedValue(handlers);
}

function pageOf(jobs: SchedulerJob[], extra: Partial<Page<SchedulerJob>> = {}): Page<SchedulerJob> {
  return {
    items: jobs,
    page: 1,
    perPage: DEFAULT_PER_PAGE,
    total: jobs.length,
    totalPages: jobs.length > 0 ? 1 : 0,
    ...extra,
  };
}

/** Un error como lo arma client.ts: el cuerpo entero queda en body para errorDetail. */
function rejected(
  status: number,
  code: string,
  message: string,
  field?: string,
  rule?: string,
): ApiError {
  const details = field ? { field, ...(rule ? { rule } : {}) } : undefined;
  const error = { code, message, ...(details ? { details } : {}) };
  return new ApiError(status, error, { error });
}

/** Ventana que publica GET /scheduler/tasks en su meta (domain.PendingTasksWindow). */
const TASKS_WINDOW_SECONDS = 86_400;

function tasksPage(items: ScheduledTask[], extra: Partial<TasksPage> = {}): TasksPage {
  return {
    items,
    page: 1,
    perPage: DEFAULT_PER_PAGE,
    total: items.length,
    totalPages: items.length > 0 ? 1 : 0,
    pendingWindowSeconds: TASKS_WINDOW_SECONDS,
    ...extra,
  };
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

  it('el 422 va al campo de error.details.field en espanol por su regla, una sola vez, y se retira al corregirlo', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    const detail = 'invalid cron expression: expected exactly 5 fields, found 3: [0 7 *]';
    const create = vi
      .spyOn(schedulerApi, 'createJob')
      .mockRejectedValue(
        rejected(
          422,
          ERROR_CODES.VALIDATION_ERROR,
          detail,
          'cron_expression',
          FIELD_RULES.invalidFormat,
        ),
      );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 *');
    await user.click(submitButton());

    const message = t('scheduler.validation.cronFormat');
    expect(await screen.findByText(message)).toHaveAttribute('id', 'job-cron-error');
    expect(screen.getAllByText(message)).toHaveLength(1);
    expect(screen.queryByText(detail, { exact: false })).toBeNull();
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
    const detail = 'invalid time zone: "Mars/Olympus" is not in the time zone database';
    vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(
      rejected(422, ERROR_CODES.INVALID_TIMEZONE, detail, 'timezone', FIELD_RULES.notAllowed),
    );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    await user.clear(field(t('scheduler.form.timezone')));
    await user.type(field(t('scheduler.form.timezone')), 'Mars/Olympus');
    await user.click(submitButton());

    expect(await screen.findByText(t('scheduler.validation.timezoneUnknown'))).toHaveAttribute(
      'id',
      'job-timezone-error',
    );
    expect(field(t('scheduler.form.timezone'))).toHaveAttribute('aria-invalid', 'true');
  });

  it('una regla que esta version no conoce muestra el mensaje del servidor junto al campo', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    const detail = 'invalid time zone: "Mars/Olympus" is retired';
    vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(
      rejected(422, ERROR_CODES.INVALID_TIMEZONE, detail, 'timezone', 'retired_zone'),
    );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    await user.click(submitButton());
    expect(
      await screen.findByText(t('scheduler.form.timezoneRejected', { detail })),
    ).toHaveAttribute('id', 'job-timezone-error');
  });

  it('el plazo fuera de rango se explica con el maximo del manejador elegido', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(
      rejected(
        422,
        ERROR_CODES.VALIDATION_ERROR,
        'invalid job: timeout_seconds must be at most 600',
        'timeout_seconds',
        FIELD_RULES.outOfRange,
      ),
    );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    await user.click(submitButton());
    expect(
      await screen.findByText(
        t('scheduler.validation.timeoutHandlerMax', {
          value: formatSeconds(tenantHandlerFixture.max_timeout_seconds),
        }),
      ),
    ).toHaveAttribute('id', 'job-timeout-error');
  });

  it('el codigo repetido (409 con field code y regla duplicate) se pinta junto al codigo', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(
      rejected(409, ERROR_CODES.CONFLICT, 'job already exists', 'code', FIELD_RULES.duplicate),
    );
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    await user.click(submitButton());
    expect(await screen.findByText(t('scheduler.error.codeTaken'))).toHaveAttribute(
      'id',
      'job-code-error',
    );
  });

  it('un 422 sin campo queda como error general del formulario', async () => {
    const user = userEvent.setup();
    mockCatalogs();
    const err = rejected(422, ERROR_CODES.VALIDATION_ERROR, 'invalid job: unexpected');
    vi.spyOn(schedulerApi, 'createJob').mockRejectedValue(err);
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    await user.click(submitButton());
    expect(await screen.findByRole('alert')).toHaveTextContent(errorMessage(err));
    expect(field(t('scheduler.form.cron'))).not.toHaveAttribute('aria-invalid', 'true');
  });

  it('ofrece los tipos de trabajo que publica la meta, en su orden', async () => {
    mockCatalogs(undefined, { ...META, job_types: ['interval', 'cron'] });
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    const select = await screen.findByLabelText(
      new RegExp(`^${escape(t('scheduler.column.type'))}`),
    );
    const values = within(select)
      .getAllByRole('option')
      .map((option) => (option as HTMLOptionElement).value)
      .filter(Boolean);
    expect(values).toEqual(['interval', 'cron']);
  });

  it('aplica los topes de la meta y el plazo maximo del manejador sin llamar al API', async () => {
    const user = userEvent.setup();
    mockCatalogs(undefined, { ...META, limits: { ...META.limits, max_name_length: 5 } });
    const create = vi.spyOn(schedulerApi, 'createJob');
    renderWith(<JobForm job={null} onClose={vi.fn()} onSaved={vi.fn()} />);

    await fillCronForm(user, '0 7 * * *');
    const handlerMax = tenantHandlerFixture.max_timeout_seconds;
    expect(
      screen.getByText(t('scheduler.form.timeoutHintMax', { value: formatSeconds(handlerMax) })),
    ).toBeInTheDocument();
    await user.clear(field(t('scheduler.form.timeout')));
    await user.type(field(t('scheduler.form.timeout')), String(handlerMax + 1));
    await user.click(submitButton());

    expect(await screen.findByText(t('validation.maxLength', { n: 5 }))).toHaveAttribute(
      'id',
      'job-name-error',
    );
    expect(
      screen.getByText(
        t('scheduler.validation.timeoutHandlerMax', { value: formatSeconds(handlerMax) }),
      ),
    ).toHaveAttribute('id', 'job-timeout-error');
    expect(create).not.toHaveBeenCalled();
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
      version: job.version,
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
    failure_reason: null,
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
    const last: LastExecution = {
      id: 'e-1',
      status: 'failed',
      completed_at: '2026-09-13T10:00:05Z',
      failure_reason: 'timeout',
    };
    const active = jobFixture({ last_execution: last });
    vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue(
      pageOf([
        active,
        jobFixture({
          id: 'p-1',
          tenant_id: null,
          name: 'Limpieza',
          code: 'cleanup',
          job_type: 'interval',
          cron_expression: null,
          interval_minutes: 30,
          is_active: false,
          next_run_at: null,
          handler: platformHandlerFixture.name,
        }),
      ]),
    );
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
    expect(within(table).getByText(formatDateTime(active.next_run_at))).toBeInTheDocument();
    expect(within(table).getByText(t('scheduler.executionStatus.failed'))).toBeInTheDocument();
    expect(within(table).getByText(formatDateTime(last.completed_at))).toBeInTheDocument();
    expect(within(table).getByText(t('scheduler.nextRun.inactive'))).toBeInTheDocument();
    expect(within(table).getByText(t('scheduler.lastExecution.none'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('scheduler.new') })).toBeNull();
    expect(screen.queryByRole('tab')).toBeNull();
    expect(handlers).not.toHaveBeenCalled();
    expect(tasks).not.toHaveBeenCalled();
  });

  it('el filtro de estado viaja como is_active junto a la pagina', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read);
    const list = vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue(pageOf([]));
    renderWith(<SchedulerPage />);

    expect(await screen.findByText(t('scheduler.empty'))).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith({
      is_active: undefined,
      page: 1,
      per_page: DEFAULT_PER_PAGE,
    });
    await user.selectOptions(screen.getByLabelText(t('common.status')), 'false');
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith({
        is_active: false,
        page: 1,
        per_page: DEFAULT_PER_PAGE,
      }),
    );
  });

  it('pagina en el servidor y un cambio de filtro vuelve a la primera pagina', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read);
    const list = vi.spyOn(schedulerApi, 'listJobs').mockImplementation(async (query) => {
      const page = query.page ?? 1;
      return pageOf([jobFixture({ id: `job-${page}`, name: `Trabajo ${page}` })], {
        page,
        total: DEFAULT_PER_PAGE + 1,
        totalPages: 2,
      });
    });
    renderWith(<SchedulerPage />);

    expect(await screen.findByText('Trabajo 1')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('common.next') }));
    expect(await screen.findByText('Trabajo 2')).toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith({
      is_active: undefined,
      page: 2,
      per_page: DEFAULT_PER_PAGE,
    });

    await user.selectOptions(screen.getByLabelText(t('common.status')), 'true');
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith({
        is_active: true,
        page: 1,
        per_page: DEFAULT_PER_PAGE,
      }),
    );
  });

  it('con jobs/create ofrece crear y avisa si el catalogo esta vacio', async () => {
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerJobs.create);
    mockCatalogs([]);
    vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue(pageOf([]));
    renderWith(<SchedulerPage />);

    expect(await screen.findByText(t('scheduler.catalogEmpty.title'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('scheduler.new') })).toBeInTheDocument();
  });

  it('las tareas puntuales exigen tasks/read, y cancelarlas tasks/cancel', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerTasks.read);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'listJobs').mockResolvedValue(pageOf([]));
    const tasks = vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue(tasksPage([TASK]));
    renderWith(<SchedulerPage />);

    expect(tasks).not.toHaveBeenCalled();
    await user.click(screen.getByRole('tab', { name: t('scheduler.tab.tasks') }));
    expect(await screen.findByText(TASK.name)).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.taskStatus.scheduled'))).toBeInTheDocument();
    expect(tasks).toHaveBeenCalledWith({ page: 1, per_page: DEFAULT_PER_PAGE });
    expect(
      screen.queryByRole('button', { name: t('scheduler.tasks.cancelLabel', { name: TASK.name }) }),
    ).toBeNull();
  });

  it('con solo tasks/read la ventana sale de la respuesta del listado, sin pedir la meta', async () => {
    grant(PERMISSIONS.schedulerTasks.read);
    const meta = vi.spyOn(schedulerMeta, 'get');
    const jobs = vi.spyOn(schedulerApi, 'listJobs');
    vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue(
      tasksPage([], { pendingWindowSeconds: 7_200 }),
    );
    renderWith(<SchedulerPage />);

    const window = formatSeconds(7_200);
    expect(
      await screen.findByText(t('scheduler.tasks.descriptionWindow', { window })),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(t('scheduler.tasks.emptyWindow', { window })),
    ).toBeInTheDocument();
    expect(meta).not.toHaveBeenCalled();
    expect(jobs).not.toHaveBeenCalled();
  });

  it('si la respuesta no trae ventana, la pestana se describe sin plazo', async () => {
    grant(PERMISSIONS.schedulerTasks.read);
    vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue(
      tasksPage([], { pendingWindowSeconds: null }),
    );
    renderWith(<SchedulerPage />);
    expect(await screen.findByText(t('scheduler.tasks.description'))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.tasks.empty'))).toBeInTheDocument();
  });

  it('pagina las tareas en el servidor', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerTasks.read);
    const list = vi.spyOn(schedulerApi, 'listPendingTasks').mockImplementation(async (query) => {
      const page = query.page ?? 1;
      return tasksPage([{ ...TASK, id: `task-${page}`, name: `Tarea ${page}` }], {
        page,
        total: DEFAULT_PER_PAGE + 1,
        totalPages: 2,
      });
    });
    renderWith(<SchedulerPage />);

    expect(await screen.findByText('Tarea 1')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('common.next') }));
    expect(await screen.findByText('Tarea 2')).toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith({ page: 2, per_page: DEFAULT_PER_PAGE });
  });

  it('con tasks/cancel se cancela una tarea tras confirmarlo', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerTasks.read, PERMISSIONS.schedulerTasks.cancel);
    const jobs = vi.spyOn(schedulerApi, 'listJobs');
    const meta = vi.spyOn(schedulerMeta, 'get');
    vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue(tasksPage([TASK]));
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
    // Sin jobs/read no se pide la meta: la ventana llega con el propio listado.
    expect(meta).not.toHaveBeenCalled();
    expect(
      screen.getByText(
        t('scheduler.tasks.descriptionWindow', { window: formatSeconds(TASKS_WINDOW_SECONDS) }),
      ),
    ).toBeInTheDocument();
  });

  it('tras cancelar la unica tarea de la ultima pagina vuelve a la que existe', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerTasks.read, PERMISSIONS.schedulerTasks.cancel);
    let secondPageGone = false;
    const list = vi.spyOn(schedulerApi, 'listPendingTasks').mockImplementation(async (query) => {
      const page = query.page ?? 1;
      if (page === 2 && secondPageGone) {
        return tasksPage([], { page: 2, total: DEFAULT_PER_PAGE, totalPages: 1 });
      }
      return tasksPage([{ ...TASK, id: `task-${page}`, name: `Tarea ${page}` }], {
        page,
        total: DEFAULT_PER_PAGE + 1,
        totalPages: 2,
      });
    });
    vi.spyOn(schedulerApi, 'cancelTask').mockImplementation(async () => {
      secondPageGone = true;
      return { data: null };
    });
    renderWith(<SchedulerPage />);

    await screen.findByText('Tarea 1');
    await user.click(screen.getByRole('button', { name: t('common.next') }));
    await user.click(
      await screen.findByRole('button', {
        name: t('scheduler.tasks.cancelLabel', { name: 'Tarea 2' }),
      }),
    );
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: t('scheduler.tasks.cancel') }),
    );
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith({ page: 1, per_page: DEFAULT_PER_PAGE }),
    );
    expect(await screen.findByText('Tarea 1')).toBeInTheDocument();
  });

  it('cancelar una tarea que ya se ejecuto explica el 409 en espanol', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerTasks.read, PERMISSIONS.schedulerTasks.cancel);
    vi.spyOn(schedulerApi, 'listPendingTasks').mockResolvedValue(tasksPage([TASK]));
    vi.spyOn(schedulerApi, 'cancelTask').mockRejectedValue(
      rejected(409, ERROR_CODES.CONFLICT, 'task already executed and can no longer be cancelled'),
    );
    renderWith(<SchedulerPage />);

    await user.click(
      await screen.findByRole('button', {
        name: t('scheduler.tasks.cancelLabel', { name: TASK.name }),
      }),
    );
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('scheduler.tasks.cancel') }));
    expect(
      await within(dialog).findByText(t('scheduler.tasks.cancelConflict')),
    ).toBeInTheDocument();
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

  it('muestra la proxima ejecucion, el ultimo lanzamiento y el resultado de la ultima', async () => {
    grant(PERMISSIONS.schedulerJobs.read);
    mockCatalogs();
    const job = jobFixture({
      last_run_at: '2026-09-13T07:00:00Z',
      last_execution: {
        id: 'e-last',
        status: 'failed',
        completed_at: '2026-09-13T07:10:00Z',
        failure_reason: 'timeout',
      },
    });
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({ data: job });
    renderDetail();

    expect(await screen.findByRole('heading', { name: job.name })).toBeInTheDocument();
    expect(screen.getByText(formatDateTime(job.next_run_at))).toBeInTheDocument();
    expect(screen.getByText(formatDateTime(job.last_run_at))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.detail.lastRunHint'))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.executionStatus.failed'))).toBeInTheDocument();
    expect(screen.getByText(formatDateTime('2026-09-13T07:10:00Z'))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.failureReason.timeout'))).toBeInTheDocument();
  });

  it('un trabajo inactivo y sin ejecuciones lo dice en vez de dejar fechas vacias', async () => {
    grant(PERMISSIONS.schedulerJobs.read);
    mockCatalogs();
    const job = jobFixture({ is_active: false, next_run_at: null });
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({ data: job });
    renderDetail();

    expect(await screen.findByText(t('scheduler.nextRun.inactive'))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.lastExecution.none'))).toBeInTheDocument();
  });

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
    const get = vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({ data: JOB });
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
    // El trabajo se relee: su ultima ejecucion es la que se acaba de lanzar.
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
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

  it('un one_time que el servidor da por lanzado no ofrece activar y explica como repetirlo', async () => {
    grant(
      PERMISSIONS.schedulerJobs.read,
      PERMISSIONS.schedulerJobs.update,
      PERMISSIONS.schedulerJobs.run,
    );
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({
      data: jobFixture({
        job_type: 'one_time',
        cron_expression: null,
        is_active: false,
        next_run_at: null,
        last_run_at: '2026-09-13T09:00:00Z',
        already_run: true,
      }),
    });
    const enable = vi.spyOn(schedulerApi, 'enableJob');
    renderDetail();

    expect(await screen.findByText(t('scheduler.alreadyRunHint'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('common.activate') })).toBeNull();
    expect(screen.queryByRole('button', { name: t('common.deactivate') })).toBeNull();
    expect(screen.getByRole('button', { name: t('scheduler.run') })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('common.edit') })).toBeInTheDocument();
    expect(enable).not.toHaveBeenCalled();
  });

  it('la regla es la del servidor: sin already_run se ofrece activar, tambien a un one_time', async () => {
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerJobs.update);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({
      data: jobFixture({
        job_type: 'one_time',
        cron_expression: null,
        is_active: false,
        next_run_at: null,
        last_run_at: '2026-09-13T09:00:00Z',
        already_run: false,
      }),
    });
    renderDetail();

    expect(await screen.findByRole('button', { name: t('common.activate') })).toBeInTheDocument();
    expect(screen.queryByText(t('scheduler.alreadyRunHint'))).toBeNull();
  });

  it('si el servidor lo rechaza igual (lo despacho despues de leerlo) explica el 409 JOB_ALREADY_RUN', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerJobs.update);
    mockCatalogs();
    vi.spyOn(schedulerApi, 'getJob').mockResolvedValue({
      data: jobFixture({
        job_type: 'one_time',
        cron_expression: null,
        is_active: false,
        next_run_at: null,
      }),
    });
    const enable = vi
      .spyOn(schedulerApi, 'enableJob')
      .mockRejectedValue(
        rejected(
          409,
          ERROR_CODES.JOB_ALREADY_RUN,
          'a one_time job that already ran cannot be re-enabled',
        ),
      );
    renderDetail();

    await user.click(await screen.findByRole('button', { name: t('common.activate') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('common.activate') }));
    expect(await within(dialog).findByText(t('error.code.JOB_ALREADY_RUN'))).toBeInTheDocument();
    expect(enable).toHaveBeenCalledTimes(1);
  });

  it('si otra persona guardo el trabajo mientras se editaba, lo explica y recarga el formulario con la version vigente', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.schedulerJobs.read, PERMISSIONS.schedulerJobs.update);
    mockCatalogs();
    const read = jobFixture({ version: 3 });
    const current = jobFixture({ version: 4, name: 'Resumen de la manana', timezone: 'Europe/Madrid' });
    const get = vi
      .spyOn(schedulerApi, 'getJob')
      .mockResolvedValueOnce({ data: read })
      .mockResolvedValue({ data: current });
    const update = vi
      .spyOn(schedulerApi, 'updateJob')
      .mockRejectedValueOnce(
        rejected(409, ERROR_CODES.VERSION_CONFLICT, 'the job changed after it was read'),
      )
      .mockResolvedValue({ data: { ...current, version: 5 } });
    renderDetail();

    await user.click(await screen.findByRole('button', { name: t('common.edit') }));
    const name = await screen.findByLabelText(new RegExp(`^${escape(t('common.name'))}`));
    await user.clear(name);
    await user.type(name, 'Otro nombre');
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    expect(await screen.findByText(t('scheduler.form.staleTitle'))).toBeInTheDocument();
    expect(screen.getByText(t('scheduler.form.staleBody'))).toBeInTheDocument();
    expect(screen.queryByText('the job changed after it was read')).toBeNull();
    expect(update).toHaveBeenCalledWith(
      read.id,
      expect.objectContaining({ version: 3, name: 'Otro nombre' }),
    );
    expect(screen.getByRole('button', { name: t('common.save') })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: t('scheduler.form.reload') }));
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(field(t('scheduler.form.timezone'))).toHaveValue('Europe/Madrid'));
    expect(field(t('common.name'))).toHaveValue(current.name);
    expect(screen.queryByText(t('scheduler.form.staleTitle'))).toBeNull();

    const save = screen.getByRole('button', { name: t('common.save') });
    expect(save).toBeEnabled();
    await user.click(save);
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    expect(update).toHaveBeenLastCalledWith(
      current.id,
      expect.objectContaining({ version: 4, name: current.name, timezone: 'Europe/Madrid' }),
    );
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
