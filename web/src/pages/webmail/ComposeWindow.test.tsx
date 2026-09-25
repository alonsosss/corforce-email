import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type Signature } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import ComposeRoute from './ComposeWindow';
import { DRAFTS, INBOX, META, outletFor, renderScreen } from './testing';

const SIGNATURE: Signature = {
  enabled: false,
  html: '',
  text: '',
  on_replies: false,
  updated_at: null,
  limits: { max_html_bytes: 8192, max_text_bytes: 4096 },
};

function renderWindow() {
  renderScreen(<ComposeRoute />, {
    path: '/webmail/compose',
    outlet: outletFor([INBOX, DRAFTS]),
  });
}

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

  it('flota sobre el buzon y se minimiza, se restaura y se amplia', async () => {
    const user = userEvent.setup();
    renderWindow();

    const subject = await screen.findByLabelText(t('webmail.header.subject'));
    expect(screen.getByRole('heading', { name: t('webmail.role.inbox') })).toBeInTheDocument();
    const region = screen.getByRole('region', { name: t('webmail.composer.label') });

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

  it('cerrarla con algo escrito lo guarda como borrador', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderWindow();

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
    expect(screen.getByTestId('location').textContent).toBe('/webmail');
  });

  it('descartar no deja borrador', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderWindow();

    await user.type(await screen.findByLabelText(t('webmail.header.subject')), 'Pedido');
    await user.click(screen.getByRole('button', { name: t('webmail.compose.discard') }));
    const dialog = await screen.findByRole('dialog', { name: t('webmail.compose.discardTitle') });
    await user.click(within(dialog).getByRole('button', { name: t('webmail.compose.discard') }));

    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/webmail'));
    expect(saveDraft).not.toHaveBeenCalled();
  });

  it('sin nada escrito se cierra sin guardar', async () => {
    const user = userEvent.setup();
    const saveDraft = vi.spyOn(webmailApi, 'saveDraft').mockResolvedValue({ uid: 9 });
    renderWindow();

    await screen.findByLabelText(t('webmail.header.subject'));
    await user.click(screen.getByRole('button', { name: t('webmail.composer.close') }));

    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/webmail'));
    expect(saveDraft).not.toHaveBeenCalled();
  });

  it('los atajos del buzon de fondo no actuan mientras se redacta', async () => {
    const remove = vi
      .spyOn(webmailApi, 'batch')
      .mockResolvedValue({ affected: 1, permanent: false });
    vi.spyOn(webmailApi, 'message').mockRejectedValue(new ApiError(404, null));
    renderScreen(<ComposeRoute />, {
      path: '/webmail/compose',
      url: '/webmail/compose?mode=reply&folder=INBOX&uid=7',
      outlet: outletFor([INBOX, DRAFTS]),
    });
    await screen.findByRole('region', { name: t('webmail.composer.label') });

    fireEvent.keyDown(document.body, { key: '#' });
    fireEvent.keyDown(document.body, { key: 'e' });
    fireEvent.keyDown(document.body, { key: 'j' });

    expect(remove).not.toHaveBeenCalled();
    expect(screen.getByTestId('location').textContent).toBe(
      '/webmail/compose?mode=reply&folder=INBOX&uid=7',
    );
  });

  it('el destinatario de ?to= no filtra el buzon de fondo', async () => {
    const messages = vi.mocked(webmailApi.messages);
    renderScreen(<ComposeRoute />, {
      path: '/webmail/compose',
      url: '/webmail/compose?to=luis%40cliente.com',
      outlet: outletFor([INBOX, DRAFTS]),
    });
    await screen.findByLabelText(t('webmail.header.subject'));
    await waitFor(() => expect(messages).toHaveBeenCalled());
    for (const call of messages.mock.calls) {
      expect(call[1]).not.toHaveProperty('to', 'luis@cliente.com');
    }
  });
});
