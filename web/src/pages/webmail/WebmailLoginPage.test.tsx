import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
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
      checkError: null,
    });
  });

  afterEach(() => vi.restoreAllMocks());

  it('buzon inexistente y contrasena mala dan el mismo mensaje y se borra la contrasena', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'login').mockRejectedValue(
      new ApiError(401, {
        code: ERROR_CODES.INVALID_CREDENTIALS,
        message: 'usuario o contrasena incorrectos',
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
});
