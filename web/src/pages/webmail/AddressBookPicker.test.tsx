import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type AddressBookEntry } from '@/api/webmail';
import { t } from '@/i18n';
import { AddressBookPicker } from './AddressBookPicker';

const ENTRIES: AddressBookEntry[] = [
  { address: 'ana@empresa.pe', display_name: 'Ana Diaz' },
  { address: 'bea@empresa.pe', display_name: '' },
];

describe('libreta de direcciones de la empresa', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('lista a los companeros y anade la direccion elegida', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'addressBook').mockResolvedValue(ENTRIES);
    const onPick = vi.fn();
    render(<AddressBookPicker chosen={[]} onPick={onPick} />);

    await user.click(await screen.findByRole('button', { name: t('webmail.compose.addressBookAdd', { address: 'ana@empresa.pe' }) }));
    expect(onPick).toHaveBeenCalledWith('ana@empresa.pe');
    expect(screen.getByText('Ana Diaz')).toBeInTheDocument();
    // Sin nombre visible se muestra la direccion como nombre.
    expect(screen.getByText('bea@empresa.pe')).toBeInTheDocument();
  });

  it('marca y bloquea a quien ya es destinatario, sin distinguir mayusculas', async () => {
    vi.spyOn(webmailApi, 'addressBook').mockResolvedValue(ENTRIES);
    const onPick = vi.fn();
    render(<AddressBookPicker chosen={['ANA@Empresa.pe']} onPick={onPick} />);

    const ana = await screen.findByRole('button', { name: t('webmail.compose.addressBookAdd', { address: 'ana@empresa.pe' }) });
    expect(ana).toBeDisabled();
    expect(screen.getByText(t('webmail.compose.addressBookAdded'))).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('webmail.compose.addressBookAdd', { address: 'bea@empresa.pe' }) })).toBeEnabled();
  });

  it('busca tras una pausa, con el texto escrito y una sola peticion', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const search = vi.spyOn(webmailApi, 'addressBook').mockResolvedValue(ENTRIES);
    render(<AddressBookPicker chosen={[]} onPick={vi.fn()} />);
    await screen.findByText('Ana Diaz');
    expect(search).toHaveBeenLastCalledWith('', expect.any(AbortSignal));

    await user.type(screen.getByRole('searchbox'), ' ana ');
    expect(search).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(search).toHaveBeenCalledTimes(2);
    expect(search).toHaveBeenLastCalledWith('ana', expect.any(AbortSignal));
  });

  it('avisa cuando nadie coincide y cuando el servicio no responde', async () => {
    const search = vi.spyOn(webmailApi, 'addressBook').mockResolvedValueOnce([]);
    const { unmount } = render(<AddressBookPicker chosen={[]} onPick={vi.fn()} />);
    expect(await screen.findByText(t('webmail.compose.addressBookEmpty'))).toBeInTheDocument();
    unmount();

    search.mockRejectedValue(new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: 'no disponible' }));
    render(<AddressBookPicker chosen={[]} onPick={vi.fn()} />);
    expect(await screen.findByRole('alert')).toHaveTextContent(t('webmail.compose.addressBookUnavailable'));
  });
});
