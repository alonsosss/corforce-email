import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import type { CreateMigrationRequest } from '@/api/mailMigration';
import { t } from '@/i18n';
import { MigrationForm } from './MigrationForm';
import { META } from './migrationFixtures';

const PASSWORD = 'secreto-de-origen';

function renderForm(onSubmit: (request: CreateMigrationRequest) => Promise<void>) {
  render(<MigrationForm mailboxId="mb-1" meta={META} onSubmit={onSubmit} />);
}

const field = (key: Parameters<typeof t>[0]) => screen.getByLabelText(new RegExp(`^${t(key)}`));

async function fill(user: ReturnType<typeof userEvent.setup>) {
  await user.type(field('migration.form.host'), 'imap.origen.test');
  await user.type(field('migration.form.username'), 'ana@origen.test');
  await user.type(field('migration.form.password'), PASSWORD);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('formulario de migracion', () => {
  it('la contrasena es un campo de contrasena que el navegador no autocompleta', () => {
    renderForm(vi.fn());
    const password = field('migration.form.password');
    expect(password).toHaveAttribute('type', 'password');
    expect(password).toHaveAttribute('autocomplete', 'new-password');
  });

  it('los puertos y los modos TLS son los del servicio y el TLS sigue al puerto', async () => {
    const user = userEvent.setup();
    renderForm(vi.fn());
    const port = field('migration.form.port');
    const tls = field('migration.form.tls');
    expect(Array.from((port as HTMLSelectElement).options).map((o) => o.value)).toEqual([
      '143',
      '993',
    ]);
    expect(port).toHaveValue('993');
    expect(tls).toHaveValue('ssl');
    await user.selectOptions(port, '143');
    expect(tls).toHaveValue('starttls');
  });

  it('avisa que la contrasena se borra al terminar y que hace falta contrasena de aplicacion', () => {
    renderForm(vi.fn());
    expect(screen.getByText(t('migration.form.passwordNotice'))).toBeInTheDocument();
    expect(screen.getByText(t('migration.form.appPasswordNotice'))).toBeInTheDocument();
  });

  it('no envia nada si falta un dato y lo senala', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    renderForm(onSubmit);
    await user.click(screen.getByRole('button', { name: t('migration.form.submit') }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getAllByText(t('validation.required')).length).toBeGreaterThanOrEqual(3);
  });

  it('borra la contrasena del estado en cuanto envia, sin esperar la respuesta', async () => {
    const user = userEvent.setup();
    let resolve: () => void = () => undefined;
    const onSubmit = vi.fn(
      () =>
        new Promise<void>((done) => {
          resolve = done;
        }),
    );
    renderForm(onSubmit);
    await fill(user);
    await user.click(screen.getByRole('button', { name: t('migration.form.submit') }));

    expect(onSubmit).toHaveBeenCalledWith({
      mailbox_id: 'mb-1',
      source_host: 'imap.origen.test',
      source_port: 993,
      source_tls: 'ssl',
      source_username: 'ana@origen.test',
      source_password: PASSWORD,
    });
    expect(field('migration.form.password')).toHaveValue('');
    expect(field('migration.form.host')).toHaveValue('imap.origen.test');
    resolve();
    await waitFor(() =>
      expect(screen.getByRole('button', { name: t('migration.form.submit') })).toBeEnabled(),
    );
  });

  it('tambien la borra si el servidor rechaza, muestra solo el texto del codigo y devuelve el foco', async () => {
    const user = userEvent.setup();
    const onSubmit = vi
      .fn()
      .mockRejectedValue(
        new ApiError(422, { code: 'SOURCE_HOST_NOT_ALLOWED', message: `detalle con ${PASSWORD}` }),
      );
    renderForm(onSubmit);
    await fill(user);
    await user.click(screen.getByRole('button', { name: t('migration.form.submit') }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(t('migration.error.SOURCE_HOST_NOT_ALLOWED'));
    expect(document.body).not.toHaveTextContent(PASSWORD);
    expect(field('migration.form.password')).toHaveValue('');
    expect(field('migration.form.password')).toHaveFocus();
    expect(field('migration.form.username')).toHaveValue('ana@origen.test');
  });

  it('la contrasena no se guarda en el almacenamiento del navegador', async () => {
    const user = userEvent.setup();
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    renderForm(vi.fn().mockResolvedValue(undefined));
    await fill(user);
    await user.click(screen.getByRole('button', { name: t('migration.form.submit') }));
    expect(setItem).not.toHaveBeenCalled();
  });
});
