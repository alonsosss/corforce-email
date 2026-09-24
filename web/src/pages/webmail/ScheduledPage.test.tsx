import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type ScheduledSend } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import ScheduledPage from './ScheduledPage';
import { META, renderScreen } from './testing';

const ROW: ScheduledSend = {
  id: 's1',
  send_at: new Date(Date.now() + 3 * 86_400_000).toISOString(),
  subject: 'Informe mensual',
  recipients: ['jefe@empresa.com'],
  created_at: '2026-09-24T08:00:00Z',
  status: 'pending',
};

function renderScheduled() {
  return renderScreen(<ScheduledPage />, { path: '/webmail/scheduled' });
}

describe('envios programados', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
  });
  afterEach(() => vi.restoreAllMocks());

  it('lista los pendientes y cancela uno: vuelve a Borradores', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'scheduled').mockResolvedValue([ROW]);
    const cancel = vi.spyOn(webmailApi, 'cancelScheduled').mockResolvedValue(null);
    const outlet = renderScheduled();

    expect(await screen.findByText('Informe mensual')).toBeInTheDocument();
    expect(screen.getByText(t('webmail.scheduled.status.pending'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('webmail.scheduled.cancel') }));
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: t('webmail.scheduled.cancel'),
      }),
    );
    await waitFor(() => expect(cancel).toHaveBeenCalledWith('s1'));
    expect(await screen.findByText(t('webmail.scheduled.empty'))).toBeInTheDocument();
    expect(outlet.reloadFolders).toHaveBeenCalled();
  });

  it('cambiar la hora de uno que ya esta saliendo explica el 409 y relee la lista', async () => {
    const user = userEvent.setup();
    const list = vi
      .spyOn(webmailApi, 'scheduled')
      .mockResolvedValueOnce([ROW])
      .mockResolvedValue([{ ...ROW, status: 'sending' }]);
    vi.spyOn(webmailApi, 'reschedule').mockRejectedValue(
      new ApiError(409, { code: 'SCHEDULED_SEND_NOT_PENDING', message: '' }),
    );
    renderScheduled();

    await user.click(
      await screen.findByRole('button', { name: t('webmail.scheduled.reschedule') }),
    );
    const dialog = screen.getByRole('dialog');
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.scheduled.reschedule') }),
    );
    expect(
      await within(dialog).findByText(t('error.code.SCHEDULED_SEND_NOT_PENDING')),
    ).toBeInTheDocument();
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it('cambia la hora de un envio pendiente', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'scheduled').mockResolvedValue([ROW]);
    const reschedule = vi
      .spyOn(webmailApi, 'reschedule')
      .mockImplementation(async (id, sendAt) => ({ ...ROW, id, send_at: sendAt }));
    renderScheduled();

    await user.click(
      await screen.findByRole('button', { name: t('webmail.scheduled.reschedule') }),
    );
    const dialog = screen.getByRole('dialog');
    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.scheduled.reschedule') }),
    );
    await waitFor(() => expect(reschedule).toHaveBeenCalledWith('s1', expect.any(String)));
  });
});
