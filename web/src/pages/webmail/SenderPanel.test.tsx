import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import {
  webmailApi,
  type Contact,
  type ConversationMessage,
  type MessageEnvelope,
} from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { ConversationPanel } from './ConversationPanel';
import { SenderPanel } from './SenderPanel';
import { CLIENTES, INBOX, META } from './testing';

const SENDER = { name: 'Lucia Mendez', email: 'lucia@cliente.test' };
const SENT = { ...INBOX, name: 'Sent', role: 'sent', unread: 0 };

function page<T>(items: T[], total = items.length) {
  return { items, page: 1, perPage: 50, total, totalPages: 1 };
}

function envelope(uid: number, subject: string): MessageEnvelope {
  return {
    uid,
    from: [SENDER],
    to: [],
    cc: [],
    subject,
    date: '2026-09-01T10:00:00Z',
    flags: ['\\Seen'],
    size: 1,
    has_attachments: false,
  };
}

const CONTACT: Contact = {
  id: 'c1',
  etag: '1',
  updated_at: '2026-09-01T00:00:00Z',
  name: 'Lucia Mendez',
  given_name: 'Lucia',
  family_name: 'Mendez',
  emails: [{ value: 'lucia@cliente.test', type: 'work' }],
  phones: [{ value: '+51 999 000 111', type: 'mobile' }],
  organization: 'Cliente SAC',
  title: 'Gerente',
  notes: '',
  birthday: '',
};

function renderPanel(onClose = vi.fn()) {
  render(
    <MemoryRouter>
      <ToastProvider>
        <SenderPanel
          sender={SENDER}
          internal={false}
          folderName="Clientes"
          uid={9}
          folders={[INBOX, CLIENTES]}
          onClose={onClose}
        />
      </ToastProvider>
    </MemoryRouter>,
  );
  return screen.getByRole('complementary');
}

describe('ficha del remitente', () => {
  beforeEach(() => resetWebmailCatalogs());
  afterEach(() => vi.restoreAllMocks());

  it('muestra su contacto, sus correos anteriores y las reuniones que lo mencionan', async () => {
    vi.spyOn(webmailApi, 'davMeta').mockResolvedValue({ limits: { max_event_window_days: 31 } });
    const contacts = vi.spyOn(webmailApi, 'contacts').mockResolvedValue(page([CONTACT]));
    const messages = vi
      .spyOn(webmailApi, 'messages')
      .mockResolvedValue(page([envelope(4, 'Pedido 12'), envelope(3, 'Pedido 11')], 8));
    const soon = new Date(Date.now() + 2 * 24 * 60 * 60 * 1000).toISOString();
    const later = new Date(Date.now() + 2 * 24 * 60 * 60 * 1000 + 3600_000).toISOString();
    const occurrences = vi.spyOn(webmailApi, 'calendarOccurrences').mockResolvedValue([
      {
        id: 'e1',
        title: 'Revision con Lucia Mendez',
        start: soon,
        end: later,
        location: '',
        all_day: false,
        recurring: false,
      },
      {
        id: 'e2',
        title: 'Comite interno',
        start: soon,
        end: later,
        location: '',
        all_day: false,
        recurring: false,
      },
    ]);
    const panel = renderPanel();

    expect(await within(panel).findByText('Cliente SAC', { exact: false })).toBeInTheDocument();
    expect(within(panel).getByText('+51 999 000 111')).toBeInTheDocument();
    expect(contacts).toHaveBeenCalledWith({ q: 'lucia@cliente.test' }, expect.anything());

    expect(await within(panel).findByText('Pedido 12')).toBeInTheDocument();
    expect(messages).toHaveBeenCalledWith(
      'INBOX',
      { from: 'lucia@cliente.test' },
      expect.anything(),
    );
    expect(
      within(panel).getByRole('link', { name: t('webmail.sender.previousAll', { n: 8 }) }),
    ).toHaveAttribute('href', '/webmail?folder=INBOX&from=lucia%40cliente.test');

    expect(await within(panel).findByText('Revision con Lucia Mendez')).toBeInTheDocument();
    expect(within(panel).queryByText('Comite interno')).toBeNull();
    const [start, end] = occurrences.mock.calls[0] ?? [];
    expect(new Date(end as string).getTime() - new Date(start as string).getTime()).toBe(
      31 * 24 * 60 * 60 * 1000,
    );
  });

  it('sin contacto ofrece anadirlo; sin ventana del calendario no lo consulta; los fallos se explican por seccion', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'davMeta').mockResolvedValue({ limits: {} });
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue(page([]));
    vi.spyOn(webmailApi, 'messages').mockRejectedValue(
      new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: '' }),
    );
    const occurrences = vi.spyOn(webmailApi, 'calendarOccurrences');
    const onClose = vi.fn();
    const panel = renderPanel(onClose);

    expect(await within(panel).findByText(t('webmail.sender.notContact'))).toBeInTheDocument();
    expect(await within(panel).findByText(t('webmail.sender.loadError'))).toBeInTheDocument();
    expect(within(panel).queryByText(t('webmail.sender.meetings'))).toBeNull();
    expect(occurrences).not.toHaveBeenCalled();

    await user.click(within(panel).getByRole('button', { name: t('webmail.sender.addContact') }));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    await user.click(within(panel).getByRole('button', { name: t('common.close') }));
    expect(onClose).toHaveBeenCalled();
  });
});

function conv(
  uid: number,
  folder: string,
  id: string,
  flags: string[] = ['\\Seen'],
): ConversationMessage {
  return { ...envelope(uid, 'Presupuesto'), folder, message_id: id, flags };
}

describe('conversacion abierta', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
  });
  afterEach(() => vi.restoreAllMocks());

  function renderConversation() {
    render(
      <MemoryRouter>
        <ConversationPanel
          folderName="INBOX"
          uid={12}
          folders={[INBOX, SENT]}
          hrefFor={(uid) => `/webmail?folder=INBOX&view=threads&uid=${uid}`}
        />
      </MemoryRouter>,
    );
  }

  it('lista la conversacion con las respuestas propias y marca la abierta', async () => {
    const read = vi
      .spyOn(webmailApi, 'conversation')
      .mockResolvedValue([
        conv(10, 'INBOX', 'a@x'),
        conv(3, 'Sent', 'r@y'),
        conv(12, 'INBOX', 'b@x', []),
      ]);
    renderConversation();
    const nav = await screen.findByRole('navigation', { name: t('webmail.thread.label') });
    const links = within(nav).getAllByRole('link');
    expect(links).toHaveLength(3);
    expect(links[1]).toHaveTextContent(t('webmail.thread.sent'));
    expect(links[1]).toHaveAttribute('href', '/webmail?folder=Sent&uid=3');
    expect(links[2]).toHaveAttribute('aria-current', 'true');
    expect(links[0]).toHaveAttribute('href', '/webmail?folder=INBOX&view=threads&uid=10');
    expect(within(nav).getByText(t('webmail.thread.count', { n: 3 }))).toBeInTheDocument();
    expect(read).toHaveBeenCalledWith('INBOX', 12, expect.anything());
  });

  it('un mensaje suelto no muestra la conversacion', async () => {
    const read = vi.spyOn(webmailApi, 'conversation').mockResolvedValue([conv(12, 'INBOX', 'b@x')]);
    renderConversation();
    await waitFor(() => expect(read).toHaveBeenCalled());
    expect(screen.queryByRole('navigation')).toBeNull();
  });
});
