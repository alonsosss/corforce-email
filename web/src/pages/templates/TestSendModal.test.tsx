import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { templatesApi } from '@/api/templates';
import { transactionalApi, type SendingDomain } from '@/api/transactional';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { t } from '@/i18n';
import { TestSendModal } from './TestSendModal';

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
    modules: [MODULES.templates, MODULES.transactional],
    permissions,
  });
}

function domain(name: string, canSend: boolean): SendingDomain {
  return {
    domain: name,
    status: 'verified',
    purpose: 'both',
    sending_ready: canSend,
    can_send: canSend,
    updated_at: '2026-09-23T10:00:00Z',
  };
}

function renderModal() {
  return render(
    <MemoryRouter>
      <TestSendModal
        templateId="tpl-1"
        version={3}
        variables={[{ name: 'first_name', type: 'string', required: true }]}
        maxRecipients={5}
        onClose={vi.fn()}
      />
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('envio de prueba de una version', () => {
  it('envia desde un dominio verificado, sin la variable vacia, y muestra lo encolado', async () => {
    grant(PERMISSIONS.templates.testSend, PERMISSIONS.sendingDomains.read);
    vi.spyOn(transactionalApi, 'sendingDomains').mockResolvedValue([
      domain('acme.test', true),
      domain('pendiente.test', false),
    ]);
    const send = vi.spyOn(templatesApi, 'testSend').mockResolvedValue({
      messages: [{ id: 'm1', status: 'queued', email: 'qa@example.com' }],
      suppressed: [],
    });
    renderModal();
    const user = userEvent.setup();

    const select = await screen.findByLabelText(t('templates.testSend.fromDomain'), {
      exact: false,
    });
    expect(screen.queryByRole('option', { name: '@pendiente.test' })).toBeNull();
    expect(select).toHaveValue('acme.test');
    await user.type(
      screen.getByLabelText(new RegExp(`^${t('templates.testSend.fromLocal')}`)),
      'hola',
    );
    await user.type(
      screen.getByLabelText(new RegExp(`^${t('templates.testSend.to')}`)),
      'qa@example.com{Enter}',
    );
    await user.click(screen.getByRole('button', { name: t('templates.testSend.submit') }));

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(send).toHaveBeenCalledWith('tpl-1', 3, {
      from: { email: 'hola@acme.test', name: undefined },
      to: ['qa@example.com'],
      variables: {},
    });
    expect(await screen.findByText(t('templates.testSend.accepted', { n: 1 }))).toBeInTheDocument();
  });

  it('sin permiso para ver los dominios de envio no deja enviar', async () => {
    grant(PERMISSIONS.templates.testSend);
    const list = vi.spyOn(transactionalApi, 'sendingDomains');
    renderModal();
    expect(await screen.findByText(t('templates.testSend.noDomainPermission'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('templates.testSend.submit') })).toBeDisabled();
    expect(list).not.toHaveBeenCalled();
  });

  it('sin dominios verificados lo explica y no deja enviar', async () => {
    grant(PERMISSIONS.templates.testSend, PERMISSIONS.sendingDomains.read);
    vi.spyOn(transactionalApi, 'sendingDomains').mockResolvedValue([
      domain('pendiente.test', false),
    ]);
    renderModal();
    expect(await screen.findByText(t('templates.testSend.noDomains'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('templates.testSend.submit') })).toBeDisabled();
  });
});
