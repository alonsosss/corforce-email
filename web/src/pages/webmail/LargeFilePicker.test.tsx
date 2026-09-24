import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { largeFilesApi, type LargeFile, type LargeFileListing } from '@/api/largeFiles';
import { t } from '@/i18n';
import { LargeFilePicker } from './LargeFilePicker';

const LISTING: LargeFileListing = {
  enabled: true,
  items: [],
  usage: { mailbox_bytes: 0, mailbox_active: 0, tenant_bytes: 0 },
  limits: {
    max_file_bytes: 1000,
    default_expiry_days: 7,
    max_expiry_days: 30,
    default_max_downloads: 20,
    max_downloads: 100,
    mailbox_quota_bytes: 5000,
    tenant_quota_bytes: 9000,
    max_active_per_mailbox: 10,
  },
};

const SHARED: LargeFile = {
  id: 'f1',
  name: 'planos.dwg',
  size_bytes: 9,
  sha256: 'a'.repeat(64),
  url: 'https://x/enlace',
  state: 'active',
  expires_at: '2026-10-01T00:00:00Z',
  max_downloads: 20,
  downloads: 0,
  remaining_downloads: 20,
  created_at: '2026-09-24T00:00:00Z',
  last_download_at: null,
  revoked_at: null,
};

function fileInput(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error('sin selector de fichero');
  return input;
}

describe('fichero grande por enlace en la redaccion', () => {
  afterEach(() => vi.restoreAllMocks());

  it('no aparece si el servidor no tiene la funcion', async () => {
    const list = vi.spyOn(largeFilesApi, 'list').mockResolvedValue({ ...LISTING, enabled: false });
    render(<LargeFilePicker suggest={[]} onShared={vi.fn()} disabled={false} />);
    await waitFor(() => expect(list).toHaveBeenCalled());
    expect(screen.queryByText(t('webmail.largeFiles.compose.pick'))).toBeNull();
  });

  it('sube con las opciones elegidas y entrega el enlace', async () => {
    const user = userEvent.setup();
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue(LISTING);
    const upload = vi.spyOn(largeFilesApi, 'upload').mockResolvedValue(SHARED);
    const onShared = vi.fn();
    render(<LargeFilePicker suggest={[]} onShared={onShared} disabled={false} />);
    await screen.findByText(t('webmail.largeFiles.compose.pick'));
    await user.selectOptions(screen.getByRole('combobox'), '30');
    const downloads = screen.getByRole('spinbutton');
    await user.clear(downloads);
    await user.type(downloads, '5');
    const file = new File(['contenido'], 'planos.dwg');
    await user.upload(fileInput(), file);
    await waitFor(() => expect(onShared).toHaveBeenCalledWith(SHARED, undefined));
    expect(upload).toHaveBeenCalledWith(file, { expiresInDays: 30, maxDownloads: 5 });
    expect(
      await screen.findByText(t('webmail.largeFiles.compose.added', { name: 'planos.dwg' })),
    ).toBeTruthy();
  });

  it('avisa sin subir lo que no cabe y muestra los rechazos del servidor', async () => {
    const user = userEvent.setup();
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue(LISTING);
    const upload = vi
      .spyOn(largeFilesApi, 'upload')
      .mockRejectedValue(new ApiError(422, { code: 'FILE_INFECTED', message: '' }));
    render(<LargeFilePicker suggest={[]} onShared={vi.fn()} disabled={false} />);
    await screen.findByText(t('webmail.largeFiles.compose.pick'));
    await user.upload(fileInput(), new File(['x'.repeat(2000)], 'grande.iso'));
    expect(await screen.findByRole('alert')).toHaveTextContent('grande.iso');
    expect(upload).not.toHaveBeenCalled();

    await user.upload(fileInput(), new File(['x'], 'virus.exe'));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(t('error.code.FILE_INFECTED')),
    );
  });

  it('convierte en enlace un adjunto que no cabe en el mensaje', async () => {
    const user = userEvent.setup();
    vi.spyOn(largeFilesApi, 'list').mockResolvedValue(LISTING);
    vi.spyOn(largeFilesApi, 'upload').mockResolvedValue(SHARED);
    const onShared = vi.fn();
    const attachment = new File(['contenido'], 'planos.dwg');
    render(<LargeFilePicker suggest={[attachment]} onShared={onShared} disabled={false} />);
    await user.click(
      await screen.findByRole('button', {
        name: t('webmail.largeFiles.compose.convert', { name: 'planos.dwg' }),
      }),
    );
    await waitFor(() => expect(onShared).toHaveBeenCalledWith(SHARED, attachment));
  });
});
