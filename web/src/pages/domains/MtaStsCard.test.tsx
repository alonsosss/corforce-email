import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { domainsApi, type DomainDetail } from '@/api/domains';
import { ApiError } from '@/api/errors';
import { mailDirectoryApi } from '@/api/mailDirectory';
import type { MtaStsMode, MtaStsState } from '@/api/mtaSts';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import DomainDetailPage from './DomainDetailPage';
import { MtaStsCard } from './MtaStsCard';

type Triple = readonly [string, string, string];

/** El ultimo elemento: Array.prototype.at no esta en la lib de TypeScript del proyecto. */
function lastOf<T>(items: T[]): T {
  return items[items.length - 1]!;
}

function grant(...triples: Triple[]) {
  const permissions: PermissionTriple[] = triples.map(([module, resource, action]) => ({
    module,
    resource,
    action,
  }));
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.domains],
    permissions,
  });
}

function ok<T>(data: T): ApiResponse<T> {
  return { data };
}

const NEXT: Record<MtaStsMode, MtaStsMode[]> = {
  none: ['testing'],
  testing: ['none', 'enforce'],
  enforce: ['testing'],
};

function state(mode: MtaStsMode, extra: Partial<MtaStsState> = {}): MtaStsState {
  return {
    domain: 'acme.com',
    domain_active: true,
    mode,
    max_age: mode === 'none' ? 0 : mode === 'enforce' ? 604800 : 86400,
    policy_id: mode === 'none' ? '' : 'a1b2c3d4e5f60718293a4b5c6d7e8f90',
    updated_at: mode === 'none' ? null : '2026-09-21T10:00:00Z',
    allowed_modes: NEXT[mode],
    ...extra,
  };
}

function renderCard(onChanged = vi.fn()) {
  return render(
    <ToastProvider>
      <MtaStsCard domain="acme.com" onChanged={onChanged} />
    </ToastProvider>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('MTA-STS en la ficha del dominio', () => {
  it('muestra el modo, la version y la vigencia que da el servicio', async () => {
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    grant(PERMISSIONS.mtaSts.read);
    renderCard();

    expect(await screen.findByText(t('domains.mtaSts.mode.testing'))).toBeInTheDocument();
    expect(screen.getByText('a1b2c3d4e5f60718293a4b5c6d7e8f90')).toBeInTheDocument();
    expect(screen.getByText(t('domains.mtaSts.maxAgeDays', { days: 1 }))).toBeInTheDocument();
    expect(screen.getByText(t('domains.mtaSts.enforceRisk'))).toBeInTheDocument();
  });

  it('sin permiso de cambio no ofrece ningun paso', async () => {
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    grant(PERMISSIONS.mtaSts.read);
    renderCard();

    await screen.findByText(t('domains.mtaSts.mode.testing'));
    expect(
      screen.queryByRole('button', { name: t('domains.mtaSts.enforce') }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('domains.mtaSts.deactivate') }),
    ).not.toBeInTheDocument();
  });

  it('desde none solo ofrece activar en prueba, sin pedir confirmacion', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('none')));
    const set = vi.spyOn(mailDirectoryApi, 'setMtaSts').mockResolvedValue(ok(state('testing')));
    const onChanged = vi.fn();
    grant(PERMISSIONS.mtaSts.read, PERMISSIONS.mtaSts.update);
    renderCard(onChanged);

    expect(
      screen.queryByRole('button', { name: t('domains.mtaSts.enforce') }),
    ).not.toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: t('domains.mtaSts.activate') }));

    await waitFor(() => expect(set).toHaveBeenCalledWith('acme.com', 'testing'));
    expect(await screen.findByText(t('domains.mtaSts.mode.testing'))).toBeInTheDocument();
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('pasar a enforce pide confirmar el aviso y solo entonces lo envia', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    const set = vi.spyOn(mailDirectoryApi, 'setMtaSts').mockResolvedValue(ok(state('enforce')));
    const onChanged = vi.fn();
    grant(PERMISSIONS.mtaSts.read, PERMISSIONS.mtaSts.update);
    renderCard(onChanged);

    await user.click(await screen.findByRole('button', { name: t('domains.mtaSts.enforce') }));
    expect(set).not.toHaveBeenCalled();
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(t('domains.mtaSts.enforceConfirm', { domain: 'acme.com' }));

    await user.click(lastOf(screen.getAllByRole('button', { name: t('domains.mtaSts.enforce') })));
    await waitFor(() => expect(set).toHaveBeenCalledWith('acme.com', 'enforce'));
    expect(await screen.findByText(t('domains.mtaSts.mode.enforce'))).toBeInTheDocument();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it('cancelar la confirmacion no cambia nada', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    const set = vi.spyOn(mailDirectoryApi, 'setMtaSts');
    grant(PERMISSIONS.mtaSts.read, PERMISSIONS.mtaSts.update);
    renderCard();

    await user.click(await screen.findByRole('button', { name: t('domains.mtaSts.enforce') }));
    await user.click(await screen.findByRole('button', { name: t('common.cancel') }));
    expect(set).not.toHaveBeenCalled();
    expect(screen.getByText(t('domains.mtaSts.mode.testing'))).toBeInTheDocument();
  });

  it('si el servicio rechaza enforce muestra su motivo dentro de la confirmacion y no cambia el modo', async () => {
    const user = userEvent.setup();
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    vi.spyOn(mailDirectoryApi, 'setMtaSts').mockRejectedValue(
      new ApiError(409, {
        code: 'CONFLICT',
        message: 'los MX publicados del dominio no son los de la plataforma',
      }),
    );
    const onChanged = vi.fn();
    grant(PERMISSIONS.mtaSts.read, PERMISSIONS.mtaSts.update);
    renderCard(onChanged);

    await user.click(await screen.findByRole('button', { name: t('domains.mtaSts.enforce') }));
    await user.click(lastOf(screen.getAllByRole('button', { name: t('domains.mtaSts.enforce') })));

    expect(
      await screen.findByText(/los MX publicados del dominio no son los de la plataforma/),
    ).toBeInTheDocument();
    expect(screen.getByText(t('domains.mtaSts.mode.testing'))).toBeInTheDocument();
    expect(onChanged).not.toHaveBeenCalled();
  });

  it('en enforce solo ofrece volver a prueba, y de un dominio inactivo no ofrece enforce', async () => {
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('enforce')));
    grant(PERMISSIONS.mtaSts.read, PERMISSIONS.mtaSts.update);
    const { unmount } = renderCard();
    expect(
      await screen.findByRole('button', { name: t('domains.mtaSts.backToTesting') }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('domains.mtaSts.deactivate') }),
    ).not.toBeInTheDocument();
    unmount();

    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(
      ok(state('testing', { domain_active: false, allowed_modes: ['none'] })),
    );
    renderCard();
    expect(
      await screen.findByRole('button', { name: t('domains.mtaSts.deactivate') }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('domains.mtaSts.enforce') }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(t('domains.mtaSts.enforceNeedsActive'))).toBeInTheDocument();
  });

  it('un dominio que el directorio no tiene explica que primero se verifica', async () => {
    vi.spyOn(mailDirectoryApi, 'getMtaSts').mockRejectedValue(
      new ApiError(404, { code: 'NOT_FOUND', message: 'recurso no encontrado' }),
    );
    grant(PERMISSIONS.mtaSts.read, PERMISSIONS.mtaSts.update);
    renderCard();
    expect(await screen.findByText(t('domains.mtaSts.notInDirectory'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('domains.mtaSts.activate') }),
    ).not.toBeInTheDocument();
  });
});

const DOMAIN_ID = 'b7c1f0de-0000-4000-8000-000000000003';

function detail(purpose: DomainDetail['purpose']): DomainDetail {
  return {
    id: DOMAIN_ID,
    domain: 'acme.com',
    purpose,
    status: 'verified',
    verification_token: '0123456789abcdef0123456789abcdef',
    verified_at: '2026-09-01T10:00:00Z',
    last_checked_at: '2026-09-12T09:00:00Z',
    dkim_selector: 'cfm202609',
    dkim_public_key: 'PUB',
    dkim_key_bits: 2048,
    dkim_previous_selector: null,
    dkim_rotated_at: null,
    dkim_previous_until: null,
    dkim_revocation_pending: false,
    dmarc_policy: 'quarantine',
    dns_mode: 'manual',
    dns_published_at: null,
    ses_identity_status: null,
    ses_dkim_status: null,
    ses_mail_from_status: null,
    ses_checked_at: null,
    ses_last_error: null,
    created_at: '2026-09-01T10:00:00Z',
    updated_at: '2026-09-12T09:00:00Z',
    dns_records: [
      {
        record: 'mta_sts',
        type: 'TXT',
        host: '_mta-sts.acme.com',
        value: 'v=STSv1; id=a1b2c3d4e5f60718293a4b5c6d7e8f90',
        required: false,
      },
    ],
    dns_checks: [],
    dkim_rotations: [],
  };
}

function renderPage() {
  return render(
    <ToastProvider>
      <MemoryRouter initialEntries={[paths.domain(DOMAIN_ID)]}>
        <Routes>
          <Route path={`${paths.domains}/:id`} element={<DomainDetailPage />} />
        </Routes>
      </MemoryRouter>
    </ToastProvider>,
  );
}

describe('MTA-STS en la ficha', () => {
  it('aparece con el permiso de lectura en un dominio que recibe correo, con su TXT en la tabla', async () => {
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(detail('corporate')));
    const get = vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    grant(PERMISSIONS.domains.read, PERMISSIONS.mtaSts.read);
    renderPage();

    expect(await screen.findByText(t('domains.mtaSts.mode.testing'))).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith('acme.com');
    expect(screen.getByText('_mta-sts.acme.com')).toBeInTheDocument();
    expect(
      screen.getByText(t('domains.record.mta_sts'), { selector: 'strong' }),
    ).toBeInTheDocument();
  });

  it('no aparece sin el permiso ni en un dominio solo de envio, y no pide nada', async () => {
    const get = vi.spyOn(mailDirectoryApi, 'getMtaSts').mockResolvedValue(ok(state('testing')));
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(detail('corporate')));
    grant(PERMISSIONS.domains.read);
    const { unmount } = renderPage();
    await screen.findByText('acme.com', { selector: 'h1, h2' });
    expect(screen.queryByText(t('domains.mtaSts.description'))).not.toBeInTheDocument();
    unmount();

    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(detail('sending')));
    grant(PERMISSIONS.domains.read, PERMISSIONS.mtaSts.read);
    renderPage();
    await screen.findByText('acme.com', { selector: 'h1, h2' });
    expect(screen.queryByText(t('domains.mtaSts.description'))).not.toBeInTheDocument();
    expect(get).not.toHaveBeenCalled();
  });
});
