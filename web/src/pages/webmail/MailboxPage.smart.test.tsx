import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type MessageEnvelope } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import MailboxPage from './MailboxPage';
import { ARCHIVE, INBOX, JUNK, META, TRASH, outletFor, renderScreen } from './testing';

const FOLDERS = [INBOX, TRASH, JUNK, ARCHIVE];

function envelope(uid: number, overrides: Partial<MessageEnvelope> = {}): MessageEnvelope {
  return {
    uid,
    from: [{ name: `Remitente ${uid}`, email: `r${uid}@cliente.com` }],
    to: [{ name: '', email: 'ana@empresa.com' }],
    cc: [],
    subject: `Asunto ${uid}`,
    date: '2026-09-20T10:00:00Z',
    flags: ['\\Seen'],
    size: 100,
    has_attachments: false,
    category: 'primary',
    ...overrides,
  };
}

function page(items: MessageEnvelope[]) {
  return { items, page: 1, perPage: 50, total: items.length, totalPages: 1 };
}

const location = () => screen.getByTestId('location').textContent ?? '';

describe('buzon: bandeja inteligente y conversaciones', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
    vi.spyOn(webmailApi, 'senderInsight').mockRejectedValue(
      new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: '' }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it('la bandeja abre en Principal y cada pestana pide su categoria', async () => {
    const user = userEvent.setup();
    const messages = vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([envelope(1)]));
    renderScreen(<MailboxPage />, { url: '/webmail?folder=INBOX', outlet: outletFor(FOLDERS) });

    const tabs = await screen.findByRole('tablist', { name: t('webmail.tabs.label') });
    expect(
      within(tabs)
        .getAllByRole('tab')
        .map((tab) => tab.textContent),
    ).toEqual([
      t('webmail.tabs.primary'),
      t('webmail.tabs.notifications'),
      t('webmail.tabs.newsletters'),
      t('webmail.tabs.all'),
    ]);
    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'INBOX',
        expect.objectContaining({ category: 'primary', view: undefined }),
        expect.anything(),
      ),
    );

    // La bandeja espera a la meta: el listado no se pide antes sin pestana.
    expect(messages.mock.calls[0]?.[1]).toMatchObject({ category: 'primary' });

    await user.click(within(tabs).getByRole('tab', { name: t('webmail.tabs.newsletters') }));
    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'INBOX',
        expect.objectContaining({ category: 'newsletters' }),
        expect.anything(),
      ),
    );
    expect(location()).toContain('tab=newsletters');

    await user.click(within(tabs).getByRole('tab', { name: t('webmail.tabs.all') }));
    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'INBOX',
        expect.objectContaining({ category: undefined }),
        expect.anything(),
      ),
    );
  });

  it('una busqueda recorre todas las pestanas y fuera de la bandeja no hay pestanas', async () => {
    const messages = vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([]));
    renderScreen(<MailboxPage />, {
      url: '/webmail?folder=INBOX&q=factura&tab=newsletters',
      outlet: outletFor(FOLDERS),
    });
    expect(await screen.findByText(t('webmail.tabs.searchAll'))).toBeInTheDocument();
    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'INBOX',
        expect.objectContaining({ search: 'factura', category: undefined }),
        expect.anything(),
      ),
    );
  });

  it('fuera de la bandeja de entrada no hay pestanas ni filtro', async () => {
    const messages = vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([]));
    renderScreen(<MailboxPage />, { url: '/webmail?folder=Archive', outlet: outletFor(FOLDERS) });
    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'Archive',
        expect.objectContaining({ category: undefined }),
        expect.anything(),
      ),
    );
    expect(screen.queryByRole('tablist')).toBeNull();
  });

  it('la vista por conversaciones agrupa y una accion alcanza todos los mensajes de la conversacion', async () => {
    const user = userEvent.setup();
    const messages = vi.spyOn(webmailApi, 'messages').mockResolvedValue(
      page([
        envelope(9, {
          flags: [],
          thread: {
            size: 3,
            unread: 2,
            uids: [9, 4, 2],
            participants: [
              { name: 'Lucia', email: 'lucia@cliente.com' },
              { name: 'Pedro', email: 'pedro@cliente.com' },
            ],
          },
        }),
      ]),
    );
    const batch = vi
      .spyOn(webmailApi, 'batch')
      .mockImplementation(async (_folder, uids) => ({ affected: uids.length, permanent: false }));
    const outlet = renderScreen(<MailboxPage />, {
      url: '/webmail?folder=INBOX',
      outlet: outletFor(FOLDERS),
    });

    await user.click(await screen.findByRole('button', { name: t('webmail.view.threads') }));
    expect(location()).toContain('view=threads');
    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'INBOX',
        expect.objectContaining({ view: 'threads' }),
        expect.anything(),
      ),
    );
    expect(await screen.findByText('Lucia, Pedro')).toBeInTheDocument();
    expect(screen.getByText(t('webmail.thread.count', { n: 3 }))).toBeInTheDocument();

    await user.click(screen.getByLabelText(t('webmail.batch.check', { subject: 'Asunto 9' })));
    const bar = screen.getByRole('toolbar', { name: t('webmail.batch.actions') });
    await user.click(within(bar).getByRole('button', { name: t('webmail.batch.markRead') }));
    // META.limits.max_batch_uids es 2: tres UIDs van en dos tandas.
    await waitFor(() => expect(batch).toHaveBeenCalledTimes(2));
    expect(batch.mock.calls.flatMap((call) => call[1])).toEqual([9, 4, 2]);
    await waitFor(() => expect(outlet.reloadFolders).toHaveBeenCalled());

    await user.click(screen.getByRole('button', { name: t('webmail.view.messages') }));
    expect(location()).not.toContain('view=threads');
  });

  it('al abrir un mensaje en la vista por conversaciones se ve la conversacion', async () => {
    vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([envelope(9)]));
    vi.spyOn(webmailApi, 'message').mockRejectedValue(
      new ApiError(404, { code: 'MESSAGE_NOT_FOUND', message: '' }),
    );
    const conversation = vi.spyOn(webmailApi, 'conversation').mockResolvedValue([
      { ...envelope(4), folder: 'INBOX', message_id: 'a@x' },
      { ...envelope(9), folder: 'INBOX', message_id: 'b@x' },
    ]);
    renderScreen(<MailboxPage />, {
      url: '/webmail?folder=INBOX&view=threads&uid=9',
      outlet: outletFor(FOLDERS),
    });
    const nav = await screen.findByRole('navigation', { name: t('webmail.thread.label') });
    expect(within(nav).getAllByRole('link')[0]).toHaveAttribute(
      'href',
      '/webmail?folder=INBOX&uid=4&view=threads',
    );
    expect(conversation).toHaveBeenCalledWith('INBOX', 9, expect.anything());
  });
});
