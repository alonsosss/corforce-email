import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type WebmailAppPassword, type WebmailSecurity } from '@/api/webmail';
import { t } from '@/i18n';
import * as download from '@/lib/download';
import { useWebmailStore } from '@/webmail/store';
import SettingsPage from '../SettingsPage';
import { renderScreen } from '../testing';

const OFF: WebmailSecurity = {
  mfa: { enabled: false, enabled_at: null, recovery_remaining: 0 },
  app_passwords: [],
  app_passwords_max: 2,
};
const ON: WebmailSecurity = {
  ...OFF,
  mfa: { enabled: true, enabled_at: '2026-09-24T10:00:00Z', recovery_remaining: 10 },
};
const CODES = Array.from({ length: 10 }, (_, i) => `AAAA${i}-BBBB${i}`);

const PHONE: WebmailAppPassword = {
  id: 'ap-1',
  name: 'Movil',
  imap_access: true,
  pop3_access: false,
  smtp_access: true,
  sieve_access: false,
  dav_access: false,
  active: true,
  last_used_at: null,
  created_at: '2026-09-20T10:00:00Z',
};

function renderSecurity() {
  renderScreen(<SettingsPage />, {
    path: '/webmail/settings',
    url: '/webmail/settings?tab=security',
  });
}

describe('ajustes: seguridad', () => {
  beforeEach(() => {
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
  afterEach(() => vi.restoreAllMocks());

  it('activa la verificacion con la contrasena, el QR y un codigo, y muestra los codigos una vez', async () => {
    const user = userEvent.setup();
    const security = vi
      .spyOn(webmailApi, 'security')
      .mockResolvedValueOnce(OFF)
      .mockResolvedValue({ ...ON });
    const setup = vi.spyOn(webmailApi, 'mfaSetup').mockResolvedValue({
      secret: 'JBSWY3DPEHPK3PXP',
      provisioning_uri: 'otpauth://totp/Correo:ana@empresa.com?secret=JBSWY3DPEHPK3PXP',
    });
    const activate = vi
      .spyOn(webmailApi, 'mfaActivate')
      .mockRejectedValueOnce(new ApiError(422, { code: 'INVALID_MFA_CODE', message: '' }))
      .mockResolvedValue({ recovery_codes: CODES, other_sessions_closed: true });
    const save = vi.spyOn(download, 'saveBlob').mockImplementation(() => undefined);
    renderSecurity();

    expect(await screen.findByText(t('webmail.security.mfa.off'))).toBeInTheDocument();
    expect(screen.getByText(t('webmail.security.mfa.appsNotice'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('webmail.security.mfa.enable') }));

    let dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(t('webmail.security.mfa.appsNotice'))).toBeInTheDocument();
    await user.type(
      within(dialog).getByLabelText(new RegExp(t('webmail.password.current'))),
      'clave',
    );
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.security.mfa.continue') }),
    );
    await waitFor(() => expect(setup).toHaveBeenCalledWith('clave'));

    expect(
      await within(dialog).findByRole('img', { name: t('webmail.security.mfa.qrAlt') }),
    ).toBeInTheDocument();
    expect(within(dialog).getByText('JBSW Y3DP EHPK 3PXP')).toBeInTheDocument();
    const code = within(dialog).getByLabelText(new RegExp(t('webmail.security.code')));
    await user.type(code, '000000');
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.security.mfa.activate') }),
    );
    expect(await within(dialog).findByText(t('error.code.INVALID_MFA_CODE'))).toBeInTheDocument();
    await user.type(code, '123456');
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.security.mfa.activate') }),
    );
    await waitFor(() => expect(activate).toHaveBeenLastCalledWith('JBSWY3DPEHPK3PXP', '123456'));
    expect(
      await screen.findByText(t('webmail.security.mfa.otherSessionsClosed')),
    ).toBeInTheDocument();

    dialog = await screen.findByRole('dialog', { name: t('webmail.security.recovery.title') });
    for (const recovery of CODES) expect(within(dialog).getByText(recovery)).toBeInTheDocument();
    const close = within(dialog).getByRole('button', { name: t('common.close') });
    expect(close).toBeDisabled();

    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.security.recovery.download') }),
    );
    expect(save).toHaveBeenCalledWith(
      expect.any(Blob),
      t('webmail.security.recovery.filename', { mailbox: 'ana@empresa.com' }),
    );
    const blob = save.mock.calls[0]?.[0] as Blob;
    expect(await blob.text()).toContain(CODES[9]);

    await user.click(within(dialog).getByLabelText(t('webmail.security.recovery.confirm')));
    expect(close).toBeEnabled();
    await user.click(close);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(await screen.findByText(t('webmail.security.mfa.on'))).toBeInTheDocument();
    expect(security).toHaveBeenCalledTimes(2);
  });

  it('avisa cuando quedan pocos codigos de recuperacion', async () => {
    vi.spyOn(webmailApi, 'security').mockResolvedValue({
      ...ON,
      mfa: { ...ON.mfa, recovery_remaining: 2 },
    });
    renderSecurity();
    expect(
      await screen.findByText(t('webmail.security.mfa.recoveryLow', { n: 2 })),
    ).toBeInTheDocument();
  });

  it('desactivar exige la contrasena y un codigo', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'security').mockResolvedValueOnce(ON).mockResolvedValue(OFF);
    const disable = vi.spyOn(webmailApi, 'disableMfa').mockResolvedValue(null);
    renderSecurity();

    await user.click(
      await screen.findByRole('button', { name: t('webmail.security.mfa.disable') }),
    );
    const dialog = screen.getByRole('dialog');
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.security.mfa.disable') }),
    );
    expect(within(dialog).getByText(t('webmail.password.currentRequired'))).toBeInTheDocument();
    expect(disable).not.toHaveBeenCalled();

    await user.type(
      within(dialog).getByLabelText(new RegExp(t('webmail.password.current'))),
      'clave',
    );
    await user.type(
      within(dialog).getByLabelText(new RegExp(t('webmail.security.code'))),
      'ABCDE-FGHIJ',
    );
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.security.mfa.disable') }),
    );
    await waitFor(() => expect(disable).toHaveBeenCalledWith('clave', 'ABCDE-FGHIJ'));
    expect(await screen.findByText(t('webmail.security.mfa.disabledDone'))).toBeInTheDocument();
  });

  it('crea una contrasena de aplicacion con la contrasena y el codigo y la muestra una vez', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'security')
      .mockResolvedValueOnce(ON)
      .mockResolvedValue({ ...ON, app_passwords: [PHONE] });
    const create = vi
      .spyOn(webmailApi, 'createAppPassword')
      .mockResolvedValue({ ...PHONE, password: 'abcd-efgh-ijkl-mnop' });
    renderSecurity();

    await user.click(await screen.findByRole('button', { name: t('appPasswords.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(new RegExp(t('common.name'))), 'Movil');
    for (const label of ['POP3', 'Sieve', 'DAV']) {
      await user.click(within(dialog).getByLabelText(label));
    }
    await user.type(
      within(dialog).getByLabelText(new RegExp(t('webmail.password.current'))),
      'clave',
    );
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));
    expect(within(dialog).getByText(t('webmail.security.codeMissing'))).toBeInTheDocument();
    await user.type(
      within(dialog).getByLabelText(new RegExp(t('webmail.security.code'))),
      '123456',
    );
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));

    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        { name: 'Movil', imap: true, pop3: false, smtp: true, sieve: false, dav: false },
        { current_password: 'clave', code: '123456' },
      ),
    );
    const shown = await screen.findByRole('dialog', {
      name: t('appPasswords.created.title', { name: 'Movil' }),
    });
    expect(within(shown).getByText('abcd-efgh-ijkl-mnop')).toBeInTheDocument();
    await user.click(within(shown).getByRole('button', { name: t('appPasswords.created.done') }));
    expect(screen.queryByText('abcd-efgh-ijkl-mnop')).toBeNull();
    expect(await screen.findByText('Movil')).toBeInTheDocument();
  });

  it('revoca una contrasena de aplicacion tras confirmar y respeta el maximo servido', async () => {
    const user = userEvent.setup();
    const second = { ...PHONE, id: 'ap-2', name: 'Portatil' };
    vi.spyOn(webmailApi, 'security')
      .mockResolvedValueOnce({ ...OFF, app_passwords: [PHONE, second] })
      .mockResolvedValue({ ...OFF, app_passwords: [second] });
    const remove = vi.spyOn(webmailApi, 'deleteAppPassword').mockResolvedValue(null);
    renderSecurity();

    expect(
      await screen.findByText(t('webmail.security.apps.full', { max: 2 })),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('appPasswords.new') })).toBeDisabled();

    await user.click(
      screen.getByRole('button', {
        name: t('webmail.security.apps.revokeName', { name: 'Movil' }),
      }),
    );
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('appPasswords.revoke') }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith('ap-1'));
    await waitFor(() => expect(screen.queryByText('Movil')).toBeNull());
    expect(screen.getByRole('button', { name: t('appPasswords.new') })).toBeEnabled();
  });
});
