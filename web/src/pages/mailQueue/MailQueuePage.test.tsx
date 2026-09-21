import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { mailSecurityApi, QUEUE_MAX_LIMIT, type QueueListing } from '@/api/mailSecurity';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import MailQueuePage from './MailQueuePage';

const LISTING: QueueListing = {
  total: 3,
  truncated: true,
  items: [
    {
      queue_id: 'ABCDEF1234',
      queue_name: 'deferred',
      arrival_time: 1_700_000_000,
      message_size: 2048,
      sender: 'ana@acme.test',
      recipients: [
        { address: 'x@ejemplo.org', delay_reason: 'connect to ejemplo.org: Connection timed out' },
        { address: 'y@ejemplo.org' },
      ],
      recipients_total: 2,
    },
    {
      queue_id: 'B7C2D9E1F0',
      queue_name: 'hold',
      arrival_time: 1_700_000_100,
      message_size: 10,
      sender: '',
      recipients: [{ address: 'z@ejemplo.net' }],
      recipients_total: 1,
    },
  ],
};

function renderPage() {
  render(
    <ToastProvider>
      <MailQueuePage />
    </ToastProvider>,
  );
}

const row = (id: string) => screen.getByRole('group', { name: t('mailQueue.actionsFor', { id }) });

describe('cola de correo del superadmin', () => {
  afterEach(() => vi.restoreAllMocks());

  it('pide la cola con el tope del servicio y muestra remitente, destinatarios y el motivo', async () => {
    const list = vi.spyOn(mailSecurityApi, 'listQueue').mockResolvedValue(LISTING);
    renderPage();

    expect(await screen.findByText('ana@acme.test')).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith(QUEUE_MAX_LIMIT);
    expect(screen.getByText(/x@ejemplo.org/)).toBeInTheDocument();
    expect(
      screen.getByText(t('mailQueue.moreRecipients', { n: 1 }), { exact: false }),
    ).toBeInTheDocument();
    expect(screen.getByText(/Connection timed out/)).toBeInTheDocument();
    // Un rebote no tiene remitente.
    expect(screen.getByText(t('mailQueue.bounce'))).toBeInTheDocument();
    expect(screen.getByText(t('mailQueue.showing', { shown: 2, total: 3 }))).toBeInTheDocument();
  });

  it('un mensaje retenido se libera y uno diferido se retiene', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailSecurityApi, 'listQueue').mockResolvedValue(LISTING);
    const act = vi.spyOn(mailSecurityApi, 'queueAction').mockResolvedValue(null as never);
    renderPage();
    await screen.findByText('ana@acme.test');

    expect(
      within(row('ABCDEF1234')).queryByRole('button', { name: t('mailQueue.unhold') }),
    ).toBeNull();
    await user.click(within(row('ABCDEF1234')).getByRole('button', { name: t('mailQueue.hold') }));
    expect(act).toHaveBeenLastCalledWith('ABCDEF1234', 'hold');

    expect(
      within(row('B7C2D9E1F0')).queryByRole('button', { name: t('mailQueue.hold') }),
    ).toBeNull();
    await user.click(
      within(row('B7C2D9E1F0')).getByRole('button', { name: t('mailQueue.unhold') }),
    );
    expect(act).toHaveBeenLastCalledWith('B7C2D9E1F0', 'unhold');

    await user.click(within(row('ABCDEF1234')).getByRole('button', { name: t('mailQueue.retry') }));
    expect(act).toHaveBeenLastCalledWith('ABCDEF1234', 'retry');
  });

  it('borrar pide confirmacion, nombra el mensaje y no borra si se cancela', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailSecurityApi, 'listQueue').mockResolvedValue(LISTING);
    const del = vi.spyOn(mailSecurityApi, 'deleteQueueMessage').mockResolvedValue(null as never);
    renderPage();
    await screen.findByText('ana@acme.test');

    await user.click(
      within(row('ABCDEF1234')).getByRole('button', { name: t('mailQueue.delete') }),
    );
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/ABCDEF1234/)).toBeInTheDocument();
    expect(del).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: t('common.cancel') }));
    expect(del).not.toHaveBeenCalled();

    await user.click(
      within(row('ABCDEF1234')).getByRole('button', { name: t('mailQueue.delete') }),
    );
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: t('mailQueue.delete'),
      }),
    );
    expect(del).toHaveBeenCalledWith('ABCDEF1234');
  });

  it('vaciar la cola diferida tambien pide confirmacion', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailSecurityApi, 'listQueue').mockResolvedValue(LISTING);
    const flush = vi
      .spyOn(mailSecurityApi, 'flushQueue')
      .mockResolvedValue({ status: 'queued' } as never);
    renderPage();
    await screen.findByText('ana@acme.test');

    await user.click(screen.getByRole('button', { name: t('mailQueue.flush') }));
    expect(flush).not.toHaveBeenCalled();
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: t('mailQueue.flush') }),
    );
    expect(flush).toHaveBeenCalledTimes(1);
  });

  it('un mensaje que ya no esta en la cola se explica y no rompe la pantalla', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailSecurityApi, 'listQueue').mockResolvedValue(LISTING);
    vi.spyOn(mailSecurityApi, 'queueAction').mockRejectedValue(
      new ApiError(404, { code: 'NOT_FOUND', message: 'no encontrado' }),
    );
    renderPage();
    await screen.findByText('ana@acme.test');

    await user.click(within(row('ABCDEF1234')).getByRole('button', { name: t('mailQueue.retry') }));
    expect(await screen.findByText(t('mailQueue.gone'))).toBeInTheDocument();
  });

  it('sin agente configurado lo dice y no ofrece vaciar la cola', async () => {
    vi.spyOn(mailSecurityApi, 'listQueue').mockRejectedValue(
      new ApiError(503, { code: 'NOT_CONFIGURED', message: 'integracion no configurada' }),
    );
    renderPage();
    expect(await screen.findByText(t('mailQueue.notConfigured'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('mailQueue.flush') })).toBeDisabled();
  });

  it('una cola vacia lo dice', async () => {
    vi.spyOn(mailSecurityApi, 'listQueue').mockResolvedValue({
      total: 0,
      truncated: false,
      items: [],
    });
    renderPage();
    expect(await screen.findByText(t('mailQueue.empty'))).toBeInTheDocument();
  });
});
