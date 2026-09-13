import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { t } from '@/i18n';
import { UserForm } from './UserForm';

function renderCreate(onSubmit = vi.fn(async () => undefined)) {
  render(<UserForm mode="create" open onClose={vi.fn()} onSubmit={onSubmit} />);
  return { onSubmit, user: userEvent.setup() };
}

const field = (key: Parameters<typeof t>[0]) => screen.getByLabelText(t(key), { exact: false });

describe('UserForm', () => {
  it('no envia y marca los campos obligatorios vacios', async () => {
    const { onSubmit, user } = renderCreate();

    await user.click(screen.getByRole('button', { name: t('common.create') }));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getAllByText(t('validation.required'))).toHaveLength(4);
  });

  it('valida el correo y la longitud minima de la contrasena', async () => {
    const { onSubmit, user } = renderCreate();

    await user.type(field('users.form.firstName'), 'Ana');
    await user.type(field('users.form.lastName'), 'Torres');
    await user.type(field('users.form.email'), 'no-es-un-correo');
    await user.type(field('users.form.password'), 'corta');
    await user.click(screen.getByRole('button', { name: t('common.create') }));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(t('validation.email'))).toBeInTheDocument();
    expect(screen.getByText(t('validation.minLength', { n: 8 }))).toBeInTheDocument();
  });

  it('envia los datos recortados con el contrato de POST /users', async () => {
    const { onSubmit, user } = renderCreate();

    await user.type(field('users.form.firstName'), '  Ana ');
    await user.type(field('users.form.lastName'), ' Torres ');
    await user.type(field('users.form.email'), ' ana@empresa.test ');
    await user.type(field('users.form.password'), 'Clave-segura-2026');
    await user.click(screen.getByRole('button', { name: t('common.create') }));

    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onSubmit).toHaveBeenCalledWith({
      email: 'ana@empresa.test',
      password: 'Clave-segura-2026',
      first_name: 'Ana',
      last_name: 'Torres',
    });
  });

  it('traduce el conflicto del backend por su codigo', async () => {
    const onSubmit = vi.fn(async () => {
      throw new ApiError(409, {
        code: ERROR_CODES.CONFLICT,
        message: 'user with this email already exists',
      });
    });
    const { user } = renderCreate(onSubmit);

    await user.type(field('users.form.firstName'), 'Ana');
    await user.type(field('users.form.lastName'), 'Torres');
    await user.type(field('users.form.email'), 'ana@empresa.test');
    await user.type(field('users.form.password'), 'Clave-segura-2026');
    await user.click(screen.getByRole('button', { name: t('common.create') }));

    expect(await screen.findByText(t('users.exists'))).toBeInTheDocument();
  });
});
