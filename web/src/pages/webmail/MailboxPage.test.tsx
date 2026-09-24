import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type MessageEnvelope } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import MailboxPage from './MailboxPage';
import { SearchBar } from './SearchBar';
import { ARCHIVE, INBOX, JUNK, META, TRASH, outletFor, renderScreen } from './testing';

function envelope(uid: number, overrides: Partial<MessageEnvelope> = {}): MessageEnvelope {
  return {
    uid,
    from: [{ name: `Remitente ${uid}`, email: `r${uid}@cliente.com` }],
    to: [{ name: '', email: 'ana@empresa.com' }],
    cc: [],
    subject: `Asunto ${uid}`,
    date: '2026-09-20T10:00:00Z',
    flags: [],
    size: 100,
    has_attachments: false,
    ...overrides,
  };
}

function page(items: MessageEnvelope[]) {
  return { items, page: 1, perPage: 50, total: items.length, totalPages: 1 };
}

const FOLDERS = [INBOX, TRASH, JUNK, ARCHIVE];

/** El buscador vive en la barra superior del marco: se monta junto al buzon, como alli. */
function MailboxWithSearch() {
  return (
    <>
      <SearchBar folder="INBOX" />
      <MailboxPage />
    </>
  );
}

describe('buzon: varios mensajes, vaciar, busqueda avanzada y atajos', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
  });
  afterEach(() => vi.restoreAllMocks());

  it('marca toda la pagina y la marca como leida en tandas del tope servido', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'messages').mockResolvedValue(
      page([envelope(1), envelope(2), envelope(3)]),
    );
    const batch = vi
      .spyOn(webmailApi, 'batch')
      .mockImplementation(async (_folder, uids) => ({ affected: uids.length, permanent: false }));
    const outlet = renderScreen(<MailboxPage />, {
      url: '/webmail?folder=INBOX',
      outlet: outletFor(FOLDERS),
    });

    await user.click(await screen.findByLabelText(t('webmail.batch.selectPage')));
    const bar = screen.getByRole('toolbar', { name: t('webmail.batch.actions') });
    await user.click(within(bar).getByRole('button', { name: t('webmail.batch.markRead') }));

    await waitFor(() => expect(batch).toHaveBeenCalledTimes(2));
    expect(batch).toHaveBeenNthCalledWith(1, 'INBOX', [1, 2], {
      action: 'flags',
      add: ['\\Seen'],
      remove: undefined,
    });
    expect(batch).toHaveBeenNthCalledWith(
      2,
      'INBOX',
      [3],
      expect.objectContaining({ action: 'flags' }),
    );
    expect(outlet.adjustUnread).toHaveBeenCalledWith('INBOX', -3);
  });

  it('si la accion sobre varios falla, el error queda en la barra', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([envelope(1)]));
    vi.spyOn(webmailApi, 'batch').mockRejectedValue(
      new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: '' }),
    );
    renderScreen(<MailboxPage />, { url: '/webmail?folder=INBOX', outlet: outletFor(FOLDERS) });

    await user.click(
      await screen.findByLabelText(t('webmail.batch.check', { subject: 'Asunto 1' })),
    );
    await user.click(screen.getByRole('button', { name: t('webmail.batch.reportSpam') }));
    expect(await screen.findByText(t('error.serviceUnavailable'))).toBeInTheDocument();
  });

  it('vacia la papelera tras confirmar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([envelope(4)]));
    const empty = vi.spyOn(webmailApi, 'emptyFolder').mockResolvedValue({ removed: 4 });
    renderScreen(<MailboxPage />, { url: '/webmail?folder=Trash', outlet: outletFor(FOLDERS) });

    await user.click(await screen.findByRole('button', { name: t('webmail.empty.action') }));
    const dialog = screen.getByRole('dialog');
    expect(empty).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: t('webmail.empty.action') }));

    await waitFor(() => expect(empty).toHaveBeenCalledWith('Trash'));
    expect(await screen.findByText(t('webmail.empty.done', { n: 4 }))).toBeInTheDocument();
  });

  it('un vaciado rechazado se explica dentro del dialogo', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([envelope(4)]));
    vi.spyOn(webmailApi, 'emptyFolder').mockRejectedValue(
      new ApiError(409, { code: 'FOLDER_NOT_EMPTIABLE', message: '' }),
    );
    renderScreen(<MailboxPage />, { url: '/webmail?folder=Junk', outlet: outletFor(FOLDERS) });

    await user.click(await screen.findByRole('button', { name: t('webmail.empty.action') }));
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: t('webmail.empty.action') }),
    );
    expect(await screen.findByText(t('error.code.FOLDER_NOT_EMPTIABLE'))).toBeInTheDocument();
  });

  it('la busqueda avanzada viaja en la URL y llega al API', async () => {
    const user = userEvent.setup();
    const messages = vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([]));
    renderScreen(<MailboxWithSearch />, {
      url: '/webmail?folder=INBOX',
      outlet: outletFor(FOLDERS),
    });

    await user.click(await screen.findByRole('button', { name: t('webmail.search.advanced') }));
    await user.type(screen.getByLabelText(t('webmail.search.from')), 'luis');
    await user.click(screen.getByLabelText(t('webmail.search.unread')));
    await user.click(
      within(document.getElementById('wm-search-advanced') as HTMLElement).getByRole('button', {
        name: t('common.search'),
      }),
    );

    await waitFor(() =>
      expect(messages).toHaveBeenLastCalledWith(
        'INBOX',
        expect.objectContaining({ from: 'luis', unread: true }),
        expect.anything(),
      ),
    );
    expect(screen.getByTestId('location').textContent).toContain('from=luis');
    expect(screen.getByTestId('location').textContent).toContain('unread=1');
  });

  it('un rango de fechas invertido no busca y se explica junto al campo', async () => {
    const user = userEvent.setup();
    const messages = vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([]));
    renderScreen(<MailboxWithSearch />, {
      url: '/webmail?folder=INBOX',
      outlet: outletFor(FOLDERS),
    });

    await user.click(await screen.findByRole('button', { name: t('webmail.search.advanced') }));
    fireEvent.change(screen.getByLabelText(t('webmail.search.since')), {
      target: { value: '2026-09-10' },
    });
    fireEvent.change(screen.getByLabelText(t('webmail.search.before')), {
      target: { value: '2026-09-01' },
    });
    const calls = messages.mock.calls.length;
    await user.click(
      within(document.getElementById('wm-search-advanced') as HTMLElement).getByRole('button', {
        name: t('common.search'),
      }),
    );
    expect(await screen.findByText(t('webmail.search.badRange'))).toBeInTheDocument();
    expect(messages.mock.calls.length).toBe(calls);
  });

  it('los atajos actuan sobre los marcados y no se disparan al escribir en la busqueda', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'messages').mockResolvedValue(page([envelope(7), envelope(8)]));
    const batch = vi
      .spyOn(webmailApi, 'batch')
      .mockResolvedValue({ affected: 1, permanent: false });
    renderScreen(<MailboxWithSearch />, {
      url: '/webmail?folder=INBOX',
      outlet: outletFor(FOLDERS),
    });

    const search = await screen.findByLabelText(t('webmail.list.search'));
    await user.type(search, '#je');
    expect(batch).not.toHaveBeenCalled();
    expect(screen.getByTestId('location').textContent).not.toContain('uid=');

    await user.click(screen.getByLabelText(t('webmail.batch.check', { subject: 'Asunto 8' })));
    fireEvent.keyDown(document.body, { key: '#' });
    await waitFor(() => expect(batch).toHaveBeenCalledWith('INBOX', [8], { action: 'delete' }));

    fireEvent.keyDown(document.body, { key: 'j' });
    await waitFor(() => expect(screen.getByTestId('location').textContent).toContain('uid=7'));
  });
});
