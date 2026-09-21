import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { MODULES } from '@/access/modules';
import { useAccessStore } from '@/access/store';
import type * as AuditModule from '@/api/audit';
import { auditApi, VERIFICATION_RUNNING, type IntegrityRun } from '@/api/audit';
import { ApiError } from '@/api/errors';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import IntegrityPage, { POLL_INTERVAL_MS } from './IntegrityPage';

vi.mock('@/api/audit', async (importOriginal) => {
  const original = await importOriginal<typeof AuditModule>();
  return {
    ...original,
    auditApi: {
      startIntegrityRun: vi.fn(),
      listIntegrityRuns: vi.fn(),
      integrityRun: vi.fn(),
      cancelIntegrityRun: vi.fn(),
    },
  };
});

const api = vi.mocked(auditApi);

function runOf(overrides: Partial<IntegrityRun> = {}): IntegrityRun {
  return {
    id: 'run-1',
    mode: 'full',
    trigger: 'manual',
    status: 'running',
    phase: 'audit_logs',
    checked: 500,
    current_seq: 500,
    target_seq: 1000,
    cancel_requested: false,
    started_at: '2026-09-21T10:00:00Z',
    heartbeat_at: '2026-09-21T10:00:05Z',
    ...overrides,
  };
}

const OK_RESULT = { ok: true, checked: 1000, chain: 'audit_logs' };

function grantVerify() {
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.audit],
    permissions: [{ module: MODULES.audit, resource: 'integrity', action: 'verify' }],
  });
}

async function mount() {
  const view = render(
    <MemoryRouter>
      <ToastProvider>
        <IntegrityPage />
      </ToastProvider>
    </MemoryRouter>,
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

async function click(name: string) {
  fireEvent.click(screen.getByRole('button', { name }));
  await tick(0);
}

beforeEach(() => {
  vi.useFakeTimers();
  grantVerify();
});

afterEach(() => {
  vi.useRealTimers();
  vi.resetAllMocks();
  useAccessStore.getState().reset();
});

describe('pagina de integridad del rastro', () => {
  it('sin ninguna verificacion previa ofrece verificar', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [] });
    await mount();
    expect(screen.getByText(t('audit.integrity.idle'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('audit.integrity.verify') })).toBeEnabled();
  });

  it('al abrirla muestra el resultado de la ultima verificacion sin volver a lanzarla', async () => {
    api.listIntegrityRuns.mockResolvedValue({
      data: [
        runOf({
          status: 'completed',
          phase: 'done',
          checked: 1000,
          result: OK_RESULT,
          finished_at: '2026-09-21T10:01:00Z',
        }),
      ],
    });
    await mount();
    expect(screen.getByText(t('audit.integrity.ok'))).toBeInTheDocument();
    expect(screen.getByText(t('audit.integrity.checked', { n: 1000 }))).toBeInTheDocument();
    expect(api.startIntegrityRun).not.toHaveBeenCalled();
    await tick(POLL_INTERVAL_MS * 3);
    expect(api.integrityRun).not.toHaveBeenCalled();
  });

  it('lanza la verificacion completa, muestra el avance y se detiene al terminar', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [] });
    api.startIntegrityRun.mockResolvedValue({ data: runOf() });
    api.integrityRun
      .mockResolvedValueOnce({ data: runOf({ checked: 800, current_seq: 800 }) })
      .mockResolvedValue({
        data: runOf({
          status: 'completed',
          phase: 'done',
          checked: 1000,
          current_seq: 1000,
          result: OK_RESULT,
          finished_at: '2026-09-21T10:01:00Z',
        }),
      });
    await mount();

    await click(t('audit.integrity.verify'));
    expect(api.startIntegrityRun).toHaveBeenCalledWith('full');
    expect(screen.getByText(t('audit.integrity.running'))).toBeInTheDocument();
    expect(screen.getByText(t('audit.integrity.phase.audit_logs'))).toBeInTheDocument();
    expect(screen.getByRole('meter', { name: t('audit.integrity.progress') })).toHaveAttribute(
      'aria-valuenow',
      '50',
    );
    expect(screen.getByRole('button', { name: t('audit.integrity.verify') })).toBeDisabled();

    await tick(POLL_INTERVAL_MS);
    expect(api.integrityRun).toHaveBeenCalledWith('run-1');
    expect(screen.getByText(t('audit.integrity.checked', { n: 800 }))).toBeInTheDocument();
    expect(screen.getByRole('meter', { name: t('audit.integrity.progress') })).toHaveAttribute(
      'aria-valuenow',
      '80',
    );

    await tick(POLL_INTERVAL_MS);
    expect(screen.getByText(t('audit.integrity.ok'))).toBeInTheDocument();
    expect(api.integrityRun).toHaveBeenCalledTimes(2);

    await tick(POLL_INTERVAL_MS * 3);
    expect(api.integrityRun).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('button', { name: t('audit.integrity.verify') })).toBeEnabled();
  });

  it('la verificacion solo de lo nuevo pide el modo incremental', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [] });
    api.startIntegrityRun.mockResolvedValue({ data: runOf({ mode: 'incremental' }) });
    await mount();
    await click(t('audit.integrity.verifyNew'));
    expect(api.startIntegrityRun).toHaveBeenCalledWith('incremental');
  });

  it('una cadena rota dice la causa, la cadena y la posicion', async () => {
    api.listIntegrityRuns.mockResolvedValue({
      data: [
        runOf({
          status: 'completed',
          phase: 'done',
          checked: 4200,
          result: {
            ok: false,
            checked: 4000,
            chain: 'audit_logs',
            reason: 'checkpoint_mismatch',
            broken_seq: 2500,
          },
        }),
      ],
    });
    await mount();
    expect(screen.getByText(t('audit.integrity.broken'))).toBeInTheDocument();
    expect(screen.getByText(t('audit.integrity.reason.checkpoint_mismatch'))).toBeInTheDocument();
    expect(screen.getByText(t('audit.integrity.chain.audit_logs'))).toBeInTheDocument();
    expect(screen.getByText('2500')).toBeInTheDocument();
    expect(screen.queryByText(t('audit.integrity.ok'))).toBeNull();
  });

  it('cancelar pide la cancelacion y termina como cancelada', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [runOf()] });
    api.cancelIntegrityRun.mockResolvedValue({ data: runOf({ cancel_requested: true }) });
    api.integrityRun.mockResolvedValue({
      data: runOf({ status: 'cancelled', checked: 900, finished_at: '2026-09-21T10:01:00Z' }),
    });
    await mount();
    expect(screen.getByText(t('audit.integrity.running'))).toBeInTheDocument();

    await click(t('audit.integrity.cancel'));
    expect(api.cancelIntegrityRun).toHaveBeenCalledWith('run-1');
    expect(screen.getByRole('button', { name: t('audit.integrity.cancelling') })).toBeDisabled();

    await tick(POLL_INTERVAL_MS);
    expect(screen.getByText(t('audit.integrity.cancelled'))).toBeInTheDocument();
    expect(
      screen.getByText(t('audit.integrity.cancelledDescription', { n: 900 })),
    ).toBeInTheDocument();
    expect(screen.queryByText(t('audit.integrity.broken'))).toBeNull();
  });

  it('un fallo tecnico no se presenta como cadena rota y dice su causa', async () => {
    api.listIntegrityRuns.mockResolvedValue({
      data: [runOf({ status: 'failed', error: 'timeout', finished_at: '2026-09-21T10:01:00Z' })],
    });
    await mount();
    expect(screen.getByText(t('audit.integrity.failed'))).toBeInTheDocument();
    expect(screen.getByText(t('audit.integrity.error.timeout'))).toBeInTheDocument();
    expect(screen.queryByText(t('audit.integrity.broken'))).toBeNull();
    expect(screen.queryByText(t('audit.integrity.ok'))).toBeNull();
  });

  it('si ya hay una verificacion en curso, sigue esa en lugar de mostrar un error', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [] });
    api.startIntegrityRun.mockRejectedValue(
      new ApiError(
        409,
        { code: VERIFICATION_RUNNING, message: 'ya hay una' },
        {
          error: {
            code: VERIFICATION_RUNNING,
            message: 'ya hay una',
            details: { run_id: 'run-7' },
          },
        },
      ),
    );
    api.integrityRun.mockResolvedValue({ data: runOf({ id: 'run-7' }) });
    await mount();

    await click(t('audit.integrity.verify'));
    expect(api.integrityRun).toHaveBeenCalledWith('run-7');
    expect(screen.getByText(t('audit.integrity.running'))).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('un error al lanzar se muestra y no cambia el estado', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [] });
    api.startIntegrityRun.mockRejectedValue(
      new ApiError(429, { code: 'VERIFICATION_BUSY', message: 'ocupado el servicio' }),
    );
    await mount();
    await click(t('audit.integrity.verify'));
    expect(screen.getByRole('alert')).toHaveTextContent('ocupado el servicio');
    expect(screen.getByText(t('audit.integrity.idle'))).toBeInTheDocument();
  });

  it('si falla la consulta del avance avisa y sigue consultando', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [runOf()] });
    api.integrityRun
      .mockRejectedValueOnce(new ApiError(0, { code: 'NETWORK_ERROR', message: '' }))
      .mockResolvedValue({ data: runOf({ checked: 700, current_seq: 700 }) });
    await mount();

    await tick(POLL_INTERVAL_MS);
    expect(screen.getByText(t('audit.integrity.pollError'))).toBeInTheDocument();
    await tick(POLL_INTERVAL_MS);
    expect(screen.queryByText(t('audit.integrity.pollError'))).toBeNull();
    expect(screen.getByText(t('audit.integrity.checked', { n: 700 }))).toBeInTheDocument();
  });

  it('deja de consultar al desmontarse', async () => {
    api.listIntegrityRuns.mockResolvedValue({ data: [runOf()] });
    api.integrityRun.mockResolvedValue({ data: runOf() });
    const view = await mount();
    view.unmount();
    await tick(POLL_INTERVAL_MS * 3);
    expect(api.integrityRun).not.toHaveBeenCalled();
  });

  it('sin permiso de verificar no se pide nada ni se ofrece', async () => {
    useAccessStore.setState({
      permissions: [{ module: MODULES.audit, resource: 'integrity', action: 'read' }],
    });
    await mount();
    expect(api.listIntegrityRuns).not.toHaveBeenCalled();
    expect(screen.getByText(t('audit.integrity.noVerify'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('audit.integrity.verify') })).toBeNull();
  });
});
