import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import {
  webmailApi,
  webmailRemindersApi,
  type FollowUp,
  type MailMessage,
  type MessageEnvelope,
  type QuickReplyList,
  type SnoozedMessage,
} from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import ComposePage from './ComposePage';
import { NEW_MESSAGE } from './composeWindow';
import MailboxPage from './MailboxPage';
import { MessageView } from './MessageView';
import { QuickRepliesSettings } from './settings/QuickRepliesSettings';
import SnoozedPage from './SnoozedPage';
import { ARCHIVE, DRAFTS, INBOX, META, TRASH, outletFor, renderScreen } from './testing';

const SNOOZED: SnoozedMessage = {
  id: 'r1',
  folder: 'Snoozed',
  uid: 104,
  return_folder: 'INBOX',
  subject: 'Factura de septiembre',
  from: 'cobros@proveedor.com',
  until: new Date(Date.now() + 2 * 86_400_000).toISOString(),
  status: 'pending',
};

const FOLLOW_UP: FollowUp = {
  id: 'f1',
  subject: 'Presupuesto 2027',
  recipients: ['cliente@acme.com'],
  due_at: new Date(Date.now() + 3 * 86_400_000).toISOString(),
  status: 'pending',
};

const QUICK: QuickReplyList = {
  items: [
    {
      id: 'q1',
      name: 'Agradecer',
      html: '<p>Gracias, {nombre}. Saludos de {mi_nombre}.</p>',
      text: 'Gracias, {nombre}. Saludos de {mi_nombre}.',
      updated_at: '2026-09-24T08:00:00Z',
    },
  ],
  limits: { max_items: 2, max_name_chars: 80, max_html_bytes: 16384, max_text_bytes: 16384 },
};

const MESSAGE: MailMessage = {
  uid: 5,
  folder: 'INBOX',
  from: [{ name: 'Luis', email: 'luis@cliente.com' }],
  to: [{ name: '', email: 'ana@empresa.com' }],
  cc: [],
  bcc: [],
  reply_to: [],
  subject: 'Pedido',
  date: '2026-09-01T10:00:00Z',
  flags: [],
  size: 300,
  has_attachments: false,
  message_id: 'x@cliente.com',
  in_reply_to: [],
  references: [],
  text: 'Hola',
  text_truncated: false,
  html: '',
  html_truncated: false,
  remote_images: { present: false, blocked: false },
  attachments: [],
};

function session() {
  useWebmailStore.setState({
    status: 'authenticated',
    session: {
      username: 'ana@empresa.com',
      display_name: 'Ana Perez',
      expires_at: '2026-09-24T20:00:00Z',
      idle_timeout_seconds: 1800,
      quota: null,
    },
  });
}

beforeEach(() => {
  resetWebmailCatalogs();
  vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
});
afterEach(() => vi.restoreAllMocks());

describe('pospuestos y seguimientos', () => {
  it('lista los pospuestos con su hora de vuelta y devuelve uno ya', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailRemindersApi, 'snoozed').mockResolvedValue([SNOOZED]);
    vi.spyOn(webmailRemindersApi, 'followUps').mockResolvedValue([]);
    const unsnooze = vi.spyOn(webmailRemindersApi, 'unsnooze').mockResolvedValue(null);
    const outlet = renderScreen(<SnoozedPage />, { path: '/webmail/snoozed' });

    const link = await screen.findByRole('link', { name: 'Factura de septiembre' });
    expect(link).toHaveAttribute('href', '/webmail?folder=Snoozed&uid=104');
    expect(screen.getByText(/^Vuelve el /)).toBeInTheDocument();
    expect(screen.getByText(t('webmail.followUps.empty'))).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: t('webmail.snoozed.returnNow') }));
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: t('webmail.snoozed.returnNow'),
      }),
    );
    await waitFor(() => expect(unsnooze).toHaveBeenCalledWith('r1'));
    expect(await screen.findByText(t('webmail.snoozed.empty'))).toBeInTheDocument();
    expect(outlet.reloadFolders).toHaveBeenCalled();
  });

  it('cambiar la hora de uno que ya volvio explica el error y relee la lista', async () => {
    const user = userEvent.setup();
    const list = vi
      .spyOn(webmailRemindersApi, 'snoozed')
      .mockResolvedValueOnce([SNOOZED])
      .mockResolvedValue([]);
    vi.spyOn(webmailRemindersApi, 'followUps').mockResolvedValue([]);
    vi.spyOn(webmailRemindersApi, 'reschedule').mockRejectedValue(
      new ApiError(404, { code: 'REMINDER_NOT_FOUND', message: '' }),
    );
    renderScreen(<SnoozedPage />, { path: '/webmail/snoozed' });

    await user.click(await screen.findByRole('button', { name: t('webmail.snoozed.reschedule') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.snoozed.reschedule') }));
    expect(await within(dialog).findByText(t('error.code.REMINDER_NOT_FOUND'))).toBeInTheDocument();
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it('lista los seguimientos pendientes y quita uno', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailRemindersApi, 'snoozed').mockResolvedValue([]);
    vi.spyOn(webmailRemindersApi, 'followUps').mockResolvedValue([FOLLOW_UP]);
    const cancel = vi.spyOn(webmailRemindersApi, 'cancelFollowUp').mockResolvedValue(null);
    renderScreen(<SnoozedPage />, { path: '/webmail/snoozed' });

    expect(await screen.findByText('Presupuesto 2027')).toBeInTheDocument();
    expect(screen.getByText('cliente@acme.com')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('webmail.followUps.cancel') }));
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: t('webmail.followUps.cancel'),
      }),
    );
    await waitFor(() => expect(cancel).toHaveBeenCalledWith('f1'));
    expect(await screen.findByText(t('webmail.followUps.empty'))).toBeInTheDocument();
  });
});

describe('posponer desde el lector y en lote', () => {
  it('el lector pospone el mensaje abierto con un atajo y lo da por ido', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'message').mockResolvedValue(MESSAGE);
    const snooze = vi
      .spyOn(webmailRemindersApi, 'snooze')
      .mockResolvedValue({ snoozed: [SNOOZED], failed: [] });
    const onGone = vi.fn();
    render(
      <MemoryRouter>
        <ToastProvider>
          <MessageView
            folderName="INBOX"
            folder={INBOX}
            folders={[INBOX, TRASH]}
            uid={5}
            backHref="/webmail"
            onSeen={vi.fn()}
            onFlagsChanged={vi.fn()}
            onGone={onGone}
          />
        </ToastProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole('button', { name: t('webmail.snooze.action') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: /^Mañana/ }));
    await user.click(within(dialog).getByRole('button', { name: t('webmail.snooze.confirm') }));
    await waitFor(() => expect(snooze).toHaveBeenCalledWith('INBOX', [5], expect.any(String)));
    const until = new Date(snooze.mock.calls[0]![2]);
    expect(until.getHours()).toBe(8);
    expect(onGone).toHaveBeenCalled();
  });

  it('el lector no ofrece posponer en Borradores', async () => {
    vi.spyOn(webmailApi, 'message').mockResolvedValue(MESSAGE);
    render(
      <MemoryRouter>
        <ToastProvider>
          <MessageView
            folderName="Drafts"
            folder={DRAFTS}
            folders={[INBOX, DRAFTS]}
            uid={5}
            backHref="/webmail"
            onSeen={vi.fn()}
            onFlagsChanged={vi.fn()}
            onGone={vi.fn()}
          />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByRole('heading', { name: 'Pedido' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('webmail.snooze.action') })).toBeNull();
  });

  it('la barra de seleccion pospone los marcados en tandas del tope servido', async () => {
    const user = userEvent.setup();
    const envelope = (uid: number): MessageEnvelope => ({
      uid,
      from: [{ name: '', email: `r${uid}@x.com` }],
      to: [],
      cc: [],
      subject: `Asunto ${uid}`,
      date: '2026-09-20T10:00:00Z',
      flags: [],
      size: 10,
      has_attachments: false,
    });
    vi.spyOn(webmailApi, 'messages').mockResolvedValue({
      items: [envelope(1), envelope(2), envelope(3)],
      page: 1,
      perPage: 50,
      total: 3,
      totalPages: 1,
    });
    const snooze = vi
      .spyOn(webmailRemindersApi, 'snooze')
      .mockImplementation(async (_folder, uids) => ({
        snoozed: uids.map((uid) => ({ ...SNOOZED, id: `r${uid}`, uid })),
        failed: [],
      }));
    renderScreen(<MailboxPage />, {
      url: '/webmail?folder=INBOX',
      outlet: outletFor([INBOX, TRASH, ARCHIVE]),
    });

    await user.click(await screen.findByLabelText(t('webmail.batch.selectPage')));
    const bar = screen.getByRole('toolbar', { name: t('webmail.batch.actions') });
    await user.click(within(bar).getByRole('button', { name: t('webmail.snooze.action') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.snooze.confirm') }));
    await waitFor(() => expect(snooze).toHaveBeenCalledTimes(2));
    expect(snooze).toHaveBeenNthCalledWith(1, 'INBOX', [1, 2], expect.any(String));
    expect(snooze).toHaveBeenNthCalledWith(2, 'INBOX', [3], expect.any(String));
  });
});

describe('redaccion: seguimiento y respuestas rapidas', () => {
  beforeEach(() => {
    session();
    vi.spyOn(webmailApi, 'identities').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'addressBook').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 0,
      total: 0,
      totalPages: 0,
    });
    vi.spyOn(webmailApi, 'session').mockRejectedValue(new ApiError(503, null));
    vi.spyOn(webmailApi, 'signature').mockResolvedValue({
      enabled: false,
      html: '',
      text: '',
      on_replies: false,
      updated_at: null,
      limits: { max_html_bytes: 8192, max_text_bytes: 4096 },
    });
  });

  it('programar con aviso si no responden manda follow_up_days y avisa si no se registro', async () => {
    const user = userEvent.setup();
    const schedule = vi.spyOn(webmailApi, 'schedule').mockResolvedValue({
      id: 's1',
      send_at: new Date(Date.now() + 86_400_000).toISOString(),
      follow_up_error: 'REMINDER_LIMIT',
    });
    renderScreen(<ComposePage request={NEW_MESSAGE} onClose={vi.fn()} />, {
      path: '/webmail/compose',
      outlet: outletFor([INBOX, DRAFTS]),
    });

    await user.type(
      await screen.findByLabelText(t('webmail.header.to')),
      'luis@cliente.com{Enter}',
    );
    await user.selectOptions(screen.getByLabelText(t('webmail.followUp.label')), '3');
    await user.click(screen.getByRole('button', { name: t('webmail.compose.sendLater') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.schedule.confirm') }));
    await waitFor(() => expect(schedule).toHaveBeenCalled());
    expect(schedule.mock.calls[0]![2]).toMatchObject({ followUpDays: 3 });
    expect(await screen.findByText(t('webmail.followUp.failed'))).toBeInTheDocument();
  });

  it('inserta una respuesta rapida con las variables resueltas', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailRemindersApi, 'quickReplies').mockResolvedValue(QUICK);
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderScreen(<ComposePage request={NEW_MESSAGE} onClose={vi.fn()} />, {
      path: '/webmail/compose',
      outlet: outletFor([INBOX, DRAFTS]),
    });

    await user.type(
      await screen.findByLabelText(t('webmail.header.to')),
      'luis.gomez@cliente.com{Enter}',
    );
    await user.click(screen.getByRole('button', { name: t('webmail.quickReplies.insert') }));
    await user.click(await screen.findByRole('button', { name: /Agradecer/ }));
    await user.click(screen.getByRole('button', { name: t('webmail.compose.saveDraft') }));
    await waitFor(() => expect(saveDraft).toHaveBeenCalled());
    const html = saveDraft.mock.calls[0]![0].html ?? '';
    expect(html).toContain('Gracias, Luis Gomez. Saludos de Ana Perez.');
    expect(html).not.toContain('{nombre}');
  });
});

describe('ajustes: respuestas rapidas', () => {
  function renderSettings() {
    render(
      <MemoryRouter>
        <ToastProvider>
          <QuickRepliesSettings />
        </ToastProvider>
      </MemoryRouter>,
    );
  }

  it('crea una respuesta y valida el nombre antes de enviarla', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailRemindersApi, 'quickReplies').mockResolvedValue({ ...QUICK, items: [] });
    const create = vi
      .spyOn(webmailRemindersApi, 'createQuickReply')
      .mockImplementation(async (input) => ({
        id: 'q9',
        name: input.name,
        html: input.html,
        text: 'Hola',
        updated_at: '2026-09-24T09:00:00Z',
      }));
    renderSettings();

    await user.click(await screen.findByRole('button', { name: t('webmail.quickReplies.new') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    expect(within(dialog).getByText(t('webmail.quickReplies.nameRequired'))).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();
    expect(within(dialog).getByText('{nombre}')).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText(t('webmail.quickReplies.name')), 'Saludo');
    const editor = within(dialog).getByRole('textbox', { name: t('webmail.quickReplies.body') });
    await user.click(editor);
    await user.keyboard('Hola {{nombre}');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect(create.mock.calls[0]![0].name).toBe('Saludo');
    expect(create.mock.calls[0]![0].html).toContain('Hola {nombre}');
    expect(await screen.findByText('Saludo')).toBeInTheDocument();
  });

  it('muestra un nombre repetido y borra una respuesta', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailRemindersApi, 'quickReplies').mockResolvedValue(QUICK);
    vi.spyOn(webmailRemindersApi, 'updateQuickReply').mockRejectedValue(
      new ApiError(409, { code: 'QUICK_REPLY_EXISTS', message: '' }),
    );
    const remove = vi.spyOn(webmailRemindersApi, 'deleteQuickReply').mockResolvedValue(null);
    renderSettings();

    await user.click(
      await screen.findByRole('button', {
        name: t('webmail.quickReplies.edit', { name: 'Agradecer' }),
      }),
    );
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    expect(await within(dialog).findByText(t('error.code.QUICK_REPLY_EXISTS'))).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: t('common.cancel') }));

    await user.click(
      screen.getByRole('button', { name: t('webmail.quickReplies.delete', { name: 'Agradecer' }) }),
    );
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: t('common.delete') }),
    );
    await waitFor(() => expect(remove).toHaveBeenCalledWith('q1'));
    expect(await screen.findByText(t('webmail.quickReplies.empty'))).toBeInTheDocument();
  });
});
