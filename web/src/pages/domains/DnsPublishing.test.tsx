import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import {
  dnsProvidersApi,
  domainsApi,
  type DnsProviderStatus,
  type DnsPublication,
  type DomainDetail,
  type PublishDnsResult,
} from '@/api/domains';
import { ApiError } from '@/api/errors';
import { identityApi, type User } from '@/api/identity';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { useAuthStore } from '@/auth/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { DnsProviderCard } from './DnsProviderCard';
import DomainDetailPage from './DomainDetailPage';

type Triple = readonly [string, string, string];

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

const DOMAIN_ID = 'b7c1f0de-0000-4000-8000-000000000002';
const TOKEN = 'cf_token_pegado_por_el_usuario_0123456789';

function domainFixture(extra: Partial<DomainDetail> = {}): DomainDetail {
  return {
    id: DOMAIN_ID,
    domain: 'acme.com',
    purpose: 'corporate',
    status: 'pending',
    verification_token: '0123456789abcdef0123456789abcdef',
    verified_at: null,
    last_checked_at: null,
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
    created_at: '2026-09-01T10:00:00Z',
    updated_at: '2026-09-12T09:00:00Z',
    dns_records: [
      {
        record: 'spf',
        type: 'TXT',
        host: 'acme.com',
        value: 'v=spf1 include:spf.plataforma.example -all',
        required: true,
      },
    ],
    dns_checks: [],
    dkim_rotations: [],
    ...extra,
  };
}

function publication(extra: Partial<DnsPublication> = {}): DnsPublication {
  return {
    provider: 'cloudflare',
    zone: 'acme.com',
    published_at: '2026-09-17T10:00:00Z',
    complete: true,
    records: [
      { record: 'ownership_txt', type: 'TXT', host: '_cfm-verify.acme.com', value: 'x', action: 'created', existing: [] },
      { record: 'spf', type: 'TXT', host: 'acme.com', value: 'v=spf1', action: 'unchanged', existing: [] },
    ],
    removed: [],
    kept: [],
    ...extra,
  };
}

function publishResult(pub: DnsPublication, extra: Partial<DomainDetail> = {}): PublishDnsResult {
  const { dkim_rotations: _rotations, ...domain } = domainFixture({ dns_mode: 'cloudflare', ...extra });
  return { ...domain, dns_publication: pub, outcome: 'failed', integration_errors: [] };
}

function renderDetail() {
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

function renderProviderCard() {
  return render(
    <ToastProvider>
      <DnsProviderCard />
    </ToastProvider>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  useAuthStore.setState({ userId: null, user: null });
});

describe('modo manual', () => {
  it('sigue mostrando los registros a publicar y no escribe en ningun proveedor', async () => {
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture()));
    const publish = vi.spyOn(domainsApi, 'publishDns');
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.verify);
    renderDetail();

    expect(await screen.findByText(t('domains.dnsMode.manual'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.dns.manualHint'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.records.title'))).toBeInTheDocument();
    expect(screen.getAllByText(/v=spf1 include:spf\.plataforma\.example -all/).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: t('domains.verify') })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('domains.dns.publish') })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('domains.dns.useCloudflare') })).not.toBeInTheDocument();
    expect(publish).not.toHaveBeenCalled();
  });

  it('con publish_dns ofrece pasar a Cloudflare y explica si no esta conectado', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture()));
    const setMode = vi
      .spyOn(domainsApi, 'setDnsMode')
      .mockRejectedValue(
        new ApiError(409, { code: 'DNS_PROVIDER_NOT_CONNECTED', message: 'no conectado' }),
      );
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.publishDns);
    renderDetail();

    await user.click(await screen.findByRole('button', { name: t('domains.dns.useCloudflare') }));
    expect(setMode).toHaveBeenCalledWith(DOMAIN_ID, 'cloudflare');
    expect(await screen.findByText(t('error.code.DNS_PROVIDER_NOT_CONNECTED'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.dnsMode.manual'))).toBeInTheDocument();
  });
});

const CONNECTED_BY = 'b7c1f0de-0000-4000-8000-0000000000bb';

function userFixture(extra: Partial<User> = {}): User {
  return {
    id: CONNECTED_BY,
    email: 'ana.torres@acme.com.pe',
    first_name: 'Ana',
    last_name: 'Torres',
    avatar_url: null,
    status: 'active',
    mfa_enabled: true,
    last_login_at: null,
    created_at: '2026-09-01T10:00:00Z',
    updated_at: '2026-09-01T10:00:00Z',
    ...extra,
  };
}

function connectedStatus(): ApiResponse<DnsProviderStatus> {
  return ok<DnsProviderStatus>({
    provider: 'cloudflare',
    connected: true,
    token_hint: '6789',
    zones: ['acme.com'],
    zones_visible: 1,
    connected_by: CONNECTED_BY,
    connected_at: '2026-09-17T10:00:00Z',
    last_validated_at: '2026-09-17T10:00:00Z',
  });
}

describe('quien conecto Cloudflare', () => {
  it('con users/read muestra el nombre y el correo, no el id', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(connectedStatus());
    const getUser = vi.spyOn(identityApi, 'getUser').mockResolvedValue(ok(userFixture()));
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.users.read);
    renderProviderCard();

    expect(await screen.findByText('Ana Torres')).toBeInTheDocument();
    expect(screen.getByText('ana.torres@acme.com.pe')).toBeInTheDocument();
    expect(getUser).toHaveBeenCalledWith(CONNECTED_BY);
    expect(document.body.innerHTML).not.toContain(CONNECTED_BY);
  });

  it('un usuario sin nombre se muestra por su correo', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(connectedStatus());
    vi.spyOn(identityApi, 'getUser').mockResolvedValue(ok(userFixture({ first_name: '', last_name: '' })));
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.users.read);
    renderProviderCard();

    expect(await screen.findByText('ana.torres@acme.com.pe')).toBeInTheDocument();
  });

  it('un usuario que ya no existe se indica como eliminado', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(connectedStatus());
    vi.spyOn(identityApi, 'getUser').mockRejectedValue(
      new ApiError(404, { code: 'NOT_FOUND', message: 'user not found' }),
    );
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.users.read);
    renderProviderCard();

    expect(await screen.findByText(t('domains.dnsProvider.connectedByRemoved'))).toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain(CONNECTED_BY);
  });

  it('si identity falla no muestra el id ni lo da por eliminado', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(connectedStatus());
    vi.spyOn(identityApi, 'getUser').mockRejectedValue(
      new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: 'no' }),
    );
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.users.read);
    renderProviderCard();

    expect(await screen.findByText(t('domains.dnsProvider.connectedByUnknown'))).toBeInTheDocument();
    expect(screen.queryByText(t('domains.dnsProvider.connectedByRemoved'))).not.toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain(CONNECTED_BY);
  });

  it('sin users/read no consulta identity ni muestra el id', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(connectedStatus());
    const getUser = vi.spyOn(identityApi, 'getUser');
    grant(PERMISSIONS.dnsProviders.read);
    renderProviderCard();

    expect(await screen.findByText(t('domains.dnsProvider.connectedByUnknown'))).toBeInTheDocument();
    expect(getUser).not.toHaveBeenCalled();
    expect(document.body.innerHTML).not.toContain(CONNECTED_BY);
  });

  it('la propia conexion se resuelve con la ficha de la sesion, sin permiso ni llamada', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(connectedStatus());
    const getUser = vi.spyOn(identityApi, 'getUser');
    useAuthStore.setState({ userId: CONNECTED_BY, user: userFixture() });
    grant(PERMISSIONS.dnsProviders.read);
    renderProviderCard();

    expect(await screen.findByText('Ana Torres')).toBeInTheDocument();
    expect(getUser).not.toHaveBeenCalled();
  });
});

describe('conexion con Cloudflare', () => {
  it('conecta con el token en un campo enmascarado y no lo conserva', async () => {
    const user = userEvent.setup();
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(ok({ provider: 'cloudflare', connected: false }));
    const connect = vi.spyOn(dnsProvidersApi, 'connect').mockResolvedValue(
      ok({
        provider: 'cloudflare',
        connected: true,
        token_hint: '6789',
        zones: ['acme.com', 'acme.org'],
        zones_visible: 2,
        connected_by: CONNECTED_BY,
        connected_at: '2026-09-17T10:00:00Z',
        last_validated_at: '2026-09-17T10:00:00Z',
      }),
    );
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.dnsProviders.connect);
    const { container } = renderProviderCard();

    expect(await screen.findByText(t('domains.dnsProvider.notConnected'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('domains.dnsProvider.connect') }));
    const input = await screen.findByLabelText(new RegExp(t('domains.dnsProvider.form.token')));
    expect(input).toHaveAttribute('type', 'password');
    expect(input).toHaveAttribute('autocomplete', 'off');
    const submit = screen.getByRole('button', { name: t('domains.dnsProvider.form.submit') });
    expect(submit).toBeDisabled();
    await user.type(input, TOKEN);
    await user.click(submit);

    await waitFor(() => expect(connect).toHaveBeenCalledWith('cloudflare', TOKEN));
    expect(await screen.findByText(t('domains.dnsProvider.active'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.dnsProvider.tokenHint', { hint: '6789' }))).toBeInTheDocument();
    expect(screen.getByText('acme.com, acme.org')).toBeInTheDocument();
    expect(screen.queryByLabelText(new RegExp(t('domains.dnsProvider.form.token')))).not.toBeInTheDocument();
    expect(container.innerHTML).not.toContain(TOKEN);
    expect(document.body.innerHTML).not.toContain(TOKEN);
    expect(document.body.innerHTML).not.toContain(CONNECTED_BY);
  });

  it('muestra el error de Cloudflare y vacia el campo', async () => {
    const user = userEvent.setup();
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(ok({ provider: 'cloudflare', connected: false }));
    vi.spyOn(dnsProvidersApi, 'connect').mockRejectedValue(
      new ApiError(422, { code: 'DNS_PROVIDER_TOKEN_INVALID', message: 'no' }),
    );
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.dnsProviders.connect);
    renderProviderCard();

    await user.click(await screen.findByRole('button', { name: t('domains.dnsProvider.connect') }));
    const input = await screen.findByLabelText(new RegExp(t('domains.dnsProvider.form.token')));
    await user.type(input, TOKEN);
    await user.click(screen.getByRole('button', { name: t('domains.dnsProvider.form.submit') }));
    expect(await screen.findByText(t('error.code.DNS_PROVIDER_TOKEN_INVALID'))).toBeInTheDocument();
    expect(input).toHaveValue('');
  });

  it('sin permiso de conectar solo muestra el estado', async () => {
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(ok({ provider: 'cloudflare', connected: false }));
    grant(PERMISSIONS.dnsProviders.read);
    renderProviderCard();
    expect(await screen.findByText(t('domains.dnsProvider.notConnected'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('domains.dnsProvider.connect') })).not.toBeInTheDocument();
  });

  it('desconecta con confirmacion', async () => {
    const user = userEvent.setup();
    vi.spyOn(dnsProvidersApi, 'status').mockResolvedValue(
      ok({ provider: 'cloudflare', connected: true, token_hint: '6789', zones: ['acme.com'], zones_visible: 1 }),
    );
    const disconnect = vi
      .spyOn(dnsProvidersApi, 'disconnect')
      .mockResolvedValue(ok({ provider: 'cloudflare', connected: false, disconnected: true, domains_reset: 2 }));
    grant(PERMISSIONS.dnsProviders.read, PERMISSIONS.dnsProviders.disconnect);
    renderProviderCard();

    await user.click(await screen.findByRole('button', { name: t('domains.dnsProvider.disconnect') }));
    expect(screen.getByText(t('domains.dnsProvider.disconnectConfirm'))).toBeInTheDocument();
    expect(disconnect).not.toHaveBeenCalled();
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('domains.dnsProvider.disconnect') }));
    await waitFor(() => expect(disconnect).toHaveBeenCalledWith('cloudflare'));
    expect(await screen.findByText(t('domains.dnsProvider.notConnected'))).toBeInTheDocument();
  });
});

describe('publicacion automatica', () => {
  it('publica los registros y muestra el resultado y la verificacion', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture({ dns_mode: 'cloudflare' })));
    const publish = vi
      .spyOn(domainsApi, 'publishDns')
      .mockResolvedValue(ok(publishResult(publication(), { dns_published_at: '2026-09-17T10:00:00Z' })));
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.publishDns);
    renderDetail();

    expect(await screen.findByText(t('domains.dnsMode.cloudflare'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('domains.dns.publish') }));
    await waitFor(() => expect(publish).toHaveBeenCalledWith(DOMAIN_ID, []));
    expect(await screen.findByText(t('domains.dns.result.title', { zone: 'acme.com' }))).toBeInTheDocument();
    expect(screen.getByText(t('domains.dns.result.complete'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.dns.action.unchanged'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.outcome.failed'))).toBeInTheDocument();
    expect(screen.queryByText(t('domains.dns.conflict.title'))).not.toBeInTheDocument();
  });

  it('un conflicto solo se reemplaza marcandolo y confirmando', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture({ dns_mode: 'cloudflare' })));
    const conflict = publication({
      complete: false,
      records: [
        {
          record: 'spf',
          type: 'TXT',
          host: 'acme.com',
          value: 'v=spf1 include:spf.plataforma.example -all',
          action: 'conflict',
          existing: ['v=spf1 include:_spf.google.com ~all'],
        },
        { record: 'dkim', type: 'TXT', host: 'cfm202609._domainkey.acme.com', value: 'v=DKIM1', action: 'created', existing: [] },
      ],
    });
    const replaced = publication({
      records: [
        { record: 'spf', type: 'TXT', host: 'acme.com', value: 'v=spf1', action: 'replaced', existing: ['v=spf1 include:_spf.google.com ~all'] },
      ],
    });
    const publish = vi
      .spyOn(domainsApi, 'publishDns')
      .mockResolvedValueOnce(ok(publishResult(conflict)))
      .mockResolvedValueOnce(ok(publishResult(replaced)));
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.publishDns);
    renderDetail();

    await user.click(await screen.findByRole('button', { name: t('domains.dns.publish') }));
    expect(await screen.findByText(t('domains.dns.conflict.title'))).toBeInTheDocument();
    expect(
      screen.getByText(t('domains.dns.conflict.existing', { value: 'v=spf1 include:_spf.google.com ~all' })),
    ).toBeInTheDocument();
    expect(screen.getByText(t('domains.dns.result.incomplete'))).toBeInTheDocument();
    const confirm = screen.getByRole('button', { name: t('domains.dns.conflict.confirm') });
    expect(confirm).toBeDisabled();
    expect(publish).toHaveBeenCalledTimes(1);

    await user.click(
      screen.getByLabelText(t('domains.dns.conflict.replace', { record: t('domains.record.spf') })),
    );
    await user.click(confirm);
    await waitFor(() => expect(publish).toHaveBeenLastCalledWith(DOMAIN_ID, ['spf']));
    expect(await screen.findByText(t('domains.dns.action.replaced'))).toBeInTheDocument();
    expect(screen.queryByText(t('domains.dns.conflict.title'))).not.toBeInTheDocument();
  });

  it('vuelve a manual con confirmacion', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture({ dns_mode: 'cloudflare' })));
    const { dkim_rotations: _r, dns_checks: _c, ...manual } = domainFixture();
    const setMode = vi.spyOn(domainsApi, 'setDnsMode').mockResolvedValue(ok(manual));
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.publishDns);
    renderDetail();

    await user.click(await screen.findByRole('button', { name: t('domains.dns.useManual') }));
    expect(screen.getByText(t('domains.dns.useManualConfirm'))).toBeInTheDocument();
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('domains.dns.useManual') }));
    await waitFor(() => expect(setMode).toHaveBeenCalledWith(DOMAIN_ID, 'manual'));
    expect(await screen.findByText(t('domains.dnsMode.manual'))).toBeInTheDocument();
  });

  it('al rotar en automatico avisa de que el registro nuevo ya esta publicado', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture({ dns_mode: 'cloudflare' })));
    const { dkim_rotations: _r, dns_checks: _c, dns_records: _d, ...domain } = domainFixture({
      dns_mode: 'cloudflare',
      dkim_selector: 'cfm20261017',
    });
    vi.spyOn(domainsApi, 'rotateDkim').mockResolvedValue(
      ok({
        ...domain,
        dns_record: { record: 'dkim', type: 'TXT', host: 'cfm20261017._domainkey.acme.com', value: 'v=DKIM1', required: true },
        grace_until: '2026-10-24T10:00:00Z',
        dns_automation: { provider: 'cloudflare', publication: publication(), error_code: null },
      }),
    );
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.rotateDkim);
    renderDetail();

    await user.click(await screen.findByRole('button', { name: t('domains.rotateDkim') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('domains.rotateDkim') }));
    expect(await screen.findByText(t('domains.dns.automation.published'))).toBeInTheDocument();
  });
});
