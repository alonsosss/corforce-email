import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import { webmailApi, type SenderInsight } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { SenderShield, type SenderShieldProps } from './SenderShield';
import { INBOX } from './testing';

const SENDER = { name: 'Carlos Ruiz', email: 'carlos@gratis.test' };

function insight(overrides: Partial<SenderInsight> = {}): SenderInsight {
  return {
    sender: SENDER,
    category: 'primary',
    shield: {
      level: 'info',
      external: true,
      partial: false,
      authentication: { spf: 'pass', dkim: 'pass', dmarc: 'pass' },
      reasons: [{ code: 'external_sender', level: 'info', params: { domain: 'gratis.test' } }],
    },
    unsubscribe: { method: null, target: '' },
    ...overrides,
  };
}

function renderShield(overrides: Partial<SenderShieldProps> = {}) {
  const props: SenderShieldProps = {
    folderName: 'INBOX',
    role: 'inbox',
    uid: 7,
    folders: [INBOX],
    sender: SENDER,
    ...overrides,
  };
  render(
    <MemoryRouter>
      <ToastProvider>
        <SenderShield {...props} />
      </ToastProvider>
    </MemoryRouter>,
  );
  return props;
}

describe('escudo antifraude en la lectura', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'davMeta').mockResolvedValue({ limits: {} });
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 50,
      total: 0,
      totalPages: 0,
    });
    vi.spyOn(webmailApi, 'messages').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 50,
      total: 0,
      totalPages: 0,
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it('explica un posible fraude y ofrece marcarlo', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'senderInsight').mockResolvedValue(
      insight({
        shield: {
          level: 'danger',
          external: true,
          partial: false,
          authentication: { spf: 'fail', dkim: null, dmarc: 'fail' },
          reasons: [
            { code: 'external_sender', level: 'info', params: { domain: 'gratis.test' } },
            {
              code: 'colleague_name',
              level: 'danger',
              params: { colleague: 'Carlos Ruiz', address: 'carlos@empresa.pe' },
            },
          ],
        },
      }),
    );
    const onReportFraud = vi.fn();
    renderShield({ onReportFraud });

    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText(t('webmail.shield.dangerTitle'))).toBeInTheDocument();
    expect(
      within(alert).getByText(
        t('webmail.shield.reason.colleague_name', {
          colleague: 'Carlos Ruiz',
          address: 'carlos@empresa.pe',
        }),
      ),
    ).toBeInTheDocument();
    expect(within(alert).queryByText(/gratis\.test/)).toBeNull();
    await user.click(within(alert).getByRole('button', { name: t('webmail.shield.reportFraud') }));
    expect(onReportFraud).toHaveBeenCalledTimes(1);
    expect(screen.getByText(t('webmail.shield.external'))).toBeInTheDocument();
  });

  it('un remitente verificado de la empresa no muestra avisos', async () => {
    const read = vi.spyOn(webmailApi, 'senderInsight').mockResolvedValue(
      insight({
        shield: {
          level: 'none',
          external: false,
          partial: false,
          authentication: { spf: null, dkim: null, dmarc: null },
          reasons: [],
        },
      }),
    );
    renderShield();
    await waitFor(() => expect(read).toHaveBeenCalledWith('INBOX', 7, expect.anything()));
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByText(t('webmail.shield.external'))).toBeNull();
    expect(screen.getByRole('button', { name: t('webmail.sender.open') })).toBeInTheDocument();
  });

  it('en lo enviado no consulta el escudo', () => {
    const read = vi.spyOn(webmailApi, 'senderInsight');
    renderShield({ role: 'sent' });
    expect(read).not.toHaveBeenCalled();
    expect(screen.queryByRole('region')).toBeNull();
  });

  it('si el escudo no responde, el mensaje se lee igual y queda la ficha', async () => {
    vi.spyOn(webmailApi, 'senderInsight').mockRejectedValue(
      new ApiError(404, { code: 'MESSAGE_NOT_FOUND', message: '' }),
    );
    renderShield();
    expect(
      await screen.findByRole('button', { name: t('webmail.sender.open') }),
    ).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('la baja en un clic se confirma y la hace el servicio', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'senderInsight').mockResolvedValue(
      insight({ unsubscribe: { method: 'one_click', target: 'news.tienda.test' } }),
    );
    const unsubscribe = vi
      .spyOn(webmailApi, 'unsubscribe')
      .mockResolvedValue({ method: 'one_click', target: 'news.tienda.test' });
    renderShield();

    await user.click(await screen.findByRole('button', { name: t('webmail.unsubscribe.action') }));
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByText(
        t('webmail.unsubscribe.confirmOneClick', { target: 'news.tienda.test' }),
      ),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: t('webmail.unsubscribe.action') }));
    await waitFor(() => expect(unsubscribe).toHaveBeenCalledWith('INBOX', 7));
    expect(
      await screen.findByText(t('webmail.unsubscribe.done', { target: 'news.tienda.test' })),
    ).toBeInTheDocument();
  });

  it('una baja rechazada por el servicio se explica en el dialogo', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'senderInsight').mockResolvedValue(
      insight({ unsubscribe: { method: 'mailto', target: 'baja@tienda.test' } }),
    );
    vi.spyOn(webmailApi, 'unsubscribe').mockRejectedValue(
      new ApiError(422, { code: 'UNSUBSCRIBE_TARGET_REFUSED', message: '' }),
    );
    renderShield();
    await user.click(await screen.findByRole('button', { name: t('webmail.unsubscribe.action') }));
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByText(
        t('webmail.unsubscribe.confirmMailto', { target: 'baja@tienda.test' }),
      ),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: t('webmail.unsubscribe.action') }));
    expect(
      await within(dialog).findByText(t('error.code.UNSUBSCRIBE_TARGET_REFUSED')),
    ).toBeInTheDocument();
  });

  it('una pagina de baja se abre en otra pestana sin referer y solo si es https', async () => {
    vi.spyOn(webmailApi, 'senderInsight').mockResolvedValue(
      insight({
        unsubscribe: { method: 'web', target: 'tienda.test', url: 'https://tienda.test/baja?t=1' },
      }),
    );
    renderShield();
    const link = await screen.findByRole('link', {
      name: t('webmail.unsubscribe.openPage', { target: 'tienda.test' }),
    });
    expect(link).toHaveAttribute('href', 'https://tienda.test/baja?t=1');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
    expect(link.getAttribute('rel')).toContain('noreferrer');
    expect(screen.queryByRole('button', { name: t('webmail.unsubscribe.action') })).toBeNull();
  });

  it('la ficha del remitente se abre en un panel lateral', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'senderInsight').mockResolvedValue(insight());
    renderShield();
    await user.click(await screen.findByRole('button', { name: t('webmail.sender.open') }));
    const panel = screen.getByRole('complementary', {
      name: t('webmail.sender.title', { name: 'Carlos Ruiz' }),
    });
    expect(await within(panel).findByText(t('webmail.sender.notContact'))).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('complementary')).toBeNull();
  });
});
