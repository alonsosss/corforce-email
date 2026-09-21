import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import type { Vacation } from '@/api/vacation';
import { webmailApi } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import SettingsPage from './SettingsPage';

const VACATION: Vacation = {
  enabled: true,
  subject: 'Ausente',
  message: 'Vuelvo el lunes.',
  interval_days: 2,
  starts_on: '2026-09-21',
  ends_on: null,
  updated_at: '2026-09-20T10:00:00Z',
  limits: {
    subject_max_length: 200,
    message_max_length: 8192,
    interval_min_days: 1,
    interval_max_days: 30,
  },
};

function renderPage() {
  render(
    <ToastProvider>
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>
    </ToastProvider>,
  );
}

describe('ajustes del buzon en el webmail', () => {
  afterEach(() => vi.restoreAllMocks());

  it('carga la respuesta automatica del buzon de la sesion y la guarda por el API del webmail', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'vacation').mockResolvedValue(VACATION);
    const save = vi.spyOn(webmailApi, 'setVacation').mockImplementation(async (input) => ({
      ...VACATION,
      ...input,
      updated_at: '2026-09-21T09:00:00Z',
    }));
    renderPage();

    const message = await screen.findByLabelText(new RegExp(`^${t('vacation.message')}`));
    expect(message).toHaveValue('Vuelvo el lunes.');
    await user.clear(message);
    await user.type(message, 'Estoy de viaje.');
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    expect(save).toHaveBeenCalledWith(
      expect.objectContaining({
        enabled: true,
        message: 'Estoy de viaje.',
        interval_days: 2,
        starts_on: '2026-09-21',
      }),
    );
    expect(await screen.findByText(t('vacation.saved'))).toBeInTheDocument();
  });

  it('si el servicio no responde muestra el error y permite reintentar', async () => {
    const user = userEvent.setup();
    const read = vi
      .spyOn(webmailApi, 'vacation')
      .mockRejectedValueOnce(
        new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: 'no disponible' }),
      )
      .mockResolvedValue(VACATION);
    renderPage();

    expect(await screen.findByText(t('webmail.settings.unavailable'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /reintentar/i }));
    expect(
      await screen.findByLabelText(new RegExp(`^${t('vacation.message')}`)),
    ).toBeInTheDocument();
    expect(read).toHaveBeenCalledTimes(2);
  });
});
