import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { useAccessStore } from '@/access/store';
import { mailMigrationApi } from '@/api/mailMigration';
import type { Mailbox } from '@/api/mailDirectory';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { MigrationTab, POLL_INTERVAL_MS } from './MigrationTab';
import { jobOf, META } from './migrationFixtures';

vi.mock('@/api/mailMigration', () => ({
  mailMigrationApi: {
    meta: vi.fn(),
    listJobs: vi.fn(),
    getJob: vi.fn(),
    createJob: vi.fn(),
    cancelJob: vi.fn(),
  },
}));

const api = vi.mocked(mailMigrationApi);
const MAILBOX = { id: 'mb-1', username: 'ana@empresa.test' } as Mailbox;

function grant(...actions: string[]) {
  useAccessStore.setState({
    loaded: true,
    policyLoaded: true,
    permissions: actions.map((action) => ({ module: 'migration', resource: 'jobs', action })),
  });
}

function page(...items: ReturnType<typeof jobOf>[]) {
  return { items, page: 1, perPage: 10, total: items.length, totalPages: 1 };
}

async function mount() {
  const view = render(
    <ToastProvider>
      <MigrationTab mailbox={MAILBOX} />
    </ToastProvider>,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  return view;
}

async function tick(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  api.meta.mockResolvedValue(META);
});

afterEach(() => {
  vi.useRealTimers();
  vi.resetAllMocks();
  useAccessStore.getState().reset();
});

describe('pestana de migracion', () => {
  it('sondea mientras el trabajo esta en curso y se detiene cuando termina', async () => {
    grant('read');
    api.listJobs
      .mockResolvedValueOnce(page(jobOf({ status: 'running' })))
      .mockResolvedValueOnce(page(jobOf({ status: 'running' })))
      .mockResolvedValue(page(jobOf({ status: 'succeeded' })));
    await mount();
    expect(api.listJobs).toHaveBeenCalledTimes(1);

    await tick(POLL_INTERVAL_MS);
    expect(api.listJobs).toHaveBeenCalledTimes(2);
    await tick(POLL_INTERVAL_MS);
    expect(api.listJobs).toHaveBeenCalledTimes(3);
    expect(screen.getByText(t('migration.status.succeeded'))).toBeInTheDocument();

    await tick(POLL_INTERVAL_MS * 3);
    expect(api.listJobs).toHaveBeenCalledTimes(3);
  });

  it('deja de sondear al desmontar', async () => {
    grant('read');
    api.listJobs.mockResolvedValue(page(jobOf({ status: 'running' })));
    const view = await mount();
    view.unmount();
    await tick(POLL_INTERVAL_MS * 3);
    expect(api.listJobs).toHaveBeenCalledTimes(1);
  });

  it('no sondea si no hay trabajos activos', async () => {
    grant('read');
    api.listJobs.mockResolvedValue(page(jobOf({ status: 'failed' })));
    await mount();
    await tick(POLL_INTERVAL_MS * 3);
    expect(api.listJobs).toHaveBeenCalledTimes(1);
  });

  it('con el servicio sin configurar avisa y no ofrece lanzar', async () => {
    grant('read', 'create');
    api.meta.mockResolvedValue({ ...META, configured: false });
    api.listJobs.mockResolvedValue(page());
    await mount();
    expect(screen.getByText(t('migration.notConfigured.title'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('migration.form.submit') }),
    ).not.toBeInTheDocument();
  });

  it('sin el permiso de crear no hay formulario; con el, si', async () => {
    grant('read');
    api.listJobs.mockResolvedValue(page());
    const view = await mount();
    expect(
      screen.queryByRole('button', { name: t('migration.form.submit') }),
    ).not.toBeInTheDocument();
    view.unmount();

    grant('read', 'create');
    await mount();
    expect(screen.getByRole('button', { name: t('migration.form.submit') })).toBeInTheDocument();
  });

  it('con un trabajo activo no ofrece lanzar otro y explica por que', async () => {
    grant('read', 'create');
    api.listJobs.mockResolvedValue(page(jobOf({ status: 'pending' })));
    await mount();
    expect(screen.getByText(t('migration.block.jobActive'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('migration.form.submit') }),
    ).not.toBeInTheDocument();
  });

  it('el boton de cancelar solo aparece con el permiso y con el trabajo activo', async () => {
    grant('read');
    api.listJobs.mockResolvedValue(page(jobOf({ status: 'running' })));
    const view = await mount();
    expect(
      screen.queryByRole('button', { name: t('migration.job.cancel') }),
    ).not.toBeInTheDocument();
    view.unmount();

    grant('read', 'cancel');
    const again = await mount();
    expect(screen.getByRole('button', { name: t('migration.job.cancel') })).toBeInTheDocument();
    again.unmount();

    api.listJobs.mockResolvedValue(page(jobOf({ status: 'succeeded' })));
    await mount();
    expect(
      screen.queryByRole('button', { name: t('migration.job.cancel') }),
    ).not.toBeInTheDocument();
  });

  it('muestra el error del trabajo con el texto de su codigo', async () => {
    grant('read');
    api.listJobs.mockResolvedValue(
      page(
        jobOf({
          status: 'failed',
          last_error: { code: 'source_auth_failed', message: 'LOGIN failed' },
        }),
      ),
    );
    await mount();
    expect(screen.getByText(t('migration.failure.source_auth_failed'))).toBeInTheDocument();
  });
});
