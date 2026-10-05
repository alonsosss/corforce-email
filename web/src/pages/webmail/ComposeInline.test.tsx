import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type MailMessage } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import ComposePage, { UNDO_SEND_MS } from './ComposePage';
import {
  ComposeControllerContext,
  type ComposeController,
  type ComposeRequest,
} from './composeWindow';
import { DRAFTS, INBOX, META, outletFor, renderScreen } from './testing';

const ORIGINAL: MailMessage = {
  uid: 42,
  folder: 'INBOX',
  from: [{ name: 'Luis', email: 'luis@cliente.com' }],
  to: [{ name: 'Ana', email: 'ana@empresa.com' }],
  cc: [],
  bcc: [],
  reply_to: [],
  subject: 'Pedido',
  date: '2026-09-01T10:00:00Z',
  flags: [],
  size: 100,
  has_attachments: false,
  message_id: 'p@cliente.com',
  in_reply_to: [],
  references: [],
  text: 'Necesito el pedido',
  text_truncated: false,
  html: '',
  html_truncated: false,
  remote_images: { present: false, blocked: false },
  attachments: [],
};

const REPLY: ComposeRequest = {
  kind: 'source',
  mode: 'reply',
  folder: 'INBOX',
  uid: 42,
  assistantText: null,
};

function renderInline(controller: ComposeController | null, onClose = vi.fn()) {
  const page = <ComposePage request={REPLY} onClose={onClose} inline />;
  renderScreen(
    controller ? (
      <ComposeControllerContext.Provider value={controller}>
        {page}
      </ComposeControllerContext.Provider>
    ) : (
      page
    ),
    { outlet: outletFor([INBOX, DRAFTS]) },
  );
  return onClose;
}

describe('respuesta dentro del lector', () => {
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
    vi.spyOn(webmailApi, 'signature').mockRejectedValue(new ApiError(503, null));
    vi.spyOn(webmailApi, 'message').mockResolvedValue(ORIGINAL);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('el asunto del hilo queda oculto hasta que se pide editarlo', async () => {
    const user = userEvent.setup();
    renderInline(null);

    await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    expect(screen.queryByLabelText(t('webmail.header.subject'))).toBeNull();
    await user.click(screen.getByRole('button', { name: t('webmail.compose.editSubject') }));
    expect(screen.getByLabelText(t('webmail.header.subject'))).toHaveValue('Re: Pedido');
  });

  it('Ctrl+Enter envia con la cita plegada al final del cuerpo', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const send = vi.spyOn(webmailApi, 'send').mockResolvedValue({
      message_id: 'm@x',
      saved_to_sent: true,
      draft_removed: false,
      replayed: false,
    });
    renderInline(null);

    const body = await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    await user.click(body);
    await user.keyboard('Enviado hoy{Control>}{Enter}{/Control}');
    await vi.advanceTimersByTimeAsync(UNDO_SEND_MS + 100);

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    const payload = send.mock.calls[0]![0];
    expect(payload.subject).toBe('Re: Pedido');
    expect(payload.inReplyTo).toEqual({ folder: 'INBOX', uid: 42 });
    const sent = payload.html ?? '';
    expect(sent.indexOf('Enviado hoy')).toBeGreaterThanOrEqual(0);
    expect(sent.indexOf('Enviado hoy')).toBeLessThan(sent.indexOf('Necesito el pedido'));
  });

  it('abrir en una ventana pasa lo escrito y cierra la respuesta del lector', async () => {
    const user = userEvent.setup();
    const open = vi.fn<ComposeController['open']>().mockReturnValue(true);
    const onClose = renderInline({ open, lastView: () => null });

    await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    await user.click(screen.getByRole('button', { name: t('webmail.composer.popOut') }));

    expect(onClose).toHaveBeenCalledTimes(1);
    const request = open.mock.calls[0]![0];
    expect(request.kind).toBe('resume');
    if (request.kind !== 'resume') return;
    expect(request.mode).toBe('reply');
    expect(request.seed.to).toEqual(['luis@cliente.com']);
    expect(request.seed.inReplyTo).toEqual({ folder: 'INBOX', uid: 42 });
    expect(request.seed.quoted?.text).toContain('> Necesito el pedido');
  });

  it('si la ventana ya tiene otra redaccion, la respuesta sigue en el lector', async () => {
    const user = userEvent.setup();
    const open = vi.fn<ComposeController['open']>().mockReturnValue(false);
    const onClose = renderInline({ open, lastView: () => null });

    await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    await user.click(screen.getByRole('button', { name: t('webmail.composer.popOut') }));
    expect(open).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it('la redaccion retomada en la ventana conserva lo escrito sin volver a pedir el original', async () => {
    const message = vi.spyOn(webmailApi, 'message');
    renderScreen(
      <ComposePage
        request={{
          kind: 'resume',
          mode: 'reply',
          seed: {
            to: ['luis@cliente.com'],
            cc: [],
            bcc: [],
            subject: 'Re: Pedido',
            text: '',
            html: '<p>Lo escrito</p>',
            quoted: {
              text: '> Necesito el pedido',
              html: '<blockquote>Necesito el pedido</blockquote>',
            },
            inReplyTo: { folder: 'INBOX', uid: 42 },
          },
          files: [],
          format: 'html',
          autosavedDraft: false,
          assistantText: null,
        }}
        onClose={vi.fn()}
      />,
      { outlet: outletFor([INBOX, DRAFTS]) },
    );

    const body = await screen.findByRole('textbox', { name: t('webmail.compose.body') });
    expect(body.textContent).toContain('Lo escrito');
    expect(screen.getByLabelText(t('webmail.header.subject'))).toHaveValue('Re: Pedido');
    expect(
      screen.getByRole('button', { name: t('webmail.compose.showQuoted') }),
    ).toBeInTheDocument();
    expect(message).not.toHaveBeenCalled();
  });
});
