import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import {
  suppressionApi,
  type SuppressionCause,
  type SuppressionMeta,
  type SuppressionReason,
} from '@/api/suppression';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t, tEnum } from '@/i18n';
import { CheckTab } from './CheckTab';
import { EntriesTab } from './EntriesTab';

const META: SuppressionMeta = {
  reasons: [
    { reason: 'complaint', severity: 5, removable: true },
    { reason: 'hard_bounce', severity: 4, removable: true },
    { reason: 'unsubscribe', severity: 3, removable: false },
    { reason: 'invalid', severity: 2, removable: true },
    { reason: 'manual', severity: 1, removable: true },
  ],
  manual_reasons: ['manual'],
  max_check_emails: 1000,
  max_import_emails: 10000,
  max_per_page: 100,
  max_email_length: 320,
  max_detail_length: 1000,
};

const EMAIL = 'ana@cliente.com';

function cause(id: string, reason: SuppressionReason, extra: Partial<SuppressionCause> = {}) {
  return {
    id,
    tenant_id: 't',
    email: EMAIL,
    reason,
    source: '',
    detail: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...extra,
  };
}

function grant(...triples: (readonly [string, string, string])[]) {
  const permissions: PermissionTriple[] = triples.map(([module, resource, action]) => ({
    module,
    resource,
    action,
  }));
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.suppression],
    permissions,
  });
}

function renderWithProviders(ui: JSX.Element) {
  return render(
    <MemoryRouter>
      <ToastProvider>{ui}</ToastProvider>
    </MemoryRouter>,
  );
}

const reasonLabel = (reason: SuppressionReason) => tEnum('suppression.reason', reason);

describe('exclusiones con todas sus causas', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  function mockList() {
    return vi.spyOn(suppressionApi, 'list').mockResolvedValue({
      items: [
        {
          ...cause('c-complaint', 'complaint', { source: 'ses', detail: 'queja del proveedor' }),
          reasons: ['complaint', 'unsubscribe'],
          causes: [
            cause('c-complaint', 'complaint', { source: 'ses', detail: 'queja del proveedor' }),
            cause('c-unsub', 'unsubscribe', { source: 'enlace de baja' }),
            cause('c-manual', 'manual', { expires_at: '2020-01-01T00:00:00Z' }),
          ],
        },
      ],
      page: 1,
      perPage: 20,
      total: 1,
      totalPages: 1,
    });
  }

  it('muestra todas las causas con la principal destacada y retira cada una por su id', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.suppressionEntries.read, PERMISSIONS.suppressionEntries.delete);
    mockList();
    const remove = vi.spyOn(suppressionApi, 'remove').mockResolvedValue({ data: null });
    renderWithProviders(<EntriesTab meta={META} />);

    const table = await screen.findByRole('table');
    await within(table).findByText(EMAIL);
    for (const reason of ['complaint', 'unsubscribe', 'manual'] as const) {
      expect(within(table).getByText(reasonLabel(reason))).toBeInTheDocument();
    }
    expect(within(table).getAllByText(t('suppression.cause.primary'))).toHaveLength(1);
    expect(within(table).getByText('ses - queja del proveedor')).toBeInTheDocument();

    const removeLabel = (reason: SuppressionReason) =>
      t('suppression.cause.remove', { reason: reasonLabel(reason), email: EMAIL });
    expect(
      within(table).getByRole('button', { name: removeLabel('complaint') }),
    ).toBeInTheDocument();
    expect(within(table).queryByRole('button', { name: removeLabel('unsubscribe') })).toBeNull();
    expect(within(table).getByTitle(t('suppression.cause.protected'))).toBeInTheDocument();

    await user.click(within(table).getByRole('button', { name: removeLabel('manual') }));
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByText(
        t('suppression.cause.removeConfirmExpired', {
          reason: reasonLabel('manual'),
          email: EMAIL,
        }),
      ),
    ).toBeInTheDocument();
    await user.click(
      within(dialog).getByRole('button', { name: t('suppression.cause.removeAction') }),
    );
    await waitFor(() => expect(remove).toHaveBeenCalledWith('c-manual'));
  });

  it('retirar la ultima causa vigente avisa de que se vuelve a poder enviar', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.suppressionEntries.read, PERMISSIONS.suppressionEntries.delete);
    vi.spyOn(suppressionApi, 'list').mockResolvedValue({
      items: [
        { ...cause('only', 'manual'), reasons: ['manual'], causes: [cause('only', 'manual')] },
      ],
      page: 1,
      perPage: 20,
      total: 1,
      totalPages: 1,
    });
    renderWithProviders(<EntriesTab meta={META} />);

    const table = await screen.findByRole('table');
    await user.click(
      await within(table).findByRole('button', {
        name: t('suppression.cause.remove', { reason: reasonLabel('manual'), email: EMAIL }),
      }),
    );
    expect(
      screen.getByText(
        t('suppression.cause.removeConfirmLast', { reason: reasonLabel('manual'), email: EMAIL }),
      ),
    ).toBeInTheDocument();
  });

  it('sin permiso de borrar no se ofrece retirar ninguna causa', async () => {
    grant(PERMISSIONS.suppressionEntries.read);
    mockList();
    renderWithProviders(<EntriesTab meta={META} />);
    const table = await screen.findByRole('table');
    await within(table).findByText(EMAIL);
    expect(within(table).queryAllByRole('button')).toHaveLength(0);
  });
});

describe('comprobador de direcciones', () => {
  afterEach(() => vi.restoreAllMocks());

  it('muestra todas las causas vigentes de cada direccion excluida', async () => {
    const user = userEvent.setup();
    const check = vi
      .spyOn(suppressionApi, 'check')
      .mockResolvedValue([{ email: EMAIL, reason: 'complaint', reasons: ['complaint', 'manual'] }]);
    renderWithProviders(<CheckTab meta={META} />);

    await user.type(screen.getByRole('textbox'), `${EMAIL}\notra@cliente.com`);
    await user.click(screen.getByRole('button', { name: t('suppression.check.submit') }));

    const table = await screen.findByRole('table');
    expect(check).toHaveBeenCalledWith([EMAIL, 'otra@cliente.com']);
    expect(within(table).getByText(reasonLabel('complaint'))).toBeInTheDocument();
    expect(within(table).getByText(reasonLabel('manual'))).toBeInTheDocument();
    expect(within(table).getByText(t('suppression.cause.primary'))).toBeInTheDocument();
  });
});
