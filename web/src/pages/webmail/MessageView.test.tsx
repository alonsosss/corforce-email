import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { webmailApi, type MailMessage, type WebmailFolder } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { saveBlob } from '@/lib/download';
import { t } from '@/i18n';
import { MessageView, type MessageViewProps } from './MessageView';

vi.mock('@/lib/download', () => ({ saveBlob: vi.fn() }));

const INBOX: WebmailFolder = {
  name: 'INBOX',
  delimiter: '/',
  role: 'inbox',
  selectable: true,
  total: 1,
  unread: 1,
};
const TRASH: WebmailFolder = { ...INBOX, name: 'Trash', role: 'trash', unread: 0 };

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
  size: 3000,
  has_attachments: true,
  message_id: 'x@cliente.com',
  in_reply_to: [],
  references: [],
  text: 'Hola mundo',
  text_truncated: false,
  html: '<p>Hola mundo</p>',
  html_truncated: false,
  remote_images: { present: true, blocked: true },
  attachments: [
    {
      part: '2',
      filename: 'informe.pdf',
      content_type: 'application/pdf',
      size: 2048,
      content_id: '',
      inline: false,
    },
  ],
};

function renderView(overrides: Partial<MessageViewProps> = {}) {
  const props: MessageViewProps = {
    folderName: 'INBOX',
    folder: INBOX,
    folders: [INBOX, TRASH],
    uid: 5,
    backHref: '/webmail?folder=INBOX',
    onSeen: vi.fn(),
    onFlagsChanged: vi.fn(),
    onGone: vi.fn(),
    ...overrides,
  };
  render(
    <MemoryRouter>
      <ToastProvider>
        <MessageView {...props} />
      </ToastProvider>
    </MemoryRouter>,
  );
  return props;
}

const bodyTitle = t('webmail.reader.bodyTitle', { subject: 'Pedido' });

describe('lectura de un mensaje', () => {
  afterEach(() => vi.restoreAllMocks());

  it('pinta el HTML solo dentro de un iframe aislado, nunca en la pagina', async () => {
    vi.spyOn(webmailApi, 'message').mockResolvedValue(MESSAGE);
    renderView();
    const frame = await screen.findByTitle(bodyTitle);
    const sandbox = frame.getAttribute('sandbox') ?? '';
    expect(sandbox).not.toContain('allow-scripts');
    expect(sandbox).not.toContain('allow-same-origin');
    expect(sandbox).not.toContain('allow-top-navigation');
    expect(frame.getAttribute('srcdoc')).toContain('Hola mundo');
    expect(screen.queryByText('Hola mundo')).toBeNull();
  });

  it('las imagenes remotas se piden por mensaje, sin volver a marcarlo como leido', async () => {
    const user = userEvent.setup();
    const read = vi
      .spyOn(webmailApi, 'message')
      .mockResolvedValueOnce(MESSAGE)
      .mockResolvedValueOnce({
        ...MESSAGE,
        html: '<p>Hola mundo</p><img src="https://img.test/a.png">',
        remote_images: { present: true, blocked: false },
      });
    const props = renderView();

    await screen.findByTitle(bodyTitle);
    expect(screen.getByTitle(bodyTitle).getAttribute('srcdoc')).not.toContain('img.test');
    expect(read).toHaveBeenNthCalledWith(
      1,
      'INBOX',
      5,
      { peek: false, allowRemoteImages: false },
      expect.anything(),
    );
    await waitFor(() => expect(props.onSeen).toHaveBeenCalledWith(5));

    await user.click(screen.getByRole('button', { name: t('webmail.reader.showRemote') }));

    await waitFor(() =>
      expect(read).toHaveBeenNthCalledWith(
        2,
        'INBOX',
        5,
        { peek: true, allowRemoteImages: true },
        expect.anything(),
      ),
    );
    await waitFor(() =>
      expect(screen.getByTitle(bodyTitle).getAttribute('srcdoc')).toContain(
        'https://img.test/a.png',
      ),
    );
    expect(props.onSeen).toHaveBeenCalledTimes(1);
  });

  it('los adjuntos se descargan en memoria como fichero y nunca se enlazan', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'message').mockResolvedValue(MESSAGE);
    const blob = new Blob(['%PDF']);
    const download = vi
      .spyOn(webmailApi, 'downloadPart')
      .mockResolvedValue({ blob, filename: 'informe.pdf', contentType: 'application/pdf' });
    renderView();

    const button = await screen.findByRole('button', {
      name: t('webmail.attachments.downloadName', { name: 'informe.pdf' }),
    });
    expect(document.querySelector('a[href*="/parts/"]')).toBeNull();
    await user.click(button);

    await waitFor(() => expect(saveBlob).toHaveBeenCalledWith(blob, 'informe.pdf'));
    expect(download).toHaveBeenCalledWith('INBOX', 5, '2');
  });

  it('fuera de la papelera, borrar mueve el mensaje a la papelera sin preguntar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'message').mockResolvedValue(MESSAGE);
    const remove = vi.spyOn(webmailApi, 'remove').mockResolvedValue({ permanent: false });
    const props = renderView();

    await user.click(await screen.findByRole('button', { name: t('webmail.reader.delete') }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith('INBOX', 5));
    expect(props.onGone).toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('desde la papelera el borrado es definitivo y exige confirmacion', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'message').mockResolvedValue({ ...MESSAGE, folder: 'Trash' });
    const remove = vi.spyOn(webmailApi, 'remove').mockResolvedValue({ permanent: true });
    const props = renderView({ folderName: 'Trash', folder: TRASH });

    await user.click(
      await screen.findByRole('button', { name: t('webmail.reader.deleteForever') }),
    );
    expect(remove).not.toHaveBeenCalled();
    const dialog = screen.getByRole('dialog');
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.reader.deleteForever') }),
    );

    await waitFor(() => expect(remove).toHaveBeenCalledWith('Trash', 5));
    expect(props.onGone).toHaveBeenCalled();
  });

  it('marcar como no leido y destacar cambian solo los flags que admite el servicio', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'message').mockResolvedValue(MESSAGE);
    const setFlags = vi.spyOn(webmailApi, 'setFlags').mockResolvedValue(null);
    const props = renderView();

    await user.click(await screen.findByRole('button', { name: t('webmail.reader.markUnread') }));
    await waitFor(() => expect(setFlags).toHaveBeenCalledWith('INBOX', 5, { remove: ['\\Seen'] }));
    expect(props.onFlagsChanged).toHaveBeenLastCalledWith(5, []);

    const flag = screen.getByRole('button', { name: t('webmail.reader.flag') });
    expect(flag).toHaveAttribute('aria-pressed', 'false');
    await user.click(flag);
    await waitFor(() => expect(flag).toHaveAttribute('aria-pressed', 'true'));
    expect(setFlags).toHaveBeenLastCalledWith('INBOX', 5, { add: ['\\Flagged'] });
  });
});
