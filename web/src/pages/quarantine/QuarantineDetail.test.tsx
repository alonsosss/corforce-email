import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { PermissionTriple } from '@/api/access';
import { mailSecurityApi, type QuarantineItem } from '@/api/mailSecurity';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { QuarantineDetail } from './QuarantineDetail';

const ITEM: QuarantineItem = {
  id: 'q1',
  tenant_id: 't1',
  qid: 'ABCDEF',
  subject: 'Factura',
  score: '9.5',
  ip: '203.0.113.7',
  action: 'add header',
  symbols: null,
  fuzzy_hashes: null,
  sender: 'ana@acme.test',
  rcpt: 'bea@acme.test',
  domain: 'acme.test',
  notified: false,
  user_name: '',
  qhash: 'h',
  size: 100,
  created_at: '2026-09-21T10:00:00Z',
};

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
    modules: [MODULES.mailSecurity],
    permissions,
  });
}

function renderDetail(onRemoved = vi.fn()) {
  render(
    <ToastProvider>
      <QuarantineDetail item={ITEM} onClose={vi.fn()} onRemoved={onRemoved} />
    </ToastProvider>,
  );
  return onRemoved;
}

describe('detalle de un mensaje en cuarentena', () => {
  afterEach(() => vi.restoreAllMocks());

  it('liberar como legitimo exige los permisos de liberar y de entrenar', () => {
    grant(PERMISSIONS.quarantine.release);
    renderDetail();
    expect(screen.getByRole('button', { name: t('quarantine.release') })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('quarantine.releaseHam') })).toBeNull();
  });

  it('con los dos permisos pide confirmacion, avisa del riesgo y libera y entrena', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.quarantine.release, PERMISSIONS.quarantine.learn);
    const releaseHam = vi
      .spyOn(mailSecurityApi, 'releaseQuarantineAsHam')
      .mockResolvedValue({ status: 'released' } as never);
    const release = vi
      .spyOn(mailSecurityApi, 'releaseQuarantine')
      .mockResolvedValue({ status: 'released' } as never);
    const onRemoved = renderDetail();

    await user.click(screen.getByRole('button', { name: t('quarantine.releaseHam') }));
    expect(screen.getByText(t('quarantine.releaseHamConfirm'))).toBeInTheDocument();
    expect(releaseHam).not.toHaveBeenCalled();
    // Dentro del mismo modal la confirmacion tiene el mismo texto en su boton.
    const confirm = screen.getAllByRole('button', { name: t('quarantine.releaseHam') }).pop()!;
    await user.click(confirm);

    expect(releaseHam).toHaveBeenCalledWith('q1');
    expect(release).not.toHaveBeenCalled();
    expect(onRemoved).toHaveBeenCalled();
  });

  it('liberar a secas no entrena el clasificador', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.quarantine.release, PERMISSIONS.quarantine.learn);
    const releaseHam = vi
      .spyOn(mailSecurityApi, 'releaseQuarantineAsHam')
      .mockResolvedValue({ status: 'released' } as never);
    const release = vi
      .spyOn(mailSecurityApi, 'releaseQuarantine')
      .mockResolvedValue({ status: 'released' } as never);
    renderDetail();

    await user.click(screen.getByRole('button', { name: t('quarantine.release') }));
    await user.click(screen.getAllByRole('button', { name: t('quarantine.release') }).pop()!);
    expect(release).toHaveBeenCalledWith('q1');
    expect(releaseHam).not.toHaveBeenCalled();
  });
});
