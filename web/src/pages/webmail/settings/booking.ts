import { WEEKDAYS, type BookingSettings, type Weekday } from '@/api/webmail';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { browserTimeZone } from '../calendar/calendar';

/** Formulario de la pagina de citas: los numeros como texto mientras se escriben, las horas de antelacion en horas. */
export interface BookingForm {
  title: string;
  description: string;
  duration: string;
  buffer: string;
  noticeHours: string;
  advanceDays: string;
  dailyLimit: string;
  timezone: string;
  weekly: Record<Weekday, { start: string; end: string }[]>;
  active: boolean;
}

export type BookingField =
  'title' | 'duration' | 'buffer' | 'noticeHours' | 'advanceDays' | 'dailyLimit' | 'weekly';

/** details.field del servicio -> campo del formulario. */
export const BOOKING_API_FIELDS: Record<string, BookingField> = {
  title: 'title',
  duration_minutes: 'duration',
  buffer_minutes: 'buffer',
  min_notice_minutes: 'noticeHours',
  max_advance_days: 'advanceDays',
  daily_limit: 'dailyLimit',
};

// Propuesta para una pagina nueva: de lunes a viernes en horario de oficina, sin publicar.
const DEFAULT_WINDOW = { start: '09:00', end: '17:00' };
const WORKDAYS: readonly Weekday[] = ['MO', 'TU', 'WE', 'TH', 'FR'];

export function newBookingForm(): BookingForm {
  const weekly = Object.fromEntries(
    WEEKDAYS.map((d) => [d, WORKDAYS.includes(d) ? [{ ...DEFAULT_WINDOW }] : []]),
  ) as BookingForm['weekly'];
  return {
    title: '',
    description: '',
    duration: '30',
    buffer: '0',
    noticeHours: '2',
    advanceDays: '30',
    dailyLimit: '10',
    timezone: browserTimeZone(),
    weekly,
    active: false,
  };
}

export function settingsToForm(s: BookingSettings): BookingForm {
  const weekly = Object.fromEntries(
    WEEKDAYS.map((d) => [
      d,
      (s.weekly[d] ?? []).map((w) => ({
        start: w.start,
        end: w.end === '24:00' ? '00:00' : w.end,
      })),
    ]),
  ) as BookingForm['weekly'];
  return {
    title: s.title,
    description: s.description,
    duration: String(s.duration_minutes),
    buffer: String(s.buffer_minutes),
    noticeHours: String(Math.round(s.min_notice_minutes / 60)),
    advanceDays: String(s.max_advance_days),
    dailyLimit: String(s.daily_limit),
    timezone: s.timezone,
    weekly,
    active: s.active,
  };
}

const wholeNumber = (v: string) => (/^\d{1,6}$/.test(v.trim()) ? Number(v) : null);
const minutesOf = (clock: string) => {
  if (!/^\d{2}:\d{2}$/.test(clock)) return null;
  const [h, m] = clock.split(':').map(Number) as [number, number];
  return h * 60 + m;
};

/** Problemas que se ven antes de enviar; los rangos exactos los valida mail-dav. */
export function bookingProblems(form: BookingForm): Partial<Record<BookingField, string>> {
  const problems: Partial<Record<BookingField, string>> = {};
  if (!form.title.trim()) problems.title = t('webmail.booking.titleRequired');
  for (const field of ['duration', 'buffer', 'noticeHours', 'advanceDays', 'dailyLimit'] as const) {
    if (wholeNumber(form[field]) === null) problems[field] = t('webmail.booking.numberInvalid');
  }
  const duration = wholeNumber(form.duration) ?? 0;
  for (const day of WEEKDAYS) {
    for (const w of form.weekly[day]) {
      const start = minutesOf(w.start);
      const end = w.end === '00:00' ? 24 * 60 : minutesOf(w.end);
      if (start === null || end === null || end - start < duration) {
        problems.weekly = t('webmail.booking.windowInvalid');
      }
    }
  }
  return problems;
}

/** Cuerpo del API. Solo con un formulario sin problemas. */
export function formToSettings(form: BookingForm): BookingSettings {
  return {
    title: form.title.trim(),
    description: form.description.trim(),
    duration_minutes: Number(form.duration),
    buffer_minutes: Number(form.buffer),
    min_notice_minutes: Number(form.noticeHours) * 60,
    max_advance_days: Number(form.advanceDays),
    daily_limit: Number(form.dailyLimit),
    timezone: form.timezone,
    weekly: Object.fromEntries(
      WEEKDAYS.map((d) => [
        d,
        form.weekly[d].map((w) => ({ start: w.start, end: w.end === '00:00' ? '24:00' : w.end })),
      ]),
    ),
    active: form.active,
  };
}

/** Enlace publico completo de una pagina de citas, en el mismo origen que la aplicacion. */
export function bookingLink(
  page: { cell: string; tenant_id: string; public_id: string },
  origin: string,
): string {
  return `${origin}${paths.publicBooking(page.cell, page.tenant_id, page.public_id)}`;
}
