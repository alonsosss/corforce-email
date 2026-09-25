import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { webmailApi } from '@/api/webmail';
import { t } from '@/i18n';
import { useWebmailStore } from '@/webmail/store';
import { WebmailLoginPage } from './WebmailLoginPage';

function renderLogin() {
  render(
    <MemoryRouter initialEntries={['/webmail/login']}>
      <Routes>
        <Route path="/webmail/login" element={<WebmailLoginPage />} />
        <Route path="/webmail" element={<p>Bandeja abierta</p>} />
      </Routes>
    </MemoryRouter>,
  );
  return {
    username: screen.getByLabelText(new RegExp(`^${t('webmail.login.username')}`)),
    password: screen.getByLabelText(new RegExp(`^${t('common.password')}`)),
    submit: screen.getByRole('button', { name: t('webmail.login.submit') }),
  };
}

describe('inicio de sesion del buzon', () => {
  beforeEach(() => {
    useWebmailStore.setState({
      status: 'anonymous',
      session: null,
      expired: false,
      passwordChanged: false,
      mfaExpired: false,
      mfaUsername: null,
      checkError: null,
    });
  });

  afterEach(() => vi.restoreAllMocks());

  it('ofrece la entrada de la plataforma a quien administra', () => {
    renderLogin();
    expect(screen.getByRole('link', { name: t('webmail.login.platformLink') })).toHaveAttribute(
      'href',
      '/login',
    );
  });

  it('buzon inexistente y contrasena mala dan el mismo mensaje y se borra la contrasena', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'login').mockRejectedValue(
      new ApiError(401, {
        code: ERROR_CODES.INVALID_CREDENTIALS,
        message: 'usuario o contraseña incorrectos',
      }),
    );
    const form = renderLogin();

    await user.type(form.username, 'nadie@empresa.com');
    await user.type(form.password, 'mala');
    await user.click(form.submit);

    expect(await screen.findByText(t('webmail.login.invalid'))).toBeInTheDocument();
    expect(form.password).toHaveValue('');
    expect(useWebmailStore.getState().status).toBe('anonymous');
  });

  it('demasiados intentos se explican sin decir si el buzon existe', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'login').mockRejectedValue(
      new ApiError(429, { code: ERROR_CODES.RATE_LIMITED, message: '' }),
    );
    const form = renderLogin();
    await user.type(form.username, 'ana@empresa.com');
    await user.type(form.password, 'x');
    await user.click(form.submit);
    expect(await screen.findByText(t('webmail.login.rateLimited'))).toBeInTheDocument();
  });

  it('con la sesion abierta entra en el buzon', async () => {
    const user = userEvent.setup();
    const session = {
      username: 'ana@empresa.com',
      display_name: 'Ana',
      expires_at: '2026-09-13T20:00:00Z',
      idle_timeout_seconds: 1800,
      quota: null,
    };
    vi.spyOn(webmailApi, 'login').mockResolvedValue(session);
    vi.spyOn(webmailApi, 'session').mockResolvedValue(session);
    const form = renderLogin();

    await user.type(form.username, 'ana@empresa.com');
    await user.type(form.password, 'buena');
    await user.click(form.submit);

    expect(await screen.findByText('Bandeja abierta')).toBeInTheDocument();
    expect(webmailApi.login).toHaveBeenCalledWith('ana@empresa.com', 'buena');
  });

  it('avisa cuando la sesion anterior caduco', () => {
    useWebmailStore.setState({ expired: true });
    renderLogin();
    expect(screen.getByText(t('webmail.login.expired'))).toBeInTheDocument();
  });

  it('tras cambiar la contrasena pide entrar con la nueva, y el aviso se va al entrar', async () => {
    const user = userEvent.setup();
    useWebmailStore.setState({ status: 'authenticated' });
    useWebmailStore.getState().endAfterPasswordChange();
    expect(useWebmailStore.getState()).toMatchObject({ status: 'anonymous', session: null });
    vi.spyOn(webmailApi, 'login').mockRejectedValue(
      new ApiError(401, { code: ERROR_CODES.INVALID_CREDENTIALS, message: '' }),
    );
    const form = renderLogin();
    expect(screen.getByText(t('webmail.login.passwordChanged'))).toBeInTheDocument();
    expect(screen.queryByText(t('webmail.login.expired'))).toBeNull();

    await user.type(form.username, 'ana@empresa.com');
    await user.type(form.password, 'vieja');
    await user.click(form.submit);
    expect(await screen.findByText(t('webmail.login.invalid'))).toBeInTheDocument();
    expect(screen.queryByText(t('webmail.login.passwordChanged'))).toBeNull();
  });

  describe('verificacion en dos pasos', () => {
    const SESSION = {
      username: 'ana@empresa.com',
      display_name: 'Ana',
      expires_at: '2026-09-13T20:00:00Z',
      idle_timeout_seconds: 1800,
      quota: null,
    };

    async function passwordStep(user: ReturnType<typeof userEvent.setup>) {
      vi.spyOn(webmailApi, 'login').mockResolvedValue({ mfa_required: true });
      const form = renderLogin();
      await user.type(form.username, 'ana@empresa.com');
      await user.type(form.password, 'buena');
      await user.click(form.submit);
      return screen.findByLabelText(new RegExp(`^${t('webmail.login.mfa.code')}`));
    }

    it('tras la contrasena pide el codigo y abre la sesion con el', async () => {
      const user = userEvent.setup();
      const code = await passwordStep(user);
      expect(useWebmailStore.getState().status).toBe('mfa');
      expect(code).toHaveAttribute('autocomplete', 'one-time-code');
      expect(code).toHaveAttribute('inputmode', 'numeric');
      const verify = vi.spyOn(webmailApi, 'loginMfa').mockResolvedValue(SESSION);
      vi.spyOn(webmailApi, 'session').mockResolvedValue(SESSION);

      await user.type(code, '123456');
      await user.click(screen.getByRole('button', { name: t('webmail.login.mfa.submit') }));

      expect(await screen.findByText('Bandeja abierta')).toBeInTheDocument();
      expect(verify).toHaveBeenCalledWith('123456');
      expect(useWebmailStore.getState().status).toBe('authenticated');
    });

    it('un codigo malo deja el paso abierto con el mensaje', async () => {
      const user = userEvent.setup();
      const code = await passwordStep(user);
      vi.spyOn(webmailApi, 'loginMfa').mockRejectedValue(
        new ApiError(422, { code: ERROR_CODES.INVALID_MFA_CODE, message: '' }),
      );
      await user.type(code, '000000');
      await user.click(screen.getByRole('button', { name: t('webmail.login.mfa.submit') }));

      expect(await screen.findByText(t('webmail.login.mfa.invalid'))).toBeInTheDocument();
      expect(code).toHaveValue('');
      expect(useWebmailStore.getState().status).toBe('mfa');
    });

    it('acepta un codigo de recuperacion en su forma canonica', async () => {
      const user = userEvent.setup();
      await passwordStep(user);
      const verify = vi.spyOn(webmailApi, 'loginMfa').mockResolvedValue(SESSION);
      vi.spyOn(webmailApi, 'session').mockResolvedValue(SESSION);
      await user.click(screen.getByRole('button', { name: t('webmail.login.mfa.useRecovery') }));
      const recovery = screen.getByLabelText(new RegExp(t('webmail.login.mfa.recoveryCode')));
      expect(recovery).not.toHaveAttribute('inputmode');

      await user.type(recovery, 'abcde fghij');
      await user.click(screen.getByRole('button', { name: t('webmail.login.mfa.submit') }));
      await waitFor(() => expect(verify).toHaveBeenCalledWith('ABCDE-FGHIJ'));
    });

    it('un desafio caducado vuelve a la contrasena con aviso', async () => {
      const user = userEvent.setup();
      const code = await passwordStep(user);
      vi.spyOn(webmailApi, 'loginMfa').mockRejectedValue(
        new ApiError(401, { code: ERROR_CODES.MFA_CHALLENGE_EXPIRED, message: '' }),
      );
      await user.type(code, '123456');
      await user.click(screen.getByRole('button', { name: t('webmail.login.mfa.submit') }));

      expect(await screen.findByText(t('webmail.login.mfa.expired'))).toBeInTheDocument();
      expect(screen.getByRole('button', { name: t('webmail.login.submit') })).toBeInTheDocument();
      expect(useWebmailStore.getState()).toMatchObject({ status: 'anonymous', mfaUsername: null });
    });

    it('conserva la pantalla a la que se queria ir', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter
          initialEntries={[{ pathname: '/webmail/login', state: { from: '/webmail/settings' } }]}
        >
          <Routes>
            <Route path="/webmail/login" element={<WebmailLoginPage />} />
            <Route path="/webmail/settings" element={<p>Ajustes abiertos</p>} />
          </Routes>
        </MemoryRouter>,
      );
      vi.spyOn(webmailApi, 'login').mockResolvedValue({ mfa_required: true });
      vi.spyOn(webmailApi, 'loginMfa').mockResolvedValue(SESSION);
      vi.spyOn(webmailApi, 'session').mockResolvedValue(SESSION);
      await user.type(
        screen.getByLabelText(new RegExp(`^${t('webmail.login.username')}`)),
        'ana@empresa.com',
      );
      await user.type(screen.getByLabelText(new RegExp(`^${t('common.password')}`)), 'buena');
      await user.click(screen.getByRole('button', { name: t('webmail.login.submit') }));
      await user.type(
        await screen.findByLabelText(new RegExp(`^${t('webmail.login.mfa.code')}`)),
        '123456',
      );
      await user.click(screen.getByRole('button', { name: t('webmail.login.mfa.submit') }));
      expect(await screen.findByText('Ajustes abiertos')).toBeInTheDocument();
    });
  });
});
