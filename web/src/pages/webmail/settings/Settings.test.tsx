import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type MailFilters, type Signature } from '@/api/webmail';
import { t } from '@/i18n';
import { useWebmailStore } from '@/webmail/store';
import SettingsPage from '../SettingsPage';
import { CLIENTES, INBOX, renderScreen } from '../testing';

const SIGNATURE: Signature = {
  enabled: true,
  html: '<p>Ana</p>',
  text: 'Ana',
  on_replies: false,
  updated_at: null,
  limits: { max_html_bytes: 64, max_text_bytes: 64 },
};

const FILTERS: MailFilters = {
  rules: [],
  forwarding: { enabled: false, addresses: [], keep_copy: true },
  updated_at: null,
  limits: {
    max_rules: 5,
    max_conditions: 2,
    max_actions: 2,
    max_forward_addresses: 2,
    max_value_length: 10,
    max_name_length: 12,
    max_folder_bytes: 30,
  },
};

function validation(field: string, message: string) {
  return new ApiError(
    422,
    { code: 'VALIDATION_ERROR', message },
    { error: { code: 'VALIDATION_ERROR', message, details: { field } } },
  );
}

function renderTab(tab: string) {
  renderScreen(<SettingsPage />, {
    path: '/webmail/settings',
    url: `/webmail/settings?tab=${tab}`,
  });
}

describe('ajustes: firma', () => {
  afterEach(() => vi.restoreAllMocks());

  it('guarda la firma con formato y el uso en respuestas', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    const save = vi
      .spyOn(webmailApi, 'setSignature')
      .mockImplementation(async (input) => ({ ...SIGNATURE, ...input }));
    renderTab('signature');

    await user.click(await screen.findByLabelText(t('webmail.signature.onReplies')));
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    await waitFor(() =>
      expect(save).toHaveBeenCalledWith({ enabled: true, html: '<p>Ana</p>', on_replies: true }),
    );
    expect(await screen.findByText(t('webmail.signature.saved'))).toBeInTheDocument();
  });

  it('un 422 del contenido se muestra junto al editor', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'signature').mockResolvedValue(SIGNATURE);
    vi.spyOn(webmailApi, 'setSignature').mockRejectedValue(
      validation('html', 'la firma activa no puede estar vacia'),
    );
    renderTab('signature');

    await user.click(await screen.findByRole('button', { name: t('common.save') }));
    expect(await screen.findByText('la firma activa no puede estar vacia')).toBeInTheDocument();
  });
});

describe('ajustes: reglas y reenvio', () => {
  beforeEach(() => {
    useWebmailStore.setState({
      session: {
        username: 'ana@empresa.com',
        display_name: 'Ana',
        expires_at: '',
        idle_timeout_seconds: 1800,
        quota: null,
      },
    });
    vi.spyOn(webmailApi, 'folders').mockResolvedValue([INBOX, CLIENTES]);
  });
  afterEach(() => vi.restoreAllMocks());

  it('crea una regla que mueve a una carpeta elegida del buzon', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'filters').mockResolvedValue(FILTERS);
    const save = vi.spyOn(webmailApi, 'setFilters').mockImplementation(async (input) => ({
      ...FILTERS,
      ...input,
    }));
    renderTab('rules');

    await user.click(await screen.findByRole('button', { name: t('webmail.rules.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(t('webmail.rules.name')), 'Clientes');
    await user.type(within(dialog).getByLabelText(t('webmail.rules.value')), 'acme');
    await user.selectOptions(within(dialog).getByLabelText(t('webmail.rules.folder')), 'Clientes');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toEqual({
      rules: [
        {
          id: '',
          name: 'Clientes',
          enabled: true,
          match: 'all',
          conditions: [{ field: 'from', op: 'contains', value: 'acme' }],
          actions: [{ type: 'move', folder: 'Clientes' }],
          stop: false,
        },
      ],
      forwarding: FILTERS.forwarding,
    });
  });

  it('valida con los topes servidos antes de enviar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'filters').mockResolvedValue(FILTERS);
    const save = vi.spyOn(webmailApi, 'setFilters');
    renderTab('rules');

    await user.click(await screen.findByRole('button', { name: t('webmail.rules.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(t('webmail.rules.name')), 'Nombre larguisimo');
    await user.type(within(dialog).getByLabelText(t('webmail.rules.value')), 'valor demasiado');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.rules.addCondition') }));
    // Con el tope de condiciones alcanzado no se ofrece otra.
    expect(
      within(dialog).getByRole('button', { name: t('webmail.rules.addCondition') }),
    ).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));

    expect(within(dialog).getByText(t('webmail.rules.tooLong', { max: 12 }))).toBeInTheDocument();
    expect(within(dialog).getByText(t('webmail.rules.tooLong', { max: 10 }))).toBeInTheDocument();
    expect(within(dialog).getByText(t('webmail.rules.valueRequired'))).toBeInTheDocument();
    expect(save).not.toHaveBeenCalled();
  });

  it('un 422 del directorio se pinta junto al campo de details.field', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'filters').mockResolvedValue(FILTERS);
    vi.spyOn(webmailApi, 'setFilters').mockRejectedValue(
      validation('rules[0].conditions[0].value', 'texto no admitido'),
    );
    renderTab('rules');

    await user.click(await screen.findByRole('button', { name: t('webmail.rules.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(t('webmail.rules.name')), 'Regla');
    await user.type(within(dialog).getByLabelText(t('webmail.rules.value')), 'x');
    await user.selectOptions(within(dialog).getByLabelText(t('webmail.rules.folder')), 'Clientes');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));

    const value = within(dialog).getByLabelText(t('webmail.rules.value'));
    expect(await within(dialog).findByText('texto no admitido')).toBeInTheDocument();
    expect(value).toHaveAttribute('aria-invalid', 'true');
  });

  it('guarda el reenvio conservando las reglas; un 422 de una direccion va a la lista', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'filters').mockResolvedValue(FILTERS);
    const save = vi
      .spyOn(webmailApi, 'setFilters')
      .mockRejectedValueOnce(validation('forwarding.addresses[0]', 'no se puede reenviar ahi'))
      .mockImplementation(async (input) => ({ ...FILTERS, ...input }));
    renderTab('forwarding');

    await user.click(await screen.findByLabelText(t('webmail.forwarding.enabled')));
    await user.type(
      screen.getByLabelText(t('webmail.forwarding.addresses')),
      'otro@correo.com{Enter}',
    );
    await user.click(screen.getByRole('button', { name: t('common.save') }));
    expect(await screen.findByText('no se puede reenviar ahi')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: t('common.save') }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
    expect(save.mock.calls[1]?.[0]).toEqual({
      rules: [],
      forwarding: { enabled: true, addresses: ['otro@correo.com'], keep_copy: true },
    });
  });
});

describe('ajustes: contrasena', () => {
  afterEach(() => vi.restoreAllMocks());

  it('tras cambiarla la sesion queda cerrada y lleva al acceso con aviso', async () => {
    const user = userEvent.setup();
    useWebmailStore.setState({ status: 'authenticated', passwordChanged: false });
    const change = vi.spyOn(webmailApi, 'changePassword').mockResolvedValue(null);
    renderTab('password');

    await user.type(screen.getByLabelText(new RegExp(t('webmail.password.current'))), 'vieja');
    await user.type(screen.getByLabelText(new RegExp(`^${t('webmail.password.new')}`)), 'nueva-1');
    await user.type(screen.getByLabelText(new RegExp(t('webmail.password.repeat'))), 'nueva-1');
    await user.click(screen.getByRole('button', { name: t('webmail.password.submit') }));

    await waitFor(() => expect(change).toHaveBeenCalledWith('vieja', 'nueva-1'));
    expect(useWebmailStore.getState()).toMatchObject({
      status: 'anonymous',
      passwordChanged: true,
    });
    expect(screen.getByTestId('location').textContent).toBe('/webmail/login');
  });

  it('no envia si no coinciden y senala la actual incorrecta', async () => {
    const user = userEvent.setup();
    useWebmailStore.setState({ status: 'authenticated' });
    const change = vi
      .spyOn(webmailApi, 'changePassword')
      .mockRejectedValue(new ApiError(401, { code: 'INVALID_CREDENTIALS', message: '' }));
    renderTab('password');

    await user.type(screen.getByLabelText(new RegExp(t('webmail.password.current'))), 'mala');
    await user.type(screen.getByLabelText(new RegExp(`^${t('webmail.password.new')}`)), 'nueva-1');
    await user.type(screen.getByLabelText(new RegExp(t('webmail.password.repeat'))), 'otra');
    await user.click(screen.getByRole('button', { name: t('webmail.password.submit') }));
    expect(screen.getByText(t('webmail.password.mismatch'))).toBeInTheDocument();
    expect(change).not.toHaveBeenCalled();

    await user.clear(screen.getByLabelText(new RegExp(t('webmail.password.repeat'))));
    await user.type(screen.getByLabelText(new RegExp(t('webmail.password.repeat'))), 'nueva-1');
    await user.click(screen.getByRole('button', { name: t('webmail.password.submit') }));
    expect(await screen.findByText(t('webmail.password.currentWrong'))).toBeInTheDocument();
    expect(useWebmailStore.getState().status).toBe('authenticated');
  });
});
