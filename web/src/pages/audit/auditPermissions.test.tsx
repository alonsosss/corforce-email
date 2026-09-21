import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { auditApi } from '@/api/audit';
import { MODULES } from '@/access/modules';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import AuditLogsPage from './AuditLogsPage';
import IntegrityPage from './IntegrityPage';
import SecurityEventsPage from './SecurityEventsPage';

// Cada accion de auditoria tiene su permiso: la interfaz no ofrece ni pide lo que el
// handler va a negar con un 403.
function grant(...perms: [resource: string, action: string][]) {
  const permissions: PermissionTriple[] = perms.map(([resource, action]) => ({
    module: MODULES.audit,
    resource,
    action,
  }));
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.audit],
    permissions,
  });
}

function renderPage(page: JSX.Element) {
  return render(
    <MemoryRouter>
      <ToastProvider>{page}</ToastProvider>
    </MemoryRouter>,
  );
}

describe('permisos por accion en auditoria', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  it('verificar la cadena exige integrity/verify: con integrity/read no se ofrece ni se pide', () => {
    const list = vi.spyOn(auditApi, 'listIntegrityRuns');
    grant(['integrity', 'read']);
    renderPage(<IntegrityPage />);
    expect(screen.queryByRole('button', { name: t('audit.integrity.verify') })).toBeNull();
    expect(screen.getByText(t('audit.integrity.noVerify'))).toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
  });

  it('con integrity/verify se ofrece verificar', async () => {
    vi.spyOn(auditApi, 'listIntegrityRuns').mockResolvedValue({ data: [] });
    grant(['integrity', 'verify']);
    renderPage(<IntegrityPage />);
    expect(
      await screen.findByRole('button', { name: t('audit.integrity.verify') }),
    ).toBeInTheDocument();
  });

  it('los registros exigen logs/read y sin el no se piden', () => {
    const search = vi.spyOn(auditApi, 'searchLogs');
    grant(['security_events', 'read']);
    renderPage(<AuditLogsPage />);
    expect(screen.getByText(t('common.missingPermission'))).toBeInTheDocument();
    expect(search).not.toHaveBeenCalled();
  });

  it('los eventos de seguridad exigen security_events/read y sin el no se piden', () => {
    const list = vi.spyOn(auditApi, 'listSecurityEvents');
    grant(['logs', 'read']);
    renderPage(<SecurityEventsPage />);
    expect(screen.getByText(t('common.missingPermission'))).toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
  });

  it('reconocer un evento exige security_events/acknowledge', async () => {
    vi.spyOn(auditApi, 'listSecurityEvents').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 20,
      total: 0,
      totalPages: 0,
    });
    grant(['security_events', 'read']);
    renderPage(<SecurityEventsPage />);
    expect(await screen.findByText(t('audit.events.empty'))).toBeInTheDocument();
    expect(screen.queryByText(t('audit.events.acknowledge'))).toBeNull();
  });
});
