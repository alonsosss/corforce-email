import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { webmailApi } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import type { InboxWatchHandlers } from '@/webmail/events';
import { readNotifyPreference, writeNotifyPreference } from '@/webmail/notifications';
import { useWebmailStore } from '@/webmail/store';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { WebmailShell } from './WebmailShell';
import { INBOX, META } from './testing';

const watchers: InboxWatchHandlers[] = [];
vi.mock('@/webmail/events', () => ({
  watchInbox: (handlers: InboxWatchHandlers) => {
    watchers.push(handlers);
    return () => undefined;
  },
}));

function Where() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function renderShell() {
  render(
    <ToastProvider>
      <MemoryRouter initialEntries={['/webmail']}>
        <Routes>
          <Route path="/webmail" element={<WebmailShell />}>
            <Route index element={<p>bandeja</p>} />
            <Route path="*" element={<p>otra</p>} />
          </Route>
        </Routes>
        <Where />
      </MemoryRouter>
    </ToastProvider>,
  );
}

class FakeNotification {
  static permission: NotificationPermission = 'default';
  static requestPermission = vi.fn(async () => FakeNotification.permission);
  static created: { title: string; body?: string }[] = [];
  onclick: (() => void) | null = null;
  constructor(title: string, options?: NotificationOptions) {
    FakeNotification.created.push({ title, body: options?.body });
  }
  close() {}
}

describe('marco del webmail', () => {
  beforeEach(() => {
    watchers.length = 0;
    FakeNotification.created = [];
    FakeNotification.permission = 'default';
    vi.stubGlobal('Notification', FakeNotification);
    writeNotifyPreference(false);
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
    useWebmailStore.setState({
      status: 'authenticated',
      session: {
        username: 'ana@empresa.com',
        display_name: 'Ana',
        expires_at: '',
        idle_timeout_seconds: 1800,
        quota: null,
      },
    });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('ofrece correo, contactos, calendario y ajustes en el panel lateral', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX]);
    renderShell();

    const apps = screen.getByRole('navigation', { name: t('webmail.apps.label') });
    expect(within(apps).getByRole('link', { name: t('webmail.apps.contacts') })).toHaveAttribute(
      'href',
      '/webmail/contacts',
    );
    expect(within(apps).getByRole('link', { name: t('webmail.apps.calendar') })).toHaveAttribute(
      'href',
      '/webmail/calendar',
    );
    await user.click(screen.getByRole('button', { name: t('webmail.settings.open') }));
    expect(screen.getByTestId('location').textContent).toBe('/webmail/settings');
  });

  it('c redacta y ? abre la ayuda de atajos', async () => {
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX]);
    renderShell();

    fireEvent.keyDown(document.body, { key: '?' });
    const help = await screen.findByRole('dialog', { name: t('webmail.shortcuts.title') });
    expect(within(help).getByText(t('webmail.shortcuts.archive'))).toBeInTheDocument();
    // Con el dialogo abierto los atajos no actuan.
    fireEvent.keyDown(document.body, { key: 'c' });
    expect(screen.queryByRole('region', { name: t('webmail.composer.label') })).toBeNull();
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    fireEvent.keyDown(document.body, { key: 'c' });
    expect(
      await screen.findByRole('region', { name: t('webmail.composer.label') }),
    ).toBeInTheDocument();
    expect(screen.getByTestId('location').textContent).toBe('/webmail');
  });

  it('activa los avisos de escritorio con permiso y avisa del correo nuevo', async () => {
    const user = userEvent.setup();
    FakeNotification.permission = 'granted';
    const folders = vi
      .spyOn(webmailApi, 'folders')
      .mockResolvedValueOnce([INBOX])
      .mockResolvedValue([{ ...INBOX, unread: INBOX.unread + 2 }]);
    renderShell();

    await user.click(screen.getByRole('button', { name: t('webmail.notify.enable') }));
    expect(await screen.findByText(t('webmail.notify.on'))).toBeInTheDocument();
    expect(readNotifyPreference()).toBe(true);

    await waitFor(() => expect(folders).toHaveBeenCalledTimes(1));
    act(() => watchers[0]?.onChange());
    await waitFor(() => expect(FakeNotification.created).toHaveLength(1));
    expect(FakeNotification.created[0]).toEqual({
      title: t('webmail.notify.title'),
      body: t('webmail.notify.many', { n: 2 }),
    });
  });

  it('si el navegador deniega el permiso, lo explica y no guarda la preferencia', async () => {
    const user = userEvent.setup();
    FakeNotification.permission = 'denied';
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX]);
    renderShell();

    await user.click(screen.getByRole('button', { name: t('webmail.notify.enable') }));
    expect(await screen.findByText(t('webmail.notify.denied'))).toBeInTheDocument();
    expect(readNotifyPreference()).toBe(false);
  });

  it('la cuenta se abre bajo el avatar, cierra con Escape y cierra la sesion', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX]);
    const logout = vi.fn().mockResolvedValue(undefined);
    useWebmailStore.setState({ logout });
    renderShell();

    const trigger = screen.getByRole('button', {
      name: t('webmail.profile.open', { name: 'Ana' }),
    });
    await user.click(trigger);
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('ana@empresa.com')).toBeInTheDocument();
    expect(screen.getByText(t('webmail.profile.greeting', { name: 'Ana' }))).toBeInTheDocument();

    fireEvent.keyDown(document, { key: 'Escape' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByRole('button', { name: t('webmail.logout') }));
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it('busca desde la barra superior en la carpeta abierta y / pone el foco', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX]);
    renderShell();

    const search = screen.getByLabelText(t('webmail.list.search'));
    fireEvent.keyDown(document.body, { key: '/' });
    expect(search).toHaveFocus();
    await screen.findByRole('link', { name: /Bandeja de entrada/ });
    await user.type(search, 'factura{Enter}');
    expect(screen.getByTestId('location').textContent).toBe('/webmail?folder=INBOX&q=factura');

    await user.click(screen.getByRole('button', { name: t('webmail.search.clear') }));
    expect(screen.getByTestId('location').textContent).toBe('/webmail?folder=INBOX');
  });
});
