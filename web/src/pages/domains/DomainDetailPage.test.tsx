import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import {
  domainsApi,
  type DkimRotation,
  type DomainDetail,
  type RevokeDkimResult,
} from '@/api/domains';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
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

const DOMAIN_ID = 'b7c1f0de-0000-4000-8000-000000000001';
const REASON = 'clave expuesta en un respaldo';

const REVOCATION: DkimRotation = {
  id: 'b7c1f0de-0000-4000-8000-0000000000aa',
  kind: 'compromised',
  selector: 'cfm20260912',
  previous_selector: null,
  revoked_selectors: ['cfm202609'],
  reason: REASON,
  actor_id: 'b7c1f0de-0000-4000-8000-0000000000bb',
  rotated_at: '2026-09-12T10:00:00Z',
};

function domainFixture(extra: Partial<DomainDetail> = {}): DomainDetail {
  return {
    id: DOMAIN_ID,
    domain: 'acme.com',
    purpose: 'corporate',
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
    created_at: '2026-09-01T10:00:00Z',
    updated_at: '2026-09-12T09:00:00Z',
    dns_records: [],
    dns_checks: [],
    dkim_rotations: [],
    ...extra,
  };
}

function revocationResult(extra: Partial<RevokeDkimResult> = {}): RevokeDkimResult {
  const { dns_records: _records, dns_checks: _checks, dkim_rotations: _rotations, ...domain } =
    domainFixture({ dkim_selector: 'cfm20260912' });
  return {
    ...domain,
    dns_record: {
      record: 'dkim',
      type: 'TXT',
      host: 'cfm20260912._domainkey.acme.com',
      value: 'v=DKIM1; k=rsa; p=NEW',
      required: true,
    },
    remove_dns_records: [
      { record: 'dkim', type: 'TXT', host: 'cfm202609._domainkey.acme.com', value: '', required: false },
    ],
    revocation: REVOCATION,
    engines_retired: true,
    integration_errors: [],
    ...extra,
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

afterEach(() => {
  vi.restoreAllMocks();
});

describe('revocacion de claves DKIM comprometidas', () => {
  it('solo ofrece revocar con el permiso revoke_dkim, aparte de rotar', async () => {
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture()));
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.rotateDkim);
    const { unmount } = renderPage();
    expect(await screen.findByRole('button', { name: t('domains.rotateDkim') })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('domains.revokeDkim') })).not.toBeInTheDocument();
    unmount();

    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.revokeDkim);
    renderPage();
    expect(await screen.findByRole('button', { name: t('domains.revokeDkim') })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('domains.rotateDkim') })).not.toBeInTheDocument();
  });

  it('revoca con el selector actual y el motivo, y pide retirar ya los TXT revocados', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture()));
    const revoke = vi.spyOn(domainsApi, 'revokeDkim').mockResolvedValue(ok(revocationResult()));
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.revokeDkim);
    renderPage();

    await user.click(await screen.findByRole('button', { name: t('domains.revokeDkim') }));
    const confirm = await screen.findByRole('button', { name: t('domains.revoke.confirm') });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText(new RegExp(t('domains.revoke.reason'))), REASON);
    await user.click(confirm);

    await waitFor(() =>
      expect(revoke).toHaveBeenCalledWith(DOMAIN_ID, {
        current_selector: 'cfm202609',
        reason: REASON,
      }),
    );
    const notice = await screen.findByText(t('domains.revocation.removeNow'));
    const alert = notice.closest('.cf-alert') as HTMLElement;
    expect(within(alert).getByText(/cfm202609\._domainkey\.acme\.com/)).toBeInTheDocument();
    expect(screen.getAllByText(/cfm20260912\._domainkey\.acme\.com/).length).toBeGreaterThan(0);
    expect(screen.queryByText(t('domains.revocation.enginesPending'))).not.toBeInTheDocument();
  });

  it('avisa si los servidores de correo no confirmaron la retirada', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(ok(domainFixture()));
    vi.spyOn(domainsApi, 'revokeDkim').mockResolvedValue(
      ok(revocationResult({ engines_retired: false, integration_errors: ['mail-security: 503'] })),
    );
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.revokeDkim);
    renderPage();

    await user.click(await screen.findByRole('button', { name: t('domains.revokeDkim') }));
    await user.type(await screen.findByLabelText(new RegExp(t('domains.revoke.reason'))), REASON);
    await user.click(screen.getByRole('button', { name: t('domains.revoke.confirm') }));

    expect(await screen.findByText(t('domains.revocation.enginesPending'))).toBeInTheDocument();
    expect(screen.getByText('mail-security: 503')).toBeInTheDocument();
  });

  it('con la revocacion sin confirmar la reintenta con el selector y el motivo registrados', async () => {
    const user = userEvent.setup();
    vi.spyOn(domainsApi, 'get').mockResolvedValue(
      ok(
        domainFixture({
          dkim_selector: 'cfm20260912',
          dkim_revocation_pending: true,
          dkim_rotations: [REVOCATION],
        }),
      ),
    );
    const revoke = vi.spyOn(domainsApi, 'revokeDkim').mockResolvedValue(ok(revocationResult()));
    grant(PERMISSIONS.domains.read, PERMISSIONS.domains.revokeDkim);
    renderPage();

    expect(await screen.findByText(t('domains.revocation.pendingTitle'))).toBeInTheDocument();
    expect(screen.getByText(t('domains.history.title'))).toBeInTheDocument();
    expect(screen.getByText(REASON)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('domains.revocation.retry') }));
    await waitFor(() =>
      expect(revoke).toHaveBeenCalledWith(DOMAIN_ID, {
        current_selector: 'cfm202609',
        reason: REASON,
      }),
    );
  });
});
