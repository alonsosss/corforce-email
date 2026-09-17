import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { t } from '@/i18n';
import { useAuthStore } from '@/auth/store';
import LoginPage from './LoginPage';

function renderLogin() {
  render(
    <MemoryRouter initialEntries={['/login']}>
      <LoginPage />
    </MemoryRouter>,
  );
  return {
    email: screen.getByLabelText(new RegExp(`^${t('common.email')}`)),
    password: screen.getByLabelText(new RegExp(`^${t('common.password')}`)),
    submit: screen.getByRole('button', { name: t('auth.login.submit') }),
  };
}

describe('formulario de inicio de sesion', () => {
  afterEach(() => vi.restoreAllMocks());

  it('no pide la empresa: la resuelve la credencial', async () => {
    const user = userEvent.setup();
    const login = vi.spyOn(useAuthStore.getState(), 'login').mockResolvedValue(undefined);
    const campos = renderLogin();

    expect(screen.queryByLabelText(new RegExp(`^${t('auth.login.tenantSlug')}`))).toBeNull();

    await user.type(campos.email, 'ana@empresa.test');
    await user.type(campos.password, 'contrasena-de-prueba');
    await user.click(campos.submit);

    expect(login).toHaveBeenCalledWith({
      email: 'ana@empresa.test',
      password: 'contrasena-de-prueba',
      tenant_slug: undefined,
    });
  });

  it('quien necesita nombrar su empresa la indica, tras pedirlo', async () => {
    const user = userEvent.setup();
    const login = vi.spyOn(useAuthStore.getState(), 'login').mockResolvedValue(undefined);
    const campos = renderLogin();

    await user.click(screen.getByRole('button', { name: t('auth.login.tenantToggle') }));
    const empresa = screen.getByLabelText(new RegExp(`^${t('auth.login.tenantSlug')}`));
    await user.type(empresa, 'empresa-dos');
    await user.type(campos.email, 'ana@empresa.test');
    await user.type(campos.password, 'contrasena-de-prueba');
    await user.click(campos.submit);

    expect(login).toHaveBeenCalledWith({
      email: 'ana@empresa.test',
      password: 'contrasena-de-prueba',
      tenant_slug: 'empresa-dos',
    });
  });
});
