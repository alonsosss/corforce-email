import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type BookingPage } from '@/api/webmail';
import { t } from '@/i18n';
import { renderScreen } from '../testing';
import { BookingSettings } from './BookingSettings';
import { bookingProblems, formToSettings, newBookingForm, settingsToForm } from './booking';

const PAGE: BookingPage = {
  title: 'Demo',
  description: '',
  duration_minutes: 30,
  buffer_minutes: 10,
  min_notice_minutes: 120,
  max_advance_days: 30,
  daily_limit: 5,
  timezone: 'America/Lima',
  weekly: { MO: [{ start: '09:00', end: '24:00' }] },
  active: true,
  public_id: 'enlace-publico-de-prueba-01',
  owner_address: 'ana@empresa.pe',
  owner_name: 'Ana',
  updated_at: '2026-09-24T10:00:00Z',
  cell: 'pe-01',
  tenant_id: '11111111-1111-4111-8111-111111111111',
};

describe('pagina de citas en ajustes', () => {
  afterEach(() => vi.restoreAllMocks());

  it('sin pagina propone una y al guardarla muestra su enlace', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'bookingSettings').mockRejectedValue(
      new ApiError(404, { code: 'NOT_FOUND', message: '' }),
    );
    const save = vi.spyOn(webmailApi, 'saveBookingSettings').mockResolvedValue(PAGE);
    renderScreen(<BookingSettings />);

    expect(await screen.findByText(t('webmail.booking.notConfigured'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('common.save') }));
    expect(screen.getByText(t('webmail.booking.titleRequired'))).toBeInTheDocument();
    expect(save).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText(new RegExp(t('webmail.booking.fieldTitle'))), 'Demo');
    await user.click(screen.getByRole('button', { name: t('common.save') }));
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({ title: 'Demo', duration_minutes: 30, active: false }),
        false,
      ),
    );
    expect(
      await screen.findByText(
        `${window.location.origin}/citas/pe-01/11111111-1111-4111-8111-111111111111/enlace-publico-de-prueba-01`,
      ),
    ).toBeInTheDocument();
  });

  it('cambiar el enlace pide confirmacion y lo regenera', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'bookingSettings').mockResolvedValue(PAGE);
    const save = vi
      .spyOn(webmailApi, 'saveBookingSettings')
      .mockResolvedValue({
        ...PAGE,
        public_id: 'otro-enlace-publico-de-prueba',
        updated_at: '2026-09-24T11:00:00Z',
      });
    renderScreen(<BookingSettings />);

    await user.click(await screen.findByRole('button', { name: t('webmail.booking.regenerate') }));
    const confirm = screen.getByRole('dialog');
    await user.click(
      Array.from(confirm.querySelectorAll('button')).find(
        (b) => b.textContent === t('webmail.booking.regenerate'),
      )!,
    );
    await waitFor(() => expect(save).toHaveBeenCalledWith(expect.anything(), true));
  });

  it('convierte la configuracion ida y vuelta y senala franjas mas cortas que la cita', () => {
    const form = settingsToForm(PAGE);
    expect(form.noticeHours).toBe('2');
    expect(form.weekly.MO[0]?.end).toBe('00:00');
    const back = formToSettings(form);
    expect(back.weekly.MO).toEqual([{ start: '09:00', end: '24:00' }]);
    expect(back.min_notice_minutes).toBe(120);
    const bad = newBookingForm();
    bad.title = 'x';
    bad.duration = '120';
    bad.weekly.MO = [{ start: '09:00', end: '10:00' }];
    expect(bookingProblems(bad).weekly).toBe(t('webmail.booking.windowInvalid'));
  });
});
