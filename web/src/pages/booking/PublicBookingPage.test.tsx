import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { bookingApi, type PublicBookingPage as PageData } from '@/api/booking';
import { ApiError } from '@/api/errors';
import { t } from '@/i18n';
import { bookingWindow, hasNextWindow, slotsByDay, validVisitorEmail } from './booking';
import PublicBookingPage from './PublicBookingPage';

const PAGE: PageData = {
  title: 'Demo de producto',
  description: 'Veremos el producto.',
  duration_minutes: 30,
  timezone: 'America/Lima',
  owner_name: 'Ana Perez',
  min_notice_minutes: 60,
  max_advance_days: 14,
  slots: [
    { start: '2026-10-01T15:00:00Z', end: '2026-10-01T15:30:00Z' },
    { start: '2026-10-01T15:30:00Z', end: '2026-10-01T16:00:00Z' },
  ],
};

const URL = '/citas/pe-01/11111111-1111-4111-8111-111111111111/enlace-publico-de-prueba-01';

function renderPage() {
  render(
    <MemoryRouter initialEntries={[URL]}>
      <Routes>
        <Route path="/citas/:cell/:tenant/:page" element={<PublicBookingPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('pagina publica de citas', () => {
  afterEach(() => vi.restoreAllMocks());

  it('lista los huecos y reserva con nombre, correo y nota sin rellenar la trampa', async () => {
    const user = userEvent.setup();
    const page = vi.spyOn(bookingApi, 'page').mockResolvedValue(PAGE);
    const book = vi.spyOn(bookingApi, 'book').mockResolvedValue({
      title: 'Demo de producto',
      start: '2026-10-01T15:00:00Z',
      end: '2026-10-01T15:30:00Z',
      timezone: 'America/Lima',
      owner_name: 'Ana Perez',
      confirmation_sent: true,
    });
    renderPage();

    expect(await screen.findByText('Demo de producto')).toBeInTheDocument();
    expect(page.mock.calls[0]?.[0]).toEqual({
      cell: 'pe-01',
      tenant: '11111111-1111-4111-8111-111111111111',
      page: 'enlace-publico-de-prueba-01',
    });
    const slots = screen
      .getAllByRole('button')
      .filter((b) => /\d{2}:\d{2}/.test(b.textContent ?? ''));
    expect(slots).toHaveLength(2);
    await user.click(slots[0]!);
    await user.click(screen.getByRole('button', { name: t('booking.public.submit') }));
    expect(screen.getByText(t('booking.public.nameRequired'))).toBeInTheDocument();
    expect(book).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText(new RegExp(t('booking.public.name'))), 'Luis');
    await user.type(
      screen.getByLabelText(new RegExp(t('booking.public.email'))),
      'luis@cliente.pe',
    );
    await user.type(screen.getByLabelText(t('booking.public.note')), 'Hola');
    await user.click(screen.getByRole('button', { name: t('booking.public.submit') }));
    await waitFor(() =>
      expect(book).toHaveBeenCalledWith(expect.anything(), {
        start: '2026-10-01T15:00:00Z',
        name: 'Luis',
        email: 'luis@cliente.pe',
        note: 'Hola',
        website: '',
      }),
    );
    expect(await screen.findByText(t('booking.public.done'))).toBeInTheDocument();
    expect(screen.getByText(t('booking.public.doneMail'))).toBeInTheDocument();
  });

  it('si el hueco se ocupo vuelve a la lista y la recarga', async () => {
    const user = userEvent.setup();
    const page = vi.spyOn(bookingApi, 'page').mockResolvedValue(PAGE);
    vi.spyOn(bookingApi, 'book').mockRejectedValue(
      new ApiError(409, { code: 'SLOT_UNAVAILABLE', message: '' }),
    );
    renderPage();

    const slots = (await screen.findAllByRole('button')).filter((b) =>
      /\d{2}:\d{2}/.test(b.textContent ?? ''),
    );
    await user.click(slots[0]!);
    await user.type(screen.getByLabelText(new RegExp(t('booking.public.name'))), 'Luis');
    await user.type(
      screen.getByLabelText(new RegExp(t('booking.public.email'))),
      'luis@cliente.pe',
    );
    await user.click(screen.getByRole('button', { name: t('booking.public.submit') }));
    await waitFor(() => expect(page).toHaveBeenCalledTimes(2));
    expect(
      screen.queryByRole('button', { name: t('booking.public.submit') }),
    ).not.toBeInTheDocument();
  });

  it('un enlace que no existe lo dice sin mas', async () => {
    vi.spyOn(bookingApi, 'page').mockRejectedValue(
      new ApiError(404, { code: 'NOT_FOUND', message: '' }),
    );
    renderPage();
    expect(await screen.findByText(t('booking.public.notFound'))).toBeInTheDocument();
  });
});

describe('ayudas de la pagina de citas', () => {
  it('ventanas de una semana desde ahora y dentro de la antelacion maxima', () => {
    const now = new Date(2026, 9, 1, 10, 30);
    const first = bookingWindow(now, 0);
    expect(first.start).toEqual(now);
    expect(first.end).toEqual(new Date(2026, 9, 8));
    expect(bookingWindow(now, 1).start).toEqual(new Date(2026, 9, 8));
    expect(hasNextWindow(0, 14)).toBe(true);
    expect(hasNextWindow(1, 14)).toBe(false);
  });

  it('agrupa por dia local y valida el correo como el servicio', () => {
    const days = slotsByDay([
      { start: '2026-10-02T15:00:00Z', end: '2026-10-02T15:30:00Z' },
      { start: '2026-10-01T15:00:00Z', end: '2026-10-01T15:30:00Z' },
    ]);
    expect(days).toHaveLength(2);
    expect(days[0]?.slots[0]?.start).toBe('2026-10-01T15:00:00Z');
    expect(validVisitorEmail('luis@cliente.pe')).toBe(true);
    expect(validVisitorEmail('luis@cliente')).toBe(false);
    expect(validVisitorEmail('a b@c.pe')).toBe(false);
  });
});
