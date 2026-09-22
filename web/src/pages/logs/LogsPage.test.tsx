import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { observabilityApi, LOGS_MAX_TEXT, type LogPage } from '@/api/observability';
import { t } from '@/i18n';
import LogsPage from './LogsPage';

const SERVICES = ['gateway', 'postfix-mail', 'rspamd-mail'];

const PAGE: LogPage = {
  service: 'postfix-mail',
  since: '2026-09-21T11:00:00Z',
  until: '2026-09-21T12:00:00Z',
  direction: 'backward',
  limit: 100,
  truncated: false,
  entries: [
    {
      timestamp: '2026-09-21T11:30:00.123Z',
      line: 'to=<ana@acme.test>, status=sent <b>no es html</b>',
      labels: { contenedor: 'app-postfix-mail-1', flujo: 'stdout', plano: 'app' },
    },
    {
      timestamp: '2026-09-21T11:29:00.000Z',
      line: 'warning: algo',
      labels: { contenedor: 'app-postfix-mail-1', flujo: 'stderr', plano: 'app' },
    },
  ],
};

describe('visor de registros del superadmin', () => {
  afterEach(() => vi.restoreAllMocks());

  it('los servicios salen del API y una consulta manda el texto tal cual, con ventana y orden', async () => {
    const user = userEvent.setup();
    vi.spyOn(observabilityApi, 'listLogServices').mockResolvedValue(SERVICES);
    const query = vi.spyOn(observabilityApi, 'queryLogs').mockResolvedValue(PAGE);
    render(<LogsPage />);

    const select = (await screen.findByLabelText(t('logs.filter.service'))) as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toEqual(['', ...SERVICES]);
    await user.selectOptions(select, 'postfix-mail');
    await user.type(screen.getByLabelText(t('logs.filter.text')), 'status="sent" |= x');
    await user.selectOptions(screen.getByLabelText(t('logs.filter.window')), '6h');
    await user.selectOptions(screen.getByLabelText(t('logs.filter.direction')), 'forward');
    await user.selectOptions(screen.getByLabelText(t('logs.filter.limit')), '200');
    await user.click(screen.getByRole('button', { name: t('logs.search') }));

    expect(query).toHaveBeenCalledTimes(1);
    const params = query.mock.calls[0]?.[0];
    expect(params?.service).toBe('postfix-mail');
    expect(params?.q).toBe('status="sent" |= x');
    expect(params?.direction).toBe('forward');
    expect(params?.limit).toBe(200);
    expect(params?.until).toBeUndefined();
    const since = new Date(params?.since ?? '').getTime();
    expect(Date.now() - since).toBeGreaterThan(5 * 3600_000);
    expect(Date.now() - since).toBeLessThan(7 * 3600_000);

    const lines = await screen.findByRole('list', { name: t('logs.title') });
    const items = within(lines).getAllByRole('listitem');
    expect(items).toHaveLength(2);
    // La linea es texto: la etiqueta HTML se ve tal cual y no se interpreta.
    expect(items[0]?.textContent).toContain('<b>no es html</b>');
    expect(lines.querySelector('b')).toBeNull();
    expect(items[1]?.textContent).toContain(`[${t('logs.stream.stderr')}]`);
  });

  it('sin servicio no consulta, y el texto queda acotado al tope del servicio', async () => {
    const user = userEvent.setup();
    vi.spyOn(observabilityApi, 'listLogServices').mockResolvedValue(SERVICES);
    const query = vi.spyOn(observabilityApi, 'queryLogs').mockResolvedValue(PAGE);
    render(<LogsPage />);
    await screen.findByLabelText(t('logs.filter.service'));

    await user.click(screen.getByRole('button', { name: t('logs.search') }));
    expect(await screen.findByText(t('logs.serviceRequired'))).toBeInTheDocument();
    expect(query).not.toHaveBeenCalled();

    await user.selectOptions(screen.getByLabelText(t('logs.filter.service')), 'gateway');
    const text = screen.getByLabelText(t('logs.filter.text')) as HTMLInputElement;
    expect(text.maxLength).toBe(LOGS_MAX_TEXT);
    await user.type(text, 'x'.repeat(LOGS_MAX_TEXT + 5));
    expect(text.value).toHaveLength(LOGS_MAX_TEXT);
    await user.click(screen.getByRole('button', { name: t('logs.search') }));
    expect(query).toHaveBeenCalledTimes(1);
    expect(query.mock.calls[0]?.[0].q).toHaveLength(LOGS_MAX_TEXT);
    expect(screen.queryByText(t('logs.serviceRequired'))).toBeNull();
  });

  it('sin Loki configurado lo dice y no deja consultar', async () => {
    vi.spyOn(observabilityApi, 'listLogServices').mockResolvedValue(SERVICES);
    vi.spyOn(observabilityApi, 'queryLogs').mockRejectedValue(
      new ApiError(503, { code: 'NOT_CONFIGURED', message: 'integracion no configurada' }),
    );
    const user = userEvent.setup();
    render(<LogsPage />);
    await user.selectOptions(await screen.findByLabelText(t('logs.filter.service')), 'gateway');
    await user.click(screen.getByRole('button', { name: t('logs.search') }));
    expect(await screen.findByText(t('logs.notConfigured'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('logs.search') })).toBeDisabled();
  });

  it('una ventana sin lineas lo dice y una truncada avisa', async () => {
    const user = userEvent.setup();
    vi.spyOn(observabilityApi, 'listLogServices').mockResolvedValue(SERVICES);
    const query = vi
      .spyOn(observabilityApi, 'queryLogs')
      .mockResolvedValueOnce({ ...PAGE, entries: [] })
      .mockResolvedValueOnce({ ...PAGE, truncated: true, limit: 100 });
    render(<LogsPage />);
    await user.selectOptions(await screen.findByLabelText(t('logs.filter.service')), 'gateway');
    await user.click(screen.getByRole('button', { name: t('logs.search') }));
    expect(await screen.findByText(t('logs.empty'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('logs.search') }));
    expect(await screen.findByText(t('logs.truncated', { limit: 100 }))).toBeInTheDocument();
    expect(query).toHaveBeenCalledTimes(2);
  });

  it('un almacen caido se explica sin romper la pantalla', async () => {
    const user = userEvent.setup();
    vi.spyOn(observabilityApi, 'listLogServices').mockResolvedValue(SERVICES);
    vi.spyOn(observabilityApi, 'queryLogs').mockRejectedValue(
      new ApiError(502, { code: 'LOGS_UNAVAILABLE', message: 'no responde' }),
    );
    render(<LogsPage />);
    await user.selectOptions(await screen.findByLabelText(t('logs.filter.service')), 'gateway');
    await user.click(screen.getByRole('button', { name: t('logs.search') }));
    expect(await screen.findByText(t('logs.unavailable'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('logs.search') })).toBeEnabled();
  });
});
