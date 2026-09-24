import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { PermissionTriple } from '@/api/access';
import type { ApiResponse } from '@/api/types';
import { assistantSettingsApi, type AssistantSettings } from '@/api/webmailAssistant';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { AssistantSettingsCard } from './AssistantSettingsCard';

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

function ok(data: AssistantSettings): ApiResponse<AssistantSettings> {
  return { data };
}

const OFF: AssistantSettings = {
  enabled: false,
  enabled_at: null,
  updated_by: null,
  updated_at: null,
};
const ON: AssistantSettings = {
  enabled: true,
  enabled_at: '2026-09-24T10:00:00Z',
  updated_by: '11111111-1111-4111-8111-111111111111',
  updated_at: '2026-09-24T10:00:00Z',
};

function renderCard() {
  render(
    <ToastProvider>
      <AssistantSettingsCard />
    </ToastProvider>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('ajuste del asistente en el panel', () => {
  it('activarlo exige aceptar el aviso de tratamiento de datos', async () => {
    grant(PERMISSIONS.assistantSettings.read, PERMISSIONS.assistantSettings.update);
    vi.spyOn(assistantSettingsApi, 'get').mockResolvedValue(ok(OFF));
    const set = vi.spyOn(assistantSettingsApi, 'set').mockResolvedValue(ok(ON));
    renderCard();
    expect(await screen.findByText(t('mailboxes.assistant.off'))).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: t('mailboxes.assistant.enable') }));
    expect(set).not.toHaveBeenCalled();
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(t('mailboxes.assistant.notice'))).toBeInTheDocument();
    await userEvent.click(
      within(dialog).getByRole('button', { name: t('mailboxes.assistant.accept') }),
    );
    await waitFor(() => expect(set).toHaveBeenCalledWith(true));
    expect(await screen.findByText(t('mailboxes.assistant.on'))).toBeInTheDocument();
  });

  it('apagarlo no pide confirmacion', async () => {
    grant(PERMISSIONS.assistantSettings.read, PERMISSIONS.assistantSettings.update);
    vi.spyOn(assistantSettingsApi, 'get').mockResolvedValue(ok(ON));
    const set = vi.spyOn(assistantSettingsApi, 'set').mockResolvedValue(ok(OFF));
    renderCard();
    await userEvent.click(
      await screen.findByRole('button', { name: t('mailboxes.assistant.disable') }),
    );
    await waitFor(() => expect(set).toHaveBeenCalledWith(false));
    expect(await screen.findByText(t('mailboxes.assistant.off'))).toBeInTheDocument();
  });

  it('sin permiso de cambio solo se ve el estado', async () => {
    grant(PERMISSIONS.assistantSettings.read);
    vi.spyOn(assistantSettingsApi, 'get').mockResolvedValue(ok(OFF));
    renderCard();
    expect(await screen.findByText(t('mailboxes.assistant.readOnly'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('mailboxes.assistant.enable') }),
    ).not.toBeInTheDocument();
  });
});
