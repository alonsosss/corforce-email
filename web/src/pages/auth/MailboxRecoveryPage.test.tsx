import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { webmailApi } from '@/api/webmail';
import { t } from '@/i18n';
import MailboxRecoveryPage from './MailboxRecoveryPage';

function renderPage() {
  render(
    <MemoryRouter>
      <MailboxRecoveryPage />
    </MemoryRouter>,
  );
  return {
    email: screen.getByLabelText(new RegExp(`^${t('common.email')}`)),
    totp: screen.getByLabelText(new RegExp(`^${t('auth.mailboxRecovery.totpCode')}`)),
    recovery: screen.getByLabelText(new RegExp(`^${t('auth.mailboxRecovery.recoveryCode')}`)),
    password: screen.getByLabelText(new RegExp(`^${t('auth.reset.newPassword')}`)),
    confirm: screen.getByLabelText(new RegExp(`^${t('auth.reset.confirmPassword')}`)),
    submit: screen.getByRole('button', { name: t('auth.mailboxRecovery.submit') }),
  };
}

async function fill(campos: ReturnType<typeof renderPage>, confirm = 'Nueva-Contrasena-2030') {
  const user = userEvent.setup();
  await user.type(campos.email, ' ana@empresa.test ');
  await user.type(campos.totp, '123456');
  await user.type(campos.recovery, 'abcde fghjk');
  await user.type(campos.password, 'Nueva-Contrasena-2030');
  await user.type(campos.confirm, confirm);
  await user.click(campos.submit);
}

describe('recuperacion de la contrasena del buzon', () => {
  afterEach(() => vi.restoreAllMocks());

  it('envia los dos codigos normalizados y avisa de lo que cambio', async () => {
    const recover = vi
      .spyOn(webmailApi, 'recoverPassword')
      .mockResolvedValue({ recovery_remaining: 2, app_passwords_revoked: 1 });
    await fill(renderPage());

    expect(recover).toHaveBeenCalledWith({
      username: 'ana@empresa.test',
      totp_code: '123456',
      recovery_code: 'ABCDE-FGHJK',
      new_password: 'Nueva-Contrasena-2030',
    });
    expect(await screen.findByText(t('auth.mailboxRecovery.done'))).toBeInTheDocument();
    expect(screen.getByText(new RegExp(t('auth.mailboxRecovery.lowCodes')))).toBeInTheDocument();
    expect(
      screen.getByText(t('auth.mailboxRecovery.appPasswordsRevoked', { count: 1 })),
    ).toBeInTheDocument();
  });

  it('un rechazo no dice que fallo y vacia los codigos', async () => {
    vi.spyOn(webmailApi, 'recoverPassword').mockRejectedValue(
      new ApiError(422, { code: ERROR_CODES.PASSWORD_RECOVERY_REJECTED, message: 'no' }),
    );
    const campos = renderPage();
    await fill(campos);

    expect(await screen.findByRole('alert')).toHaveTextContent(t('auth.mailboxRecovery.rejected'));
    expect(campos.totp).toHaveValue('');
    expect(campos.recovery).toHaveValue('');
  });

  it('no envia nada si las contrasenas no coinciden', async () => {
    const recover = vi.spyOn(webmailApi, 'recoverPassword');
    await fill(renderPage(), 'otra-distinta');

    expect(recover).not.toHaveBeenCalled();
    expect(screen.getByRole('alert')).toHaveTextContent(t('validation.passwordMismatch'));
  });
});
