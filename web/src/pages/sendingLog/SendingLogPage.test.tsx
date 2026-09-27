import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { mailSecurityApi } from '@/api/mailSecurity';
import { transactionalApi } from '@/api/transactional';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import SendingLogPage from './SendingLogPage';

function grant(...triples: (readonly [string, string, string])[]) {
  const permissions: PermissionTriple[] = triples.map(([module, resource, action]) => ({
    module,
    resource,
    action,
  }));
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.transactional, MODULES.mailSecurity],
    permissions,
  });
}

function renderPage(path = '/sending/log') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ToastProvider>
        <SendingLogPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

const page = <T,>(items: T[]) => ({
  items,
  page: 1,
  perPage: 20,
  total: items.length,
  totalPages: 1,
});

describe('pantalla Envios', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  it('solo muestra las pestanas que el permiso deja ver', async () => {
    grant(PERMISSIONS.deliveryLog.read);
    const messages = vi.spyOn(transactionalApi, 'messages');
    vi.spyOn(mailSecurityApi, 'deliveryLog').mockResolvedValue(page([]));
    renderPage();
    expect(await screen.findByText(t('sendingLog.corporate.hint'))).toBeTruthy();
    expect(screen.queryByRole('tab', { name: t('sendingLog.tab.transactional') })).toBeNull();
    expect(messages).not.toHaveBeenCalled();
  });

  it('el correo corporativo muestra el rebote con la respuesta del servidor y filtra por estado', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.deliveryLog.read);
    const list = vi.spyOn(mailSecurityApi, 'deliveryLog').mockResolvedValue(
      page([
        {
          id: 'e1',
          direction: 'outbound' as const,
          queue_id: 'C9E8C60485',
          message_id: 'm@x',
          sender: 'ventas@mentorenergy.uk',
          recipient: 'nadie@empresa.example',
          status: 'bounced' as const,
          dsn: '5.1.1',
          relay: 'mx.empresa.example[203.0.113.5]:25',
          reason: 'host mx.empresa.example said: 550 5.1.1 User unknown',
          delay_seconds: '1.2',
          sasl_username: 'ventas@mentorenergy.uk',
          occurred_at: '2026-09-26T16:48:58Z',
        },
      ]),
    );
    renderPage();
    const table = await screen.findByRole('table');
    expect(within(table).getByText('nadie@empresa.example')).toBeTruthy();
    expect(within(table).getByText(/550 5.1.1 User unknown/)).toBeTruthy();

    await user.click(within(table).getByText('nadie@empresa.example'));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('mx.empresa.example[203.0.113.5]:25')).toBeTruthy();
    expect(within(dialog).getByText('5.1.1')).toBeTruthy();

    await user.keyboard('{Escape}');
    await user.selectOptions(screen.getByLabelText(t('sendingLog.column.status')), 'bounced');
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: 'bounced', page: 1 }),
      ),
    );
  });

  it('el transaccional abre la historia del envio con el motivo del rebote', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.transactionalMessages.read);
    vi.spyOn(transactionalApi, 'messages').mockResolvedValue(
      page([
        {
          id: 'm1',
          from_email: 'pedidos@campovivoalimentos.com',
          from_name: 'Campovivo',
          to: [{ email: 'cliente@example.com' }],
          cc: null,
          bcc: null,
          subject: 'Recibimos tu pedido',
          status: 'bounced' as const,
          attempts: 1,
          class: 'transactional',
          origin: 'smtp',
          created_at: '2026-09-26T10:00:00Z',
          updated_at: '2026-09-26T10:00:05Z',
          test: false,
        },
      ]),
    );
    const events = vi.spyOn(transactionalApi, 'events').mockResolvedValue([
      {
        id: 'ev1',
        type: 'bounce',
        recipient: 'cliente@example.com',
        detail: {
          reason: 'Permanent/General',
          diagnostic_code: 'smtp; 550 5.1.1 mailbox unavailable',
        },
        occurred_at: '2026-09-26T10:00:05Z',
      },
    ]);
    renderPage();
    const table = await screen.findByRole('table');
    expect(within(table).getByText(t('sendingLog.origin.smtp'))).toBeTruthy();
    await user.click(within(table).getByText('Recibimos tu pedido'));
    const dialog = await screen.findByRole('dialog');
    expect(await within(dialog).findByText('smtp; 550 5.1.1 mailbox unavailable')).toBeTruthy();
    expect(events).toHaveBeenCalledWith('m1');
  });
});
