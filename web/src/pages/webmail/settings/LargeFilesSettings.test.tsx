import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { largeFilesApi, type LargeFile, type LargeFileListing } from '@/api/largeFiles';
import { t } from '@/i18n';
import SettingsPage from '../SettingsPage';
import { renderScreen } from '../testing';

const ACTIVE: LargeFile = {
  id: 'f1',
  name: 'planos.dwg',
  size_bytes: 2048,
  sha256: 'a'.repeat(64),
  url: 'https://x/enlace',
  state: 'active',
  expires_at: '2026-10-01T00:00:00Z',
  max_downloads: 20,
  downloads: 3,
  remaining_downloads: 17,
  created_at: '2026-09-24T00:00:00Z',
  last_download_at: '2026-09-25T00:00:00Z',
  revoked_at: null,
};

const LISTING: LargeFileListing = {
  enabled: true,
  items: [ACTIVE, { ...ACTIVE, id: 'f2', name: 'viejo.zip', state: 'expired', url: '' }],
  usage: { mailbox_bytes: 2048, mailbox_active: 1, tenant_bytes: 4096 },
  limits: {
    max_file_bytes: 1000,
    default_expiry_days: 7,
    max_expiry_days: 30,
    default_max_downloads: 20,
    max_downloads: 100,
    mailbox_quota_bytes: 1 << 30,
    tenant_quota_bytes: 1 << 31,
    max_active_per_mailbox: 200,
  },
};

function renderFilesTab() {
  renderScreen(<SettingsPage />, { path: '/webmail/settings', url: '/webmail/settings?tab=files' });
}

describe('ajustes: ficheros compartidos', () => {
  afterEach(() => vi.restoreAllMocks());

  it('lista los enlaces con su estado y descargas, y solo el vigente se revoca', async () => {
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue(LISTING);
    renderFilesTab();
    const row = (await screen.findByText('planos.dwg')).closest('tr') as HTMLElement;
    expect(within(row).getByText(t('webmail.largeFiles.state.active'))).toBeTruthy();
    expect(within(row).getByText(t('webmail.largeFiles.downloads', { n: 3, max: 20 }))).toBeTruthy();
    const old = screen.getByText('viejo.zip').closest('tr') as HTMLElement;
    expect(within(old).getByText(t('webmail.largeFiles.state.expired'))).toBeTruthy();
    expect(within(old).queryByRole('button')).toBeNull();
  });

  it('revoca tras confirmar', async () => {
    const user = userEvent.setup();
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue(LISTING);
    const revoke = vi
      .spyOn(largeFilesApi, 'revoke')
      .mockResolvedValue({ ...ACTIVE, state: 'revoked', url: '' });
    renderFilesTab();
    await user.click(
      await screen.findByRole('button', {
        name: t('webmail.largeFiles.revokeLabel', { name: 'planos.dwg' }),
      }),
    );
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.largeFiles.revoke') }));
    await waitFor(() => expect(revoke).toHaveBeenCalledWith('f1'));
    expect(await screen.findByText(t('webmail.largeFiles.revoked'))).toBeTruthy();
  });

  it('sin la funcion en el servidor lo dice', async () => {
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue({ ...LISTING, enabled: false, items: [] });
    renderFilesTab();
    expect(await screen.findByText(t('webmail.largeFiles.disabled'))).toBeTruthy();
  });
});
