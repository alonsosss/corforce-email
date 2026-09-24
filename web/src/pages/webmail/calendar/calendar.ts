import {
  RECURRENCE_FREQUENCIES,
  WEEKDAYS,
  type Attendee,
  type BusyInterval,
  type CalendarEvent,
  type CalendarEventInput,
  type Occurrence,
  type RecurrenceFrequency,
  type Weekday,
} from '@/api/webmail';
import { getLocale, t } from '@/i18n';

export const CALENDAR_VIEWS = ['month', 'week', 'agenda'] as const;
export type CalendarView = (typeof CALENDAR_VIEWS)[number];

const DAY_MS = 86_400_000;
// La cuadricula del mes son seis semanas completas (42 dias). Si la ventana que admite el
// servicio fuera menor, splitWindow parte la peticion.
const MONTH_GRID_DAYS = 42;

const pad = (n: number) => String(n).padStart(2, '0');

/** Dia local como AAAA-MM-DD. */
export function dayKey(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

export function parseDay(key: string): Date | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(key)) return null;
  const [y, m, d] = key.split('-').map(Number) as [number, number, number];
  const date = new Date(y, m - 1, d);
  return date.getMonth() === m - 1 && date.getDate() === d ? date : null;
}

export function addDays(date: Date, days: number): Date {
  const next = new Date(date);
  next.setDate(next.getDate() + days);
  return next;
}

/** Semanas de lunes a domingo, como en el calendario de uso comun en espanol. */
export function startOfWeek(date: Date): Date {
  const start = new Date(date.getFullYear(), date.getMonth(), date.getDate());
  const offset = (start.getDay() + 6) % 7;
  return addDays(start, -offset);
}

export interface VisibleRange {
  start: Date;
  /** Exclusivo. */
  end: Date;
  days: Date[];
}

/** Lo que se ve y, por tanto, lo unico que se pide al servicio. */
export function visibleRange(view: CalendarView, anchor: Date): VisibleRange {
  let start: Date;
  let count: number;
  if (view === 'week') {
    start = startOfWeek(anchor);
    count = 7;
  } else if (view === 'month') {
    start = startOfWeek(new Date(anchor.getFullYear(), anchor.getMonth(), 1));
    count = MONTH_GRID_DAYS;
  } else {
    start = new Date(anchor.getFullYear(), anchor.getMonth(), 1);
    const next = new Date(anchor.getFullYear(), anchor.getMonth() + 1, 1);
    count = Math.round((next.getTime() - start.getTime()) / DAY_MS);
  }
  const days = Array.from({ length: count }, (_, i) => addDays(start, i));
  return { start, end: addDays(start, count), days };
}

/** Tramos consecutivos de como mucho maxDays dias; sin tope, uno solo. */
export function splitWindow(
  start: Date,
  end: Date,
  maxDays: number | null,
): { start: Date; end: Date }[] {
  if (!maxDays) return [{ start, end }];
  const parts: { start: Date; end: Date }[] = [];
  for (let from = start; from < end;) {
    const limit = addDays(from, maxDays);
    const to = limit < end ? limit : end;
    parts.push({ start: from, end: to });
    from = to;
  }
  return parts;
}

/** Ocurrencias de varios tramos sin repetir las que caen en el borde de dos. */
export function mergeOccurrences(parts: readonly Occurrence[][]): Occurrence[] {
  const seen = new Set<string>();
  const out: Occurrence[] = [];
  for (const occurrence of parts.flat()) {
    const key = `${occurrence.id}|${occurrence.recurrence_id || occurrence.start}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(occurrence);
  }
  return out;
}

export function shiftAnchor(view: CalendarView, anchor: Date, direction: 1 | -1): Date {
  if (view === 'week') return addDays(anchor, 7 * direction);
  return new Date(anchor.getFullYear(), anchor.getMonth() + direction, 1);
}

/** Dias (claves locales) que ocupa una ocurrencia. Todo el dia: fechas UTC, fin exclusivo. */
export function occurrenceDays(
  occurrence: Pick<Occurrence, 'start' | 'end' | 'all_day'>,
): string[] {
  if (occurrence.all_day) {
    const first = parseDay(occurrence.start.slice(0, 10));
    if (!first) return [];
    const last = parseDay(occurrence.end.slice(0, 10));
    const keys = [dayKey(first)];
    if (!last) return keys;
    for (let day = addDays(first, 1); day < last && keys.length <= 366; day = addDays(day, 1)) {
      keys.push(dayKey(day));
    }
    return keys;
  }
  const start = new Date(occurrence.start);
  const end = new Date(occurrence.end);
  if (Number.isNaN(start.getTime())) return [];
  const keys = [dayKey(start)];
  // Un evento que termina justo a medianoche no ocupa el dia siguiente.
  const lastInstant = Number.isNaN(end.getTime()) ? start : new Date(end.getTime() - 1);
  for (let day = addDays(start, 1); dayKey(day) <= dayKey(lastInstant); day = addDays(day, 1)) {
    keys.push(dayKey(day));
    if (keys.length > 366) break;
  }
  return keys;
}

/** Ocurrencias agrupadas por dia; las de todo el dia primero y el resto por hora. */
export function groupByDay(occurrences: readonly Occurrence[]): Map<string, Occurrence[]> {
  const byDay = new Map<string, Occurrence[]>();
  for (const occurrence of occurrences) {
    for (const key of occurrenceDays(occurrence)) {
      const list = byDay.get(key) ?? [];
      list.push(occurrence);
      byDay.set(key, list);
    }
  }
  for (const list of byDay.values()) {
    list.sort((a, b) =>
      a.all_day !== b.all_day ? (a.all_day ? -1 : 1) : a.start.localeCompare(b.start),
    );
  }
  return byDay;
}

/** Zona IANA del navegador; UTC si no la informa. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/** Zonas IANA que conoce el navegador, con la del navegador, UTC y la que ya se usa. */
export function timeZoneOptions(current: string): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  let known: string[] = [];
  try {
    known = intl.supportedValuesOf?.('timeZone') ?? [];
  } catch {
    known = [];
  }
  const all = new Set([...known, browserTimeZone(), 'UTC']);
  if (current) all.add(current);
  return [...all].sort((a, b) => a.localeCompare(b));
}

interface WallClock {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
}

/** Reloj de pared de un instante en una zona IANA; null si el navegador no la conoce. */
function wallClock(instant: number, zone: string): WallClock | null {
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat('en-US', {
      timeZone: zone,
      hourCycle: 'h23',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).formatToParts(new Date(instant));
  } catch {
    return null;
  }
  const get = (type: string) => Number(parts.find((p) => p.type === type)?.value ?? NaN);
  const clock = {
    year: get('year'),
    month: get('month'),
    day: get('day'),
    hour: get('hour') % 24,
    minute: get('minute'),
    second: get('second'),
  };
  return Object.values(clock).some(Number.isNaN) ? null : clock;
}

const clockMs = (c: WallClock) => Date.UTC(c.year, c.month - 1, c.day, c.hour, c.minute, c.second);

/** Fecha (AAAA-MM-DD) y hora (HH:MM) de un instante en una zona; en la del navegador si no la conoce. */
export function wallInZone(instant: Date, zone: string): { date: string; time: string } {
  const c = wallClock(instant.getTime(), zone);
  if (!c)
    return {
      date: dayKey(instant),
      time: `${pad(instant.getHours())}:${pad(instant.getMinutes())}`,
    };
  return {
    date: `${c.year}-${pad(c.month)}-${pad(c.day)}`,
    time: `${pad(c.hour)}:${pad(c.minute)}`,
  };
}

/**
 * Instante de una fecha y una hora de pared en una zona IANA (la del navegador si no la conoce). En el
 * salto de un cambio de hora gana el desfase posterior, como en los clientes de calendario habituales.
 */
export function instantInZone(date: string, clock: string, zone: string): Date | null {
  const day = parseDay(date);
  if (!day || !/^\d{2}:\d{2}$/.test(clock)) return null;
  const [h, m] = clock.split(':').map(Number) as [number, number];
  if (h > 23 || m > 59) return null;
  const guess = Date.UTC(day.getFullYear(), day.getMonth(), day.getDate(), h, m);
  const offset = (t: number) => {
    const c = wallClock(t, zone);
    return c ? clockMs(c) - t : null;
  };
  const first = offset(guess);
  if (first === null) return new Date(day.getFullYear(), day.getMonth(), day.getDate(), h, m);
  let instant = guess - first;
  const second = offset(instant);
  if (second !== null && second !== first) instant = guess - second;
  return new Date(instant);
}

/**
 * Intervalo de un evento en la zona del navegador: fecha y horas, o las fechas de un evento de todo el dia (fin
 * exclusivo, fechas UTC).
 */
export function formatEventWhen(start: string, end: string, allDay: boolean): string {
  const locale = getLocale();
  if (allDay) {
    const first = parseDay(start.slice(0, 10));
    const last = parseDay(end.slice(0, 10));
    if (!first) return '';
    const date = new Intl.DateTimeFormat(locale, { dateStyle: 'medium' });
    const lastDay = last ? addDays(last, -1) : first;
    return lastDay > first ? `${date.format(first)} - ${date.format(lastDay)}` : date.format(first);
  }
  const from = new Date(start);
  const to = new Date(end);
  if (Number.isNaN(from.getTime())) return '';
  const full = new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' });
  if (Number.isNaN(to.getTime())) return full.format(from);
  const sameDay = dayKey(from) === dayKey(to);
  const tail = sameDay
    ? new Intl.DateTimeFormat(locale, { timeStyle: 'short' }).format(to)
    : full.format(to);
  return `${full.format(from)} - ${tail}`;
}

/** Tramos ocupados de una lista que se cruzan con [start, end). */
export function overlapping(busy: readonly BusyInterval[], start: Date, end: Date): BusyInterval[] {
  return busy.filter((b) => new Date(b.start) < end && new Date(b.end) > start);
}

/** Formulario del evento, en la hora de pared de su zona. */
export interface EventForm {
  title: string;
  allDay: boolean;
  /** Zona IANA del evento; vacia conserva la del evento guardado (UTC) y se edita en la del navegador. */
  timezone: string;
  startDate: string;
  startTime: string;
  endDate: string;
  endTime: string;
  location: string;
  description: string;
  repeat: RecurrenceFrequency | '';
  interval: string;
  ends: 'never' | 'count' | 'until';
  count: string;
  until: string;
  byDay: Weekday[];
  /** Minutos antes; vacio sin recordatorio. */
  reminder: string;
  /** Direcciones de los invitados. */
  attendees: string[];
  /** Invitados tal como estan guardados (nombre y respuesta), para conservarlos al guardar. */
  known: Attendee[];
}

/** Zona en la que se leen y escriben las horas del formulario. */
export const formZone = (form: Pick<EventForm, 'timezone'>) => form.timezone || browserTimeZone();

const time = (date: Date) => `${pad(date.getHours())}:${pad(date.getMinutes())}`;

/** Formulario de un evento nuevo en ese dia, a la siguiente hora en punto. */
export function newEventForm(day: Date, now: Date = new Date()): EventForm {
  const start = new Date(day.getFullYear(), day.getMonth(), day.getDate(), now.getHours() + 1);
  const end = new Date(start.getTime() + 3_600_000);
  return {
    title: '',
    allDay: false,
    startDate: dayKey(start),
    startTime: time(start),
    endDate: dayKey(end),
    endTime: time(end),
    location: '',
    description: '',
    repeat: '',
    interval: '1',
    ends: 'never',
    count: '10',
    until: '',
    byDay: [],
    reminder: '',
    attendees: [],
    known: [],
    timezone: browserTimeZone(),
  };
}

export function eventToForm(event: CalendarEvent): EventForm {
  const base = newEventForm(new Date());
  let startDate: string;
  let endDate: string;
  let startTime = base.startTime;
  let endTime = base.endTime;
  if (event.all_day) {
    startDate = event.start.slice(0, 10);
    // El fin de un evento de todo el dia es exclusivo; el formulario ensena el ultimo dia.
    const end = parseDay(event.end.slice(0, 10));
    endDate = end ? dayKey(addDays(end, -1)) : startDate;
    if (endDate < startDate) endDate = startDate;
  } else {
    const zone = event.timezone || browserTimeZone();
    const start = wallInZone(new Date(event.start), zone);
    const end = wallInZone(new Date(event.end), zone);
    startDate = start.date;
    startTime = start.time;
    endDate = end.date;
    endTime = end.time;
  }
  const recurrence = event.recurrence;
  return {
    title: event.title,
    allDay: event.all_day,
    timezone: event.timezone,
    attendees: event.attendees.map((a) => a.email),
    known: event.attendees,
    startDate,
    startTime,
    endDate,
    endTime,
    location: event.location,
    description: event.description,
    repeat: recurrence?.freq ?? '',
    interval: String(recurrence?.interval ?? 1),
    ends: recurrence?.count ? 'count' : recurrence?.until ? 'until' : 'never',
    count: String(recurrence?.count ?? 10),
    until: recurrence?.until ? dayKey(new Date(recurrence.until)) : '',
    byDay: recurrence?.by_day ?? [],
    reminder: event.reminder_minutes === null ? '' : String(event.reminder_minutes),
  };
}

function positiveInt(value: string): number | null {
  if (!/^\d{1,4}$/.test(value.trim())) return null;
  const n = Number(value);
  return n >= 1 ? n : null;
}

export type EventField =
  'title' | 'start' | 'end' | 'location' | 'description' | 'interval' | 'count' | 'until';

/** details.field del servicio -> campo del formulario. */
export const EVENT_API_FIELDS: Record<string, EventField> = {
  title: 'title',
  start: 'start',
  end: 'end',
  location: 'location',
  description: 'description',
  'recurrence.interval': 'interval',
  'recurrence.count': 'count',
  'recurrence.until': 'until',
};

export function eventProblems(form: EventForm): Partial<Record<EventField, string>> {
  const problems: Partial<Record<EventField, string>> = {};
  if (!form.title.trim()) problems.title = t('webmail.calendar.titleRequired');
  if (form.allDay) {
    if (!parseDay(form.startDate)) problems.start = t('webmail.calendar.dateInvalid');
    else if (!parseDay(form.endDate) || form.endDate < form.startDate) {
      problems.end = t('webmail.calendar.endBeforeStart');
    }
  } else {
    const start = instantInZone(form.startDate, form.startTime, formZone(form));
    const end = instantInZone(form.endDate, form.endTime, formZone(form));
    if (!start) problems.start = t('webmail.calendar.dateInvalid');
    else if (!end || end <= start) problems.end = t('webmail.calendar.endBeforeStart');
  }
  if (form.repeat) {
    if (!positiveInt(form.interval)) problems.interval = t('webmail.calendar.intervalInvalid');
    if (form.ends === 'count' && !positiveInt(form.count)) {
      problems.count = t('webmail.calendar.countInvalid');
    }
    if (form.ends === 'until' && (!parseDay(form.until) || form.until < form.startDate)) {
      problems.until = t('webmail.calendar.untilInvalid');
    }
  }
  return problems;
}

const utcMidnight = (key: string) => `${key}T00:00:00Z`;

/** Cuerpo del API. Solo se llama con un formulario sin problemas. */
export function formToInput(form: EventForm): CalendarEventInput {
  let start: string;
  let end: string;
  if (form.allDay) {
    start = utcMidnight(form.startDate);
    const last = parseDay(form.endDate) ?? parseDay(form.startDate) ?? new Date();
    end = utcMidnight(dayKey(addDays(last, 1)));
  } else {
    start = (
      instantInZone(form.startDate, form.startTime, formZone(form)) ?? new Date()
    ).toISOString();
    end = (instantInZone(form.endDate, form.endTime, formZone(form)) ?? new Date()).toISOString();
  }
  const freq = RECURRENCE_FREQUENCIES.find((f) => f === form.repeat);
  const untilDay = parseDay(form.until);
  return {
    title: form.title.trim(),
    start,
    end,
    all_day: form.allDay,
    timezone: form.allDay ? '' : form.timezone,
    location: form.location.trim(),
    description: form.description.trim(),
    recurrence: freq
      ? {
          freq,
          interval: positiveInt(form.interval) ?? 1,
          count: form.ends === 'count' ? positiveInt(form.count) : null,
          until:
            form.ends === 'until' && untilDay
              ? form.allDay
                ? new Date(
                    untilDay.getFullYear(),
                    untilDay.getMonth(),
                    untilDay.getDate(),
                    23,
                    59,
                    59,
                  ).toISOString()
                : (instantInZone(form.until, '23:59', formZone(form)) ?? untilDay).toISOString()
              : null,
          by_day: freq === 'weekly' && form.byDay.length ? form.byDay : null,
        }
      : null,
    reminder_minutes: form.reminder === '' ? null : Number(form.reminder),
    attendees: form.attendees.map((email) => {
      const known = form.known.find((a) => a.email.toLowerCase() === email.toLowerCase());
      return known ?? { email, name: '', partstat: 'NEEDS-ACTION' };
    }),
  };
}

/** Formulario de una sola aparicion de una serie: la serie con las horas y el texto de esa aparicion. */
export function occurrenceToForm(
  event: CalendarEvent,
  occurrence: Pick<Occurrence, 'start' | 'end' | 'title' | 'location'>,
): EventForm {
  const series = eventToForm(event);
  const form = eventToForm({
    ...event,
    start: occurrence.start,
    end: occurrence.end,
    recurrence: null,
  });
  return {
    ...form,
    title: occurrence.title || series.title,
    location: occurrence.location,
    repeat: '',
  };
}

/** Dia de la semana iCalendar de una fecha local. */
export function weekdayOf(date: Date): Weekday {
  return WEEKDAYS[(date.getDay() + 6) % 7] ?? 'MO';
}
