import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { t } from '@/i18n';
import { useAuthStore } from '@/auth/store';
import { useWebmailStore } from '@/webmail/store';
import LoginPage from './LoginPage';

function renderLogin() {
  render(
    <MemoryRouter initialEntries={['/login']}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/webmail" element={<p>Bandeja abierta</p>} />
      </Routes>
    </MemoryRouter>,
  );
  return {
    email: screen.getByLabelText(new RegExp(`^${t('common.email')}`)),
    password: screen.getByLabelText(new RegExp(`^${t('common.password')}`)),
    submit: screen.getByRole('button', { name: t('auth.login.submit') }),
  };
}

const mailboxRejects = (err: unknown) =>
  vi.spyOn(useWebmailStore.getState(), 'login').mockRejectedValue(err);

const badMailbox = () =>
  new ApiError(401, { code: ERROR_CODES.INVALID_CREDENTIALS, message: 'no' });
const badPlatform = () => new ApiError(401, { code: ERROR_CODES.UNAUTHORIZED, message: 'no' });

async function enter(campos: ReturnType<typeof renderLogin>, email: string) {
  const user = userEvent.setup();
  await user.type(campos.email, email);
  await user.type(campos.password, 'contrasena-de-prueba');
  await user.click(campos.submit);
  return user;
}

describe('inicio de sesion unico', () => {
  beforeEach(() => {
    useAuthStore.setState({ status: 'anonymous', mfaToken: null });
  });
  afterEach(() => vi.restoreAllMocks());

  it('quien tiene buzon entra a su correo sin tocar la plataforma', async () => {
    const mailbox = vi.spyOn(useWebmailStore.getState(), 'login').mockResolvedValue(undefined);
    const platform = vi.spyOn(useAuthStore.getState(), 'login');
    await enter(renderLogin(), 'ana@empresa.test');

    expect(await screen.findByText('Bandeja abierta')).toBeInTheDocument();
    expect(mailbox).toHaveBeenCalledWith('ana@empresa.test', 'contrasena-de-prueba');
    expect(platform).not.toHaveBeenCalled();
  });

  it('si no es un buzon, entra a la plataforma sin pedir la empresa', async () => {
    mailboxRejects(badMailbox());
    const platform = vi.spyOn(useAuthStore.getState(), 'login').mockResolvedValue(undefined);
    const campos = renderLogin();

    expect(screen.queryByLabelText(new RegExp(`^${t('auth.login.tenantSlug')}`))).toBeNull();
    await enter(campos, 'admin@empresa.test');

    expect(platform).toHaveBeenCalledWith({
      email: 'admin@empresa.test',
      password: 'contrasena-de-prueba',
      tenant_slug: undefined,
    });
  });

  it('con la empresa indicada va directo a la plataforma', async () => {
    const mailbox = vi.spyOn(useWebmailStore.getState(), 'login');
    const platform = vi.spyOn(useAuthStore.getState(), 'login').mockResolvedValue(undefined);
    const campos = renderLogin();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: t('auth.login.tenantToggle') }));
    await user.type(
      screen.getByLabelText(new RegExp(`^${t('auth.login.tenantSlug')}`)),
      'empresa-dos',
    );
    await enter(campos, 'admin@empresa.test');

    expect(mailbox).not.toHaveBeenCalled();
    expect(platform).toHaveBeenCalledWith({
      email: 'admin@empresa.test',
      password: 'contrasena-de-prueba',
      tenant_slug: 'empresa-dos',
    });
  });

  it('si ninguna credencial coincide, un solo mensaje y se borra la contrasena', async () => {
    mailboxRejects(badMailbox());
    vi.spyOn(useAuthStore.getState(), 'login').mockRejectedValue(badPlatform());
    const campos = renderLogin();
    await enter(campos, 'nadie@empresa.test');

    expect(await screen.findByText(t('auth.login.invalidCredentials'))).toBeInTheDocument();
    expect(campos.password).toHaveValue('');
  });

  it('si el buzon no se pudo comprobar, no se dice que la contrasena es mala', async () => {
    mailboxRejects(new ApiError(429, { code: ERROR_CODES.RATE_LIMITED, message: 'espera' }));
    vi.spyOn(useAuthStore.getState(), 'login').mockRejectedValue(badPlatform());
    await enter(renderLogin(), 'ana@empresa.test');

    expect(await screen.findByText(t('webmail.login.rateLimited'))).toBeInTheDocument();
    expect(screen.queryByText(t('auth.login.invalidCredentials'))).toBeNull();
  });
});
