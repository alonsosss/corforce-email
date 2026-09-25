import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation, type InitialEntry } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import { webmailApi, type MailMessage, type Signature } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import { assistantNavigationState } from './assistant/assistant';
import ComposeRoute from './ComposeWindow';
import MailboxPage from './MailboxPage';
import { WebmailShell } from './WebmailShell';
import { DRAFTS, INBOX, META } from './testing';

vi.mock('@/webmail/events', () => ({ watchInbox: () => () => undefined }));

const SIGNATURE: Signature = {
  enabled: false,
  html: '',
  text: '',
  on_replies: false,
  updated_at: null,
  limits: { max_html_bytes: 8192, max_text_bytes: 4096 },
};

const ORIGINAL: MailMessage = {
  uid: 7,
  folder: 'INBOX',
  from: [{ name: 'Luis', email: 'luis@cliente.com' }],
  to: [{ name: 'Ana', email: 'ana@empresa.com' }],
  cc: [],
  bcc: [],
  reply_to: [],
  subject: 'Pedido de marzo',
  date: '2026-09-01T10:00:00Z',
  flags: ['\\Seen'],
  size: 100,
  has_attachments: false,
  message_id: 'x@cliente.com',
  in_reply_to: [],
  references: [],
  text: 'Hola Ana',
  text_truncated: false,
  html: '',
  html_truncated: false,
  remote_images: { present: false, blocked: false },
  attachments: [],
};

function Where() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function renderApp(entry: InitialEntry = '/webmail/compose') {
  render(
    <ToastProvider>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/webmail" element={<WebmailShell />}>
            <Route index element={<MailboxPage />} />
            <Route path="compose" element={<ComposeRoute />} />
            <Route path="contacts" element={<p>pantalla de contactos</p>} />
          </Route>
        </Routes>
        <Where />
      </MemoryRouter>
    </ToastProvider>,
  );
}

const composer = () => screen.getByRole('region', { name: t('webmail.composer.label') });
const location = () => screen.getByTestId('location').textContent;

describe('ventana flotante de redaccion', () => {
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
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX, DRAFTS]);
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
    vi.spyOn(webmailApi, 'identities').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    vi.spyOn(webmailApi, 'addressBook').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'session').mockRejectedValue(new ApiError(503, null));
    vi.spyOn(webmailApi, 'messages').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 50,
      total: 0,
      totalPages: 0,
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it('la ruta abre la ventana en el marco y deja debajo el buzon', async () => {
    const user = userEvent.setup();
    renderApp();

    const subject = await screen.findByLabelText(t('webmail.header.subject'));
    await waitFor(() => expect(location()).toBe('/webmail'));
    expect(
      await screen.findByRole('heading', { name: t('webmail.role.inbox') }),
    ).toBeInTheDocument();
    const region = composer();

    await user.click(screen.getByRole('button', { name: t('webmail.composer.minimize') }));
    expect(region).toHaveClass('cf-wm-composer--minimized');
    await user.click(screen.getByRole('button', { name: t('webmail.composer.restore') }));
    expect(region).toHaveClass('cf-wm-composer--normal');
    expect(subject).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: t('webmail.composer.expand') }));
    expect(region).toHaveClass('cf-wm-composer--expanded');
    await user.click(screen.getByRole('button', { name: t('webmail.composer.collapse') }));
    expect(region).toHaveClass('cf-wm-composer--normal');
  });

  it('sigue abierta al cambiar de carpeta y al pasar a contactos', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderApp('/webmail');

    await user.click(await screen.findByRole('button', { name: t('webmail.compose.new') }));
    await user.type(await screen.findByLabelText(t('webmail.header.subject')), 'Pedido');

    await user.click(await screen.findByRole('link', { name: /Borradores/ }));
    await waitFor(() => expect(location()).toBe('/webmail?folder=Drafts'));
    expect(screen.getByLabelText(t('webmail.header.subject'))).toHaveValue('Pedido');

    const apps = screen.getByRole('navigation', { name: t('webmail.apps.label') });
    await user.click(within(apps).getByRole('link', { name: t('webmail.apps.contacts') }));
    expect(await screen.findByText('pantalla de contactos')).toBeInTheDocument();
    expect(screen.getByLabelText(t('webmail.header.subject'))).toHaveValue('Pedido');
    expect(saveDraft).not.toHaveBeenCalled();
  });

  it('cerrarla con algo escrito lo guarda como borrador', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderApp();

    await user.type(
      await screen.findByLabelText(t('webmail.header.to')),
      'luis@cliente.com{Enter}',
    );
    await user.type(screen.getByLabelText(t('webmail.header.subject')), 'Pedido');
    await user.click(screen.getByRole('button', { name: t('webmail.composer.close') }));

    await waitFor(() => expect(saveDraft).toHaveBeenCalledTimes(1));
    expect(saveDraft.mock.calls[0]?.[0]).toMatchObject({
      to: ['luis@cliente.com'],
      subject: 'Pedido',
    });
    expect(await screen.findByText(t('webmail.composer.savedOnLeave'))).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: t('webmail.composer.label') })).toBeNull();
  });

  it('con la sesion caducada cerrarla no intenta guardar', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderApp();

    await user.type(await screen.findByLabelText(t('webmail.header.subject')), 'Pedido');
    act(() => useWebmailStore.setState({ status: 'anonymous' }));
    await user.click(screen.getByRole('button', { name: t('webmail.composer.close') }));

    expect(screen.queryByRole('region', { name: t('webmail.composer.label') })).toBeNull();
    expect(saveDraft).not.toHaveBeenCalled();
  });

  it('descartar no deja borrador y cierra la ventana', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderApp();

    await user.type(await screen.findByLabelText(t('webmail.header.subject')), 'Pedido');
    await user.click(screen.getByRole('button', { name: t('webmail.compose.discard') }));
    const dialog = await screen.findByRole('dialog', { name: t('webmail.compose.discardTitle') });
    await user.click(within(dialog).getByRole('button', { name: t('webmail.compose.discard') }));

    await waitFor(() =>
      expect(screen.queryByRole('region', { name: t('webmail.composer.label') })).toBeNull(),
    );
    expect(saveDraft).not.toHaveBeenCalled();
  });

  it('sin nada escrito se cierra sin guardar', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderApp();

    await screen.findByLabelText(t('webmail.header.subject'));
    await user.click(screen.getByRole('button', { name: t('webmail.composer.close') }));

    expect(screen.queryByRole('region', { name: t('webmail.composer.label') })).toBeNull();
    expect(saveDraft).not.toHaveBeenCalled();
  });

  it('una segunda redaccion trae al frente la que esta en curso y lo explica', async () => {
    const user = userEvent.setup();
    renderApp('/webmail');

    await user.click(await screen.findByRole('button', { name: t('webmail.compose.new') }));
    await user.type(await screen.findByLabelText(t('webmail.header.subject')), 'Pedido');
    await user.click(screen.getByRole('button', { name: t('webmail.composer.minimize') }));
    expect(composer()).toHaveClass('cf-wm-composer--minimized');

    await user.click(screen.getByRole('button', { name: t('webmail.compose.new') }));
    expect(await screen.findByText(t('webmail.composer.oneAtATime'))).toBeInTheDocument();
    expect(screen.getAllByRole('region', { name: t('webmail.composer.label') })).toHaveLength(1);
    expect(composer()).toHaveClass('cf-wm-composer--normal');
    expect(screen.getByLabelText(t('webmail.header.subject'))).toHaveValue('Pedido');
  });

  it('una redaccion intacta se sustituye por la nueva sin aviso', async () => {
    const user = userEvent.setup();
    renderApp('/webmail/compose?to=luis%40cliente.com');

    expect(await screen.findByText('luis@cliente.com')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('webmail.compose.new') }));

    await waitFor(() => expect(screen.queryByText('luis@cliente.com')).toBeNull());
    expect(screen.getAllByRole('region', { name: t('webmail.composer.label') })).toHaveLength(1);
    expect(screen.queryByText(t('webmail.composer.oneAtATime'))).toBeNull();
  });

  it('responder deja debajo el mensaje de origen e inserta la propuesta del asistente', async () => {
    vi.spyOn(webmailApi, 'message').mockResolvedValue(ORIGINAL);
    renderApp({
      pathname: '/webmail/compose',
      search: '?mode=reply&folder=INBOX&uid=7',
      state: assistantNavigationState('Confirmado para el lunes.'),
    });

    await waitFor(() =>
      expect(
        screen.getByRole('textbox', { name: t('webmail.compose.body') }).textContent,
      ).toContain('Confirmado para el lunes.'),
    );
    await waitFor(() => expect(location()).toBe('/webmail?folder=INBOX&uid=7'));
    const subject = within(composer()).getByLabelText<HTMLInputElement>(
      t('webmail.header.subject'),
    );
    expect(subject.value).toContain('Pedido de marzo');
  });

  it('un enlace de redaccion no valido no abre la ventana y lo explica', async () => {
    renderApp('/webmail/compose?mode=inventado&folder=INBOX&uid=7');

    expect(await screen.findByText(t('webmail.compose.invalidLink'))).toBeInTheDocument();
    await waitFor(() => expect(location()).toBe('/webmail'));
    expect(screen.queryByRole('region', { name: t('webmail.composer.label') })).toBeNull();
  });

  it('los atajos no actuan con el foco en la ventana ni con ella ampliada', async () => {
    const user = userEvent.setup();
    const batch = vi
      .spyOn(webmailApi, 'batch')
      .mockResolvedValue({ affected: 1, permanent: false });
    renderApp('/webmail');

    await user.click(await screen.findByRole('button', { name: t('webmail.compose.new') }));
    const minimize = await screen.findByRole('button', { name: t('webmail.composer.minimize') });
    minimize.focus();
    fireEvent.keyDown(minimize, { key: 'c' });
    fireEvent.keyDown(minimize, { key: '?' });
    fireEvent.keyDown(minimize, { key: '#' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByText(t('webmail.composer.oneAtATime'))).toBeNull();

    await user.click(screen.getByRole('button', { name: t('webmail.composer.expand') }));
    act(() => (document.activeElement as HTMLElement | null)?.blur());
    fireEvent.keyDown(document.body, { key: '?' });
    expect(screen.queryByRole('dialog')).toBeNull();

    await user.click(screen.getByRole('button', { name: t('webmail.composer.collapse') }));
    act(() => (document.activeElement as HTMLElement | null)?.blur());
    fireEvent.keyDown(document.body, { key: '?' });
    expect(
      await screen.findByRole('dialog', { name: t('webmail.shortcuts.title') }),
    ).toBeInTheDocument();
    expect(batch).not.toHaveBeenCalled();
  });

  it('el destinatario de ?to= no filtra el buzon de fondo', async () => {
    const messages = vi.mocked(webmailApi.messages);
    renderApp('/webmail/compose?to=luis%40cliente.com');
    await screen.findByLabelText(t('webmail.header.subject'));
    await waitFor(() => expect(location()).toBe('/webmail'));
    await waitFor(() => expect(messages).toHaveBeenCalled());
    for (const call of messages.mock.calls) {
      expect(call[1]).not.toHaveProperty('to', 'luis@cliente.com');
    }
  });
});
