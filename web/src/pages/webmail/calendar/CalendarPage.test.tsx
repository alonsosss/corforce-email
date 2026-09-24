import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type CalendarEvent, type Occurrence } from '@/api/webmail';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { renderScreen } from '../testing';
import CalendarPage from './CalendarPage';

const WEEKLY: Occurrence = {
  id: 'ev1',
  start: '2026-09-15T09:00:00Z',
  end: '2026-09-15T10:00:00Z',
  all_day: false,
  title: 'Comite',
  location: 'Sala 2',
  recurring: true,
};

const EVENT: CalendarEvent = {
  id: 'ev1',
  etag: '"7"',
  title: 'Comite',
  start: '2026-09-15T09:00:00Z',
  end: '2026-09-15T10:00:00Z',
  all_day: false,
  location: 'Sala 2',
  description: '',
  recurrence: { freq: 'weekly', interval: 1, count: null, until: null, by_day: ['TU'] },
  reminder_minutes: 15,
};

function renderCalendar(url = '/webmail/calendar?view=month&date=2026-09-15') {
  renderScreen(<CalendarPage />, { path: '/webmail/calendar', url });
}

describe('calendario', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'davMeta').mockResolvedValue({ limits: {} });
  });
  afterEach(() => vi.restoreAllMocks());

  it('pide solo la ventana visible del mes y pinta sus eventos', async () => {
    const list = vi.spyOn(webmailApi, 'calendarOccurrences').mockResolvedValue([WEEKLY]);
    renderCalendar();

    expect(await screen.findByRole('button', { name: /Comite/ })).toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(1);
    const [start, end] = list.mock.calls[0] ?? [];
    const days = (new Date(String(end)).getTime() - new Date(String(start)).getTime()) / 86_400_000;
    expect(Math.round(days)).toBe(42);
  });

  it('parte la peticion si el servicio admite una ventana menor', async () => {
    vi.mocked(webmailApi.davMeta).mockResolvedValue({
      limits: { max_event_window_days: 31 },
    });
    const list = vi.spyOn(webmailApi, 'calendarOccurrences').mockResolvedValue([WEEKLY]);
    renderCalendar();

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    expect(await screen.findAllByRole('button', { name: /Comite/ })).toHaveLength(1);
  });

  it('si no se pueden leer los eventos lo dice y deja reintentar', async () => {
    const user = userEvent.setup();
    const list = vi
      .spyOn(webmailApi, 'calendarOccurrences')
      .mockRejectedValueOnce(new ApiError(503, { code: 'SERVICE_UNAVAILABLE', message: '' }))
      .mockResolvedValue([]);
    renderCalendar();

    expect(await screen.findByText(t('error.serviceUnavailable'))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('common.retry') }));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });

  it('editar una ocurrencia avisa de que cambia la serie y guarda con If-Match', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'calendarOccurrences').mockResolvedValue([WEEKLY]);
    vi.spyOn(webmailApi, 'calendarEvent').mockResolvedValue(EVENT);
    const update = vi
      .spyOn(webmailApi, 'updateCalendarEvent')
      .mockImplementation(async (_id, input) => ({ ...EVENT, ...input }));
    renderCalendar();

    await user.click(await screen.findByRole('button', { name: /Comite/ }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getAllByText(t('webmail.calendar.seriesNote')).length).toBeGreaterThan(0);
    const title = within(dialog).getByLabelText(new RegExp(t('webmail.calendar.eventTitle')));
    await user.clear(title);
    await user.type(title, 'Comite mensual');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));

    await waitFor(() =>
      expect(update).toHaveBeenCalledWith(
        'ev1',
        expect.objectContaining({
          title: 'Comite mensual',
          recurrence: expect.objectContaining({ freq: 'weekly', by_day: ['TU'] }),
          reminder_minutes: 15,
        }),
        '"7"',
      ),
    );
  });

  it('crea un evento de todo el dia y rechaza un fin anterior al inicio', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'calendarOccurrences').mockResolvedValue([]);
    const create = vi
      .spyOn(webmailApi, 'createCalendarEvent')
      .mockImplementation(async (input) => ({ ...input, id: 'n1', etag: '"1"' }));
    renderCalendar();

    await user.click(await screen.findByRole('button', { name: t('webmail.calendar.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(
      within(dialog).getByLabelText(new RegExp(t('webmail.calendar.eventTitle'))),
      'Vacaciones',
    );
    await user.click(within(dialog).getByLabelText(t('webmail.calendar.allDay')));
    const start = within(dialog).getByLabelText(t('webmail.calendar.start'));
    const end = within(dialog).getByLabelText(t('webmail.calendar.end'));
    await user.clear(start);
    await user.type(start, '2026-09-20');
    await user.clear(end);
    await user.type(end, '2026-09-18');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    expect(within(dialog).getByText(t('webmail.calendar.endBeforeStart'))).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();

    await user.clear(end);
    await user.type(end, '2026-09-22');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({
          title: 'Vacaciones',
          all_day: true,
          start: '2026-09-20T00:00:00Z',
          end: '2026-09-23T00:00:00Z',
        }),
      ),
    );
  });

  it('borrar un evento que se repite borra la serie tras confirmar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'calendarOccurrences').mockResolvedValue([WEEKLY]);
    vi.spyOn(webmailApi, 'calendarEvent').mockResolvedValue(EVENT);
    const remove = vi.spyOn(webmailApi, 'deleteCalendarEvent').mockResolvedValue(null);
    renderCalendar('/webmail/calendar?view=agenda&date=2026-09-15');

    await user.click(await screen.findByRole('button', { name: /Comite/ }));
    await user.click(
      await screen.findByRole('button', { name: t('webmail.calendar.deleteSeries') }),
    );
    const confirm = screen.getByRole('dialog');
    expect(
      within(confirm).getByText(t('webmail.calendar.deleteSeriesConfirm', { title: 'Comite' })),
    ).toBeInTheDocument();
    await user.click(within(confirm).getByRole('button', { name: t('common.delete') }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith('ev1'));
  });
});
