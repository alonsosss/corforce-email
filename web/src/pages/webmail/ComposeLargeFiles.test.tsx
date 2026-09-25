import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { largeFilesApi, type LargeFile, type LargeFileListing } from '@/api/largeFiles';
import { webmailApi } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import ComposePage from './ComposePage';
import { NEW_MESSAGE } from './composeWindow';
import { DRAFTS, INBOX, META, outletFor, renderScreen } from './testing';

const LISTING: LargeFileListing = {
  enabled: true,
  items: [],
  usage: { mailbox_bytes: 0, mailbox_active: 0, tenant_bytes: 0 },
  limits: {
    max_file_bytes: 1 << 20,
    default_expiry_days: 7,
    max_expiry_days: 30,
    default_max_downloads: 20,
    max_downloads: 100,
    mailbox_quota_bytes: 1 << 30,
    tenant_quota_bytes: 1 << 31,
    max_active_per_mailbox: 200,
  },
};

const SHARED: LargeFile = {
  id: 'f1',
  name: 'planos.dwg',
  size_bytes: 9,
  sha256: 'a'.repeat(64),
  url: 'https://correo.example.com/api/v1/public/files/t/f?x=1&s=abc',
  state: 'active',
  expires_at: '2026-10-01T00:00:00Z',
  max_downloads: 20,
  downloads: 0,
  remaining_downloads: 20,
  created_at: '2026-09-24T00:00:00Z',
  last_download_at: null,
  revoked_at: null,
};

describe('redaccion: fichero grande por enlace', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    useWebmailStore.setState({
      status: 'authenticated',
      session: {
        username: 'ana@empresa.com',
        display_name: 'Ana',
        expires_at: '2026-09-24T20:00:00Z',
        idle_timeout_seconds: 1800,
        quota: null,
      },
    });
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
    vi.spyOn(webmailApi, 'identities').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'addressBook').mockResolvedValue([]);
    vi.spyOn(webmailApi, 'signature').mockRejectedValue(new ApiError(503, null));
    vi.spyOn(webmailApi, 'session').mockRejectedValue(new ApiError(503, null));
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue(LISTING);
  });

  afterEach(() => vi.restoreAllMocks());

  it('el enlace del fichero subido entra en el cuerpo del mensaje', async () => {
    const user = userEvent.setup();
    vi.spyOn(largeFilesApi, 'upload').mockResolvedValue(SHARED);
    renderScreen(<ComposePage request={NEW_MESSAGE} onClose={vi.fn()} />, {
      path: '/webmail/compose',
      url: '/webmail/compose',
      outlet: outletFor([INBOX, DRAFTS]),
    });
    await user.click(await screen.findByRole('button', { name: t('webmail.compose.toPlain') }));
    await screen.findByText(t('webmail.largeFiles.compose.pick'));
    const input = document.querySelector<HTMLInputElement>('.cf-wm-largefiles input[type="file"]');
    if (!input) throw new Error('sin selector de fichero grande');
    await user.upload(input, new File(['contenido'], 'planos.dwg'));
    const body = screen.getByLabelText(t('webmail.compose.body')) as HTMLTextAreaElement;
    await waitFor(() => expect(body.value).toContain(SHARED.url));
    expect(body.value).toContain('planos.dwg');
  });
});
