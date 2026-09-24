import { getLocale, t, type MessageKey } from '@/i18n';

const DAY_MS = 86_400_000;
// El contrato de POST /send exige send_at al menos un minuto en el futuro; el servicio lo
// vuelve a comprobar con su reloj.
const MIN_LEAD_MS = 60_000;

export interface SchedulePreset {
  id: string;
  label: MessageKey;
  at: Date;
}

function at(base: Date, days: number, hours: number): Date {
  const date = new Date(base);
  date.setDate(date.getDate() + days);
  date.setHours(hours, 0, 0, 0);
  return date;
}

/** Atajos habituales en la hora local de quien escribe. */
export function schedulePresets(now: Date): SchedulePreset[] {
  const untilMonday = (8 - now.getDay()) % 7 || 7;
  return [
    { id: 'tomorrow-morning', label: 'webmail.schedule.tomorrowMorning', at: at(now, 1, 8) },
    { id: 'tomorrow-afternoon', label: 'webmail.schedule.tomorrowAfternoon', at: at(now, 1, 13) },
    { id: 'monday-morning', label: 'webmail.schedule.mondayMorning', at: at(now, untilMonday, 8) },
  ];
}

/** Por que no vale esa hora; null si vale. maxDays llega de la meta del servicio. */
export function scheduleProblem(
  when: Date | null,
  now: Date,
  maxDays: number | null,
): string | null {
  if (!when || Number.isNaN(when.getTime())) return t('webmail.schedule.required');
  if (when.getTime() < now.getTime() + MIN_LEAD_MS) return t('webmail.schedule.tooSoon');
  if (maxDays !== null && when.getTime() > now.getTime() + maxDays * DAY_MS) {
    return t('webmail.schedule.tooFar', { days: maxDays });
  }
  return null;
}

const pad = (n: number) => String(n).padStart(2, '0');

/** Valor de un input datetime-local en la hora local. */
export function toLocalInput(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
}

export function fromLocalInput(value: string): Date | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

/** "mar 24 sept, 08:00" en el idioma de la aplicacion. */
export function formatScheduled(value: string | Date): string {
  const date = typeof value === 'string' ? new Date(value) : value;
  if (Number.isNaN(date.getTime())) return t('common.dash');
  return new Intl.DateTimeFormat(getLocale(), {
    weekday: 'short',
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}
