import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type Signature } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import ComposePage, { AUTOSAVE_DELAY_MS, UNDO_SEND_MS } from './ComposePage';
import { DRAFTS, INBOX, META, outletFor, renderScreen } from './testing';

const SIGNATURE: Signature = {
  enabled: false,
  html: '',
  text: '',
  on_replies: false,
  updated_at: null,
  limits: { max_html_bytes: 8192, max_text_bytes: 4096 },
};

const SENT = { message_id: 'm@x', saved_to_sent: true, draft_removed: false, replayed: false };

function renderCompose(url = '/webmail/compose') {
  return renderScreen(<ComposePage />, {
    path: '/webmail/compose',
    url,
    outlet: outletFor([INBOX, DRAFTS]),
  });
}

async function fillMessage(user: ReturnType<typeof userEvent.setup>) {
  const to = await screen.findByLabelText(t('webmail.header.to'));
  await user.type(to, 'luis@cliente.com{Enter}');
  await user.type(screen.getByLabelText(t('webmail.header.subject')), 'Pedido');
}

describe('redaccion', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    useWebmailStore.setState({
      status: 'authenticated',
      session: {
        username: 'ana@empresa.com',
        display_name: 'Ana',
        expires_at: '2026-09-24T20:00:00Z',
        idle_timeout_seconds: 1800,
        quota: null,
      },
    });
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
    vi.spyOn(webmailApi, 'identities').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 0,
      total: 0,
      totalPages: 0,
    });
    vi.spyOn(webmailApi, 'addressBook').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'session').mockRejectedValue(new ApiError(503, null));
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  describe('deshacer envio', () => {
    beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));

    it('no sale ninguna peticion hasta que vence el plazo, y luego se envia', async () => {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
      vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
      const send = vi.spyOn(webmailApi, 'send').mockResolvedValue(SENT);
      renderCompose();

      await fillMessage(user);
      await user.click(screen.getByRole('button', { name: t('webmail.compose.send') }));

      expect(send).not.toHaveBeenCalled();
      expect(screen.getAllByRole('button', { name: t('webmail.compose.undo') }).length).toBe(2);
      // Cerrar la pestana en el plazo cancela el envio: el navegador pide confirmacion.
      const leaving = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(leaving);
      expect(leaving.defaultPrevented).toBe(true);
      await act(() => vi.advanceTimersByTimeAsync(UNDO_SEND_MS - 500));
      expect(send).not.toHaveBeenCalled();

      await act(() => vi.advanceTimersByTimeAsync(600));
      await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
      expect(send.mock.calls[0]?.[0]).toMatchObject({
        to: ['luis@cliente.com'],
        subject: 'Pedido',
      });
      expect(await screen.findByText(t('webmail.compose.sent'))).toBeInTheDocument();
      const after = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(after);
      expect(after.defaultPrevented).toBe(false);
    });

    it('deshacer desde el aviso cancela el envio y la redaccion sigue abierta', async () => {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
      vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
      const send = vi.spyOn(webmailApi, 'send').mockResolvedValue(SENT);
      renderCompose();

      await fillMessage(user);
      await user.click(screen.getByRole('button', { name: t('webmail.compose.send') }));
      const toast = screen
        .getAllByRole('status')
        .find((node) => node.classList.contains('cf-toast')) as HTMLElement;
      await user.click(within(toast).getByRole('button', { name: t('webmail.compose.undo') }));
      expect(screen.getByText(t('webmail.compose.sendUndone'))).toBeInTheDocument();
      await act(() => vi.advanceTimersByTimeAsync(UNDO_SEND_MS * 2));

      expect(send).not.toHaveBeenCalled();
      expect(screen.getByLabelText(t('webmail.header.subject'))).not.toBeDisabled();
    });

    it('si el envio falla al vencer el plazo, el error queda en la redaccion', async () => {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
      vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
      vi.spyOn(webmailApi, 'send').mockRejectedValue(
        new ApiError(
          422,
          { code: 'RECIPIENT_REJECTED', message: '' },
          {
            error: {
              code: 'RECIPIENT_REJECTED',
              message: '',
              details: { address: 'luis@cliente.com' },
            },
          },
        ),
      );
      renderCompose();

      await fillMessage(user);
      await user.click(screen.getByRole('button', { name: t('webmail.compose.send') }));
      await act(() => vi.advanceTimersByTimeAsync(UNDO_SEND_MS + 100));

      expect(
        await screen.findByText(
          t('webmail.compose.recipientRejected', { address: 'luis@cliente.com' }),
        ),
      ).toBeInTheDocument();
    });

    it('guarda el borrador solo tras una pausa y lo indica; un fallo tambien se indica', async () => {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
      vi.spyOn(webmailApi, 'message').mockResolvedValue({} as never);
      const save = vi
        .spyOn(webmailApi, 'saveDraft')
        .mockResolvedValueOnce({ uid: 41 })
        .mockRejectedValueOnce(new ApiError(507, { code: 'QUOTA_EXCEEDED', message: '' }));
      renderCompose();

      await fillMessage(user);
      expect(save).not.toHaveBeenCalled();
      await act(() => vi.advanceTimersByTimeAsync(AUTOSAVE_DELAY_MS + 100));
      await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
      expect(save.mock.calls[0]?.[1]).toBeUndefined();
      expect(
        await screen.findByText(new RegExp(t('webmail.compose.autosaved', { time: '' }))),
      ).toBeInTheDocument();

      await user.type(screen.getByLabelText(t('webmail.header.subject')), ' urgente');
      await act(() => vi.advanceTimersByTimeAsync(AUTOSAVE_DELAY_MS + 100));
      await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
      // El segundo guardado reemplaza el borrador anterior.
      expect(save.mock.calls[1]?.[1]).toBe(41);
      expect(await screen.findByText(t('webmail.compose.autosaveFailed'))).toBeInTheDocument();
    });
  });

  it('abre un mensaje nuevo con la firma del buzon en el editor', async () => {
    vi.spyOn(webmailApi, 'signature').mockResolvedValue({
      ...SIGNATURE,
      enabled: true,
      html: '<p><b>Ana Perez</b></p><script>x()</script>',
      text: 'Ana Perez',
    });
    renderCompose();

    const editor = await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    expect(editor.textContent).toContain('Ana Perez');
    expect(editor.querySelector('b')).not.toBeNull();
    expect(editor.querySelector('script')).toBeNull();
  });

  it('si la firma no se puede leer, se redacta igual sin ella', async () => {
    vi.spyOn(webmailApi, 'signature').mockRejectedValue(new ApiError(503, null));
    renderCompose();
    const editor = await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    expect(editor.textContent).toBe('');
  });

  it('en texto sin formato envia text y no html', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
    const send = vi.spyOn(webmailApi, 'send').mockResolvedValue(SENT);
    renderCompose();

    await fillMessage(user);
    await user.click(screen.getByRole('button', { name: t('webmail.compose.toPlain') }));
    await user.type(screen.getByLabelText(t('webmail.compose.body')), 'Hola Luis');
    await user.click(screen.getByRole('button', { name: t('webmail.compose.send') }));
    await act(() => vi.advanceTimersByTimeAsync(UNDO_SEND_MS + 100));

    await waitFor(() => expect(send).toHaveBeenCalled());
    expect(send.mock.calls[0]?.[0]).toMatchObject({ text: 'Hola Luis', html: undefined });
  });

  it('programar valida contra max_scheduled_days de la meta y luego programa', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
    const schedule = vi
      .spyOn(webmailApi, 'schedule')
      .mockResolvedValue({ id: 's1', send_at: '2099-01-01T08:00:00Z' });
    renderCompose();

    await fillMessage(user);
    await user.click(screen.getByRole('button', { name: t('webmail.compose.sendLater') }));
    const dialog = await screen.findByRole('dialog');
    const when = within(dialog).getByLabelText(t('webmail.schedule.when'));
    const far = new Date();
    far.setDate(far.getDate() + META.limits.max_scheduled_days + 2);
    const local = (d: Date) =>
      `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(
        d.getDate(),
      ).padStart(2, '0')}T10:00`;
    fireEvent.change(when, { target: { value: local(far) } });
    await user.click(within(dialog).getByRole('button', { name: t('webmail.schedule.confirm') }));
    expect(
      await within(dialog).findByText(
        t('webmail.schedule.tooFar', { days: META.limits.max_scheduled_days }),
      ),
    ).toBeInTheDocument();
    expect(schedule).not.toHaveBeenCalled();

    const soon = new Date();
    soon.setDate(soon.getDate() + 2);
    fireEvent.change(when, { target: { value: local(soon) } });
    await user.click(within(dialog).getByRole('button', { name: t('webmail.schedule.confirm') }));
    await waitFor(() => expect(schedule).toHaveBeenCalledTimes(1));
    expect(schedule.mock.calls[0]?.[1]).toBe(new Date(local(soon)).toISOString());
  });

  it('propone destinatarios de la agenda personal y de la empresa', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    vi.mocked(webmailApi.contacts).mockResolvedValue({
      items: [
        {
          id: 'c1',
          etag: 'e',
          updated_at: '',
          name: 'Luis Rojas',
          given_name: '',
          family_name: '',
          emails: [{ value: 'luis@cliente.com', type: 'work' }],
          phones: [],
          organization: '',
          title: '',
          notes: '',
          birthday: '',
        },
      ],
      page: 1,
      perPage: 0,
      total: 1,
      totalPages: 1,
    });
    vi.mocked(webmailApi.addressBook).mockRejectedValue(new ApiError(503, null));
    renderCompose();

    const to = await screen.findByLabelText(t('webmail.header.to'));
    await user.type(to, 'lui');
    const option = await screen.findByRole('option', { name: /Luis Rojas/ });
    await user.click(option);
    expect(screen.getByText('luis@cliente.com')).toBeInTheDocument();
    expect(webmailApi.contacts).toHaveBeenCalledWith({ q: 'lui' }, expect.anything());
  });

  it('Enter en un campo o en el dialogo de enlace no envia el mensaje', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
    const send = vi.spyOn(webmailApi, 'send').mockResolvedValue(SENT);
    renderCompose();

    await fillMessage(user);
    await user.type(screen.getByLabelText(t('webmail.header.subject')), '{Enter}');
    await user.click(screen.getByRole('button', { name: t('webmail.editor.link') }));
    await user.type(
      await screen.findByLabelText(t('webmail.editor.linkUrl')),
      'ejemplo.com{Enter}',
    );

    expect(
      screen.queryByText(t('webmail.compose.sendingSoon', { s: UNDO_SEND_MS / 1000 })),
    ).toBeNull();
    expect(send).not.toHaveBeenCalled();
  });
});
