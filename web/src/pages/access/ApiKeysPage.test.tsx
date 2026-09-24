import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MODULES } from '@/access/modules';
import { useAccessStore } from '@/access/store';
import type * as ApiKeysModule from '@/api/apiKeys';
import { apiKeysApi, type ApiKey, type ApiKeyScope } from '@/api/apiKeys';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import ApiKeysPage from './ApiKeysPage';

vi.mock('@/api/apiKeys', async (importOriginal) => {
  const original = await importOriginal<typeof ApiKeysModule>();
  return {
    ...original,
    apiKeysApi: {
      list: vi.fn(),
      settings: vi.fn(),
      scopes: vi.fn(),
      create: vi.fn(),
      revoke: vi.fn(),
    },
  };
});

const api = vi.mocked(apiKeysApi);

const SEND_SCOPE: ApiKeyScope = {
  module: 'transactional',
  resource: 'messages',
  action: 'create',
  description: 'Enviar correo transaccional',
};
const READ_SCOPE: ApiKeyScope = {
  module: 'transactional',
  resource: 'messages',
  action: 'read',
  description: 'Ver correo transaccional',
};

function keyOf(overrides: Partial<ApiKey> = {}): ApiKey {
  return {
    id: 'key-1',
    name: 'Tienda en linea',
    prefix: 'cfm_ab12cd34',
    scopes: [SEND_SCOPE],
    status: 'active',
    created_by: '00000000-0000-0000-0000-000000000001',
    created_at: '2026-09-01T10:00:00Z',
    expires_at: null,
    revoked_at: null,
    revoked_by: null,
    last_used_at: '2026-09-20T08:30:00Z',
    last_used_ip: '203.0.113.7',
    ...overrides,
  };
}

const SECRET = 'cfm_ef56gh78_s3cr3tv4lu3';
const SMTP = { host: 'smtp.ejemplo.test', starttls_port: 587, tls_port: 465 };

function grant(...actions: string[]) {
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.access],
    permissions: actions.map((action) => ({
      module: MODULES.access,
      resource: 'api_keys',
      action,
    })),
  });
}

function mount() {
  return render(
    <ToastProvider>
      <ApiKeysPage />
    </ToastProvider>,
  );
}

beforeEach(() => {
  grant('read', 'create', 'revoke');
  api.list.mockResolvedValue([keyOf()]);
  api.settings.mockResolvedValue({ smtp: SMTP });
  api.scopes.mockResolvedValue([SEND_SCOPE, READ_SCOPE]);
});

afterEach(() => {
  vi.resetAllMocks();
  useAccessStore.getState().reset();
});

async function openForm(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: t('apiKeys.new') }));
  const dialog = await screen.findByRole('dialog', { name: t('apiKeys.form.title') });
  await user.type(within(dialog).getByLabelText(new RegExp(`^${t('common.name')}`)), 'Tienda');
  await user.click(within(dialog).getByRole('checkbox', { name: SEND_SCOPE.description }));
  return dialog;
}

describe('claves de API', () => {
  it('lista las claves con prefijo, permisos, estado, caducidad y ultimo uso', async () => {
    api.list.mockResolvedValue([
      keyOf(),
      keyOf({
        id: 'key-2',
        name: 'Antigua',
        prefix: 'cfm_zz99yy88',
        status: 'revoked',
        last_used_at: null,
        last_used_ip: '',
        expires_at: '2027-01-01T00:00:00Z',
      }),
    ]);
    mount();
    const active = (await screen.findByText('Tienda en linea')).closest('tr') as HTMLElement;
    expect(within(active).getByText('cfm_ab12cd34')).toBeInTheDocument();
    expect(within(active).getByText(SEND_SCOPE.description!)).toBeInTheDocument();
    expect(within(active).getByText(t('apiKeys.status.active'))).toBeInTheDocument();
    expect(within(active).getByText(t('apiKeys.neverExpires'))).toBeInTheDocument();
    expect(within(active).getByText('203.0.113.7')).toBeInTheDocument();
    expect(within(active).getByRole('button', { name: t('apiKeys.revoke') })).toBeInTheDocument();

    const revoked = screen.getByText('Antigua').closest('tr') as HTMLElement;
    expect(within(revoked).getByText(t('apiKeys.status.revoked'))).toBeInTheDocument();
    expect(within(revoked).getByText(t('apiKeys.neverUsed'))).toBeInTheDocument();
    expect(within(revoked).queryByRole('button', { name: t('apiKeys.revoke') })).toBeNull();

    expect(screen.getByText(SMTP.host)).toBeInTheDocument();
    expect(screen.getByText('587')).toBeInTheDocument();
  });

  it('sin claves muestra el estado vacio y sin SMTP no muestra el bloque', async () => {
    api.list.mockResolvedValue([]);
    api.settings.mockResolvedValue({ smtp: null });
    mount();
    expect(await screen.findByText(t('apiKeys.empty'))).toBeInTheDocument();
    expect(screen.queryByText(t('apiKeys.smtp.title'))).toBeNull();
  });

  it('un error al listar se muestra con reintento', async () => {
    api.list.mockRejectedValueOnce(new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: '' }));
    mount();
    const retry = await screen.findByRole('button', { name: t('common.retry') });
    api.list.mockResolvedValue([keyOf()]);
    await userEvent.setup().click(retry);
    expect(await screen.findByText('Tienda en linea')).toBeInTheDocument();
  });

  it('sin permisos de crear ni revocar no aparecen sus botones', async () => {
    grant('read');
    mount();
    expect(await screen.findByText('Tienda en linea')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('apiKeys.new') })).toBeNull();
    expect(screen.queryByRole('button', { name: t('apiKeys.revoke') })).toBeNull();
  });

  it('sin permiso de lectura no pide nada', () => {
    grant('create');
    mount();
    expect(screen.getByText(t('common.missingPermission'))).toBeInTheDocument();
    expect(api.list).not.toHaveBeenCalled();
    expect(api.settings).not.toHaveBeenCalled();
  });

  it('los permisos del formulario vienen de /scopes y se exige al menos uno', async () => {
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole('button', { name: t('apiKeys.new') }));
    const dialog = await screen.findByRole('dialog', { name: t('apiKeys.form.title') });
    expect(within(dialog).getAllByRole('checkbox')).toHaveLength(2);
    await user.type(within(dialog).getByLabelText(new RegExp(`^${t('common.name')}`)), 'Tienda');
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));
    expect(within(dialog).getByText(t('apiKeys.form.scopesRequired'))).toBeInTheDocument();
    expect(api.create).not.toHaveBeenCalled();
  });

  it('crear muestra el secreto una sola vez con los datos de uso y lo descarta al cerrar', async () => {
    const user = userEvent.setup();
    api.create.mockResolvedValue({
      key: keyOf({ id: 'key-3', name: 'Tienda', prefix: 'cfm_ef56gh78' }),
      secret: SECRET,
    });
    mount();
    const form = await openForm(user);
    await user.click(within(form).getByRole('button', { name: t('common.create') }));

    expect(api.create).toHaveBeenCalledWith({
      name: 'Tienda',
      scopes: [{ module: 'transactional', resource: 'messages', action: 'create' }],
      expires_at: null,
    });
    const dialog = await screen.findByRole('dialog', {
      name: t('apiKeys.secret.title', { name: 'Tienda' }),
    });
    expect(within(dialog).getByLabelText(t('apiKeys.secret.valueLabel'))).toHaveTextContent(SECRET);
    expect(within(dialog).getByText(t('apiKeys.secret.warningTitle'))).toBeInTheDocument();
    expect(within(dialog).getByLabelText(t('apiKeys.secret.apiHeaderLabel'))).toHaveTextContent(
      `Authorization: Bearer ${SECRET}`,
    );
    expect(within(dialog).getByText(/\/api\/v1\/transactional\/messages$/)).toBeInTheDocument();
    expect(within(dialog).getByText(SMTP.host)).toBeInTheDocument();
    expect(within(dialog).getByText('465')).toBeInTheDocument();
    expect(within(dialog).getByText('cfm_ef56gh78')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: t('common.close') })).toBeNull();

    await user.click(within(dialog).getByRole('button', { name: t('apiKeys.secret.done') }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByText(SECRET, { exact: false })).toBeNull();
    expect(api.list).toHaveBeenCalledTimes(2);
  });

  it('una caducidad pasada no se envia', async () => {
    const user = userEvent.setup();
    mount();
    const form = await openForm(user);
    await user.type(within(form).getByLabelText(t('apiKeys.form.expiresAt')), '2020-01-01T10:00');
    await user.click(within(form).getByRole('button', { name: t('common.create') }));
    expect(within(form).getByText(t('apiKeys.form.expiresFuture'))).toBeInTheDocument();
    expect(api.create).not.toHaveBeenCalled();
  });

  it('si no se reconfirma la identidad, lo dice y no muestra ningun secreto', async () => {
    const user = userEvent.setup();
    api.create.mockRejectedValue(
      new ApiError(403, { code: ERROR_CODES.STEP_UP_REQUIRED, message: 'step-up required' }),
    );
    mount();
    const form = await openForm(user);
    await user.click(within(form).getByRole('button', { name: t('common.create') }));
    expect(await within(form).findByRole('alert')).toHaveTextContent(t('error.stepUpCancelled'));
    expect(screen.queryByText(t('apiKeys.secret.warningTitle'))).toBeNull();
  });

  it('el limite de claves se explica con su texto', async () => {
    const user = userEvent.setup();
    api.create.mockRejectedValue(
      new ApiError(409, { code: 'API_KEY_LIMIT', message: 'la empresa ya tiene 50 claves' }),
    );
    mount();
    const form = await openForm(user);
    await user.click(within(form).getByRole('button', { name: t('common.create') }));
    expect(await within(form).findByRole('alert')).toHaveTextContent(t('apiKeys.error.limit'));
  });

  it('revocar pide confirmacion, avisa de que es inmediato y actualiza la fila', async () => {
    const user = userEvent.setup();
    api.revoke.mockResolvedValue(keyOf({ status: 'revoked', revoked_at: '2026-09-24T00:00:00Z' }));
    mount();
    await user.click(await screen.findByRole('button', { name: t('apiKeys.revoke') }));
    const dialog = await screen.findByRole('dialog', { name: t('apiKeys.revokeTitle') });
    expect(
      within(dialog).getByText(
        t('apiKeys.revokeConfirm', { name: 'Tienda en linea', prefix: 'cfm_ab12cd34' }),
      ),
    ).toBeInTheDocument();
    expect(api.revoke).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole('button', { name: t('apiKeys.revoke') }));
    expect(api.revoke).toHaveBeenCalledWith('key-1');
    expect(await screen.findByText(t('apiKeys.status.revoked'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('apiKeys.revoke') })).toBeNull();
  });

  it('cancelar la revocacion no llama al servidor', async () => {
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole('button', { name: t('apiKeys.revoke') }));
    const dialog = await screen.findByRole('dialog', { name: t('apiKeys.revokeTitle') });
    await user.click(within(dialog).getByRole('button', { name: t('common.cancel') }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(api.revoke).not.toHaveBeenCalled();
  });
});
