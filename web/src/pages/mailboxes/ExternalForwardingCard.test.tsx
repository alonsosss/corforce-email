import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { PermissionTriple } from '@/api/access';
import { mailDirectoryApi, mailPolicyApi, type MailPolicy } from '@/api/mailDirectory';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { ExternalForwardingCard } from './ExternalForwardingCard';
import { MailboxMfaCard } from './MailboxMfaCard';
import { MAILBOX } from './mailboxFixtures';

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
    modules: [MODULES.mailboxes],
    permissions,
  });
}

function ok<T>(data: T): ApiResponse<T> {
  return { data };
}

const ALLOWED: MailPolicy = { external_forwarding_allowed: true, updated_at: null };
const BLOCKED: MailPolicy = {
  external_forwarding_allowed: false,
  updated_at: '2026-09-24T10:00:00Z',
};

afterEach(() => {
  vi.restoreAllMocks();
});

describe('reenvio a direcciones externas en el panel', () => {
  const renderCard = () =>
    render(
      <ToastProvider>
        <ExternalForwardingCard />
      </ToastProvider>,
    );

  it('apagarlo avisa de que retira los reenvios guardados y pide confirmacion', async () => {
    grant(PERMISSIONS.mailPolicy.read, PERMISSIONS.mailPolicy.update);
    vi.spyOn(mailPolicyApi, 'get').mockResolvedValue(ok(ALLOWED));
    const set = vi.spyOn(mailPolicyApi, 'set').mockResolvedValue(ok(BLOCKED));
    renderCard();

    expect(await screen.findByText(t('mailboxes.forwardingPolicy.on'))).toBeInTheDocument();
    expect(screen.getByText(t('mailboxes.forwardingPolicy.removalNotice'))).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole('button', { name: t('mailboxes.forwardingPolicy.disable') }),
    );
    expect(set).not.toHaveBeenCalled();
    const dialog = await screen.findByRole('dialog');
    expect(
      within(dialog).getByText(t('mailboxes.forwardingPolicy.disableConfirm')),
    ).toBeInTheDocument();
    await userEvent.click(
      within(dialog).getByRole('button', { name: t('mailboxes.forwardingPolicy.disable') }),
    );
    await waitFor(() => expect(set).toHaveBeenCalledWith(false));
    expect(await screen.findByText(t('mailboxes.forwardingPolicy.off'))).toBeInTheDocument();
  });

  it('volver a permitirlo no pide confirmacion', async () => {
    grant(PERMISSIONS.mailPolicy.read, PERMISSIONS.mailPolicy.update);
    vi.spyOn(mailPolicyApi, 'get').mockResolvedValue(ok(BLOCKED));
    const set = vi.spyOn(mailPolicyApi, 'set').mockResolvedValue(ok(ALLOWED));
    renderCard();
    await userEvent.click(
      await screen.findByRole('button', { name: t('mailboxes.forwardingPolicy.enable') }),
    );
    await waitFor(() => expect(set).toHaveBeenCalledWith(true));
    expect(await screen.findByText(t('mailboxes.forwardingPolicy.on'))).toBeInTheDocument();
  });

  it('sin permiso de cambio solo se ve el estado', async () => {
    grant(PERMISSIONS.mailPolicy.read);
    vi.spyOn(mailPolicyApi, 'get').mockResolvedValue(ok(ALLOWED));
    renderCard();
    expect(await screen.findByText(t('mailboxes.forwardingPolicy.readOnly'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('mailboxes.forwardingPolicy.disable') }),
    ).not.toBeInTheDocument();
  });
});

describe('verificacion en dos pasos en la ficha del buzon', () => {
  const renderCard = (onChange = vi.fn(), mfa = true) => {
    render(
      <ToastProvider>
        <MailboxMfaCard mailbox={{ ...MAILBOX, mfa_enabled: mfa }} onChange={onChange} />
      </ToastProvider>,
    );
    return onChange;
  };

  it('restablecerla pide confirmacion y deja el buzon sin ella', async () => {
    grant(PERMISSIONS.mailboxMfa.delete);
    const reset = vi.spyOn(mailDirectoryApi, 'resetMailboxMfa').mockResolvedValue(ok(null));
    const onChange = renderCard();

    expect(screen.getByText(t('mailboxes.mfa.on'))).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: t('mailboxes.mfa.reset') }));
    expect(reset).not.toHaveBeenCalled();
    const dialog = await screen.findByRole('dialog');
    expect(
      within(dialog).getByText(t('mailboxes.mfa.resetConfirm', { address: MAILBOX.username })),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('button', { name: t('mailboxes.mfa.reset') }));
    await waitFor(() => expect(reset).toHaveBeenCalledWith(MAILBOX.id));
    expect(onChange).toHaveBeenCalledWith({ ...MAILBOX, mfa_enabled: false });
  });

  it('sin permiso no se ofrece restablecer', () => {
    grant();
    renderCard();
    expect(screen.getByText(t('mailboxes.mfa.on'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('mailboxes.mfa.reset') })).toBeNull();
  });

  it('muestra que no esta activa', () => {
    grant(PERMISSIONS.mailboxMfa.delete);
    renderCard(vi.fn(), false);
    expect(screen.getByText(t('mailboxes.mfa.off'))).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('mailboxes.mfa.reset') })).toBeNull();
  });
});
