import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mailDirectoryApi, type AppPassword } from '@/api/mailDirectory';
import { MODULES } from '@/access/modules';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { AppPasswordsTab } from './AppPasswordsTab';
import { MAILBOX } from './mailboxFixtures';

vi.mock('@/api/mailDirectory', () => ({
  directoryMeta: { get: vi.fn() },
  mailDirectoryApi: { listAppPasswords: vi.fn(), createAppPassword: vi.fn() },
}));

const api = vi.mocked(mailDirectoryApi);

const PASSWORD: AppPassword = {
  id: 'ap-1',
  tenant_id: 'tn-1',
  mailbox_id: MAILBOX.id,
  name: 'Movil personal',
  active: true,
  imap_access: false,
  pop3_access: false,
  smtp_access: false,
  sieve_access: false,
  dav_access: true,
  last_used_at: null,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

function grant(...actions: string[]) {
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.mailboxes],
    permissions: actions.map((action) => ({
      module: MODULES.mailboxes,
      resource: 'app_passwords',
      action,
    })),
  });
}

beforeEach(() => {
  grant('read', 'create');
  api.listAppPasswords.mockResolvedValue([PASSWORD]);
});

afterEach(() => {
  vi.resetAllMocks();
  useAccessStore.getState().reset();
});

describe('contrasenas de aplicacion: acceso DAV', () => {
  it('la lista muestra DAV entre los protocolos habilitados', async () => {
    render(
      <ToastProvider>
        <AppPasswordsTab mailbox={MAILBOX} />
      </ToastProvider>,
    );
    const row = (await screen.findByText(PASSWORD.name)).closest('tr') as HTMLElement;
    expect(within(row).getByText(t('mail.protocol.dav_access'))).toBeInTheDocument();
    expect(within(row).queryByText(t('mail.protocol.imap_access'))).not.toBeInTheDocument();
  });

  it('el alta ofrece DAV activado por defecto y envia el valor elegido', async () => {
    const user = userEvent.setup();
    api.createAppPassword.mockResolvedValue({
      data: { app_password: PASSWORD, password: 'generada' },
    } as never);
    render(
      <ToastProvider>
        <AppPasswordsTab mailbox={MAILBOX} />
      </ToastProvider>,
    );
    await user.click(await screen.findByRole('button', { name: t('appPasswords.new') }));
    const dialog = await screen.findByRole('dialog');
    const dav = within(dialog).getByRole('checkbox', { name: t('mail.protocol.dav_access') });
    expect(dav).toBeChecked();

    await user.click(dav);
    await user.type(within(dialog).getByLabelText(new RegExp(`^${t('common.name')}`)), 'Agenda');
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));

    expect(api.createAppPassword).toHaveBeenCalledWith(
      MAILBOX.id,
      expect.objectContaining({ name: 'Agenda', dav_access: false, imap_access: true }),
    );
  });
});
