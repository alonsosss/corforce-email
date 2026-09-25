import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type MailMessage, type Signature } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import ComposePage, { AUTOSAVE_DELAY_MS, UNDO_SEND_MS } from './ComposePage';
import { NEW_MESSAGE, type ComposeRequest } from './composeWindow';
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

function renderCompose(request: ComposeRequest = NEW_MESSAGE, onClose = vi.fn()) {
  renderScreen(<ComposePage request={request} onClose={onClose} />, {
    outlet: outletFor([INBOX, DRAFTS]),
  });
  return onClose;
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
      const onClose = renderCompose();

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
      // Enviado, la redaccion se cierra sola.
      expect(onClose).toHaveBeenCalledTimes(1);
      const after = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(after);
      expect(after.defaultPrevented).toBe(false);
    });

    it('deshacer desde el aviso cancela el envio y la redaccion sigue abierta', async () => {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
      vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 1 });
      const send = vi.spyOn(webmailApi, 'send').mockResolvedValue(SENT);
      const onClose = renderCompose();

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
      expect(onClose).not.toHaveBeenCalled();
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

  it('responder y reenviar conservan el formato y las imagenes en linea del original', async () => {
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    const partUrl = '/api/v1/webmail/folders/INBOX/messages/42/parts/1.2';
    const original: MailMessage = {
      uid: 42,
      folder: 'INBOX',
      from: [{ name: 'Luis', email: 'luis@cliente.com' }],
      to: [{ name: 'Ana', email: 'ana@empresa.com' }],
      cc: [],
      bcc: [],
      reply_to: [],
      subject: 'Plano',
      date: '2026-09-01T10:00:00Z',
      flags: [],
      size: 100,
      has_attachments: true,
      message_id: 'x@cliente.com',
      in_reply_to: [],
      references: [],
      text: 'Mira el plano',
      text_truncated: false,
      html: `<p>Mira el <b>plano</b></p><img src="${partUrl}" alt="plano">`,
      html_truncated: false,
      remote_images: { present: false, blocked: false },
      attachments: [
        {
          part: '1.2',
          filename: 'plano.png',
          content_type: 'image/png',
          size: 3,
          content_id: 'plano@x',
          inline: true,
        },
      ],
    };
    vi.spyOn(webmailApi, 'message').mockResolvedValue(original);
    const download = vi.spyOn(webmailApi, 'downloadPart').mockResolvedValue({
      blob: new Blob([new Uint8Array([1, 2, 3])]),
      filename: 'plano.png',
      contentType: 'image/png',
    });
    renderCompose({
      kind: 'source',
      mode: 'reply',
      folder: 'INBOX',
      uid: 42,
      assistantText: null,
    });

    const editor = await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    const quoted = editor.querySelector('blockquote');
    expect(quoted?.querySelector('b')?.textContent).toBe('plano');
    expect(quoted?.querySelector('img')?.getAttribute('src')).toMatch(/^data:image\/png;base64,/);
    expect(download).toHaveBeenCalledWith('INBOX', 42, '1.2', expect.anything());
    expect(screen.queryByText('plano.png')).toBeNull();
  });

  it('cerrar y descartar sin cambios cierran la redaccion sin navegar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    const onClose = renderCompose();

    await screen.findByLabelText(t('webmail.header.subject'));
    await user.click(screen.getByRole('button', { name: t('webmail.compose.discard') }));
    await user.click(screen.getByRole('button', { name: t('webmail.composer.close') }));
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId('location').textContent).toBe('/webmail');
  });
});
