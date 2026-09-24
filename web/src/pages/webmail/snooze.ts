import { FOLDER_ROLES } from '@/api/webmail';
import { t, type MessageKey } from '@/i18n';

const DAY_MS = 86_400_000;
// El servicio exige al menos un minuto de margen (domain.MinScheduleLead) y lo vuelve a comprobar.
const MIN_LEAD_MS = 60_000;
// "Esta tarde" solo se ofrece si falta al menos este margen para su hora.
const AFTERNOON_MARGIN_MS = 3_600_000;
const AFTERNOON_HOUR = 17;
const MORNING_HOUR = 8;

/** Plazos del seguimiento que se ofrecen al redactar, en dias. */
export const FOLLOW_UP_DAYS = [1, 3, 7] as const;

export interface SnoozePreset {
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

/** Atajos para posponer en la hora local: esta tarde (si aun da tiempo), manana y el lunes. */
export function snoozePresets(now: Date): SnoozePreset[] {
  const presets: SnoozePreset[] = [];
  const afternoon = at(now, 0, AFTERNOON_HOUR);
  if (afternoon.getTime() - now.getTime() >= AFTERNOON_MARGIN_MS) {
    presets.push({ id: 'this-afternoon', label: 'webmail.snooze.thisAfternoon', at: afternoon });
  }
  const untilMonday = (8 - now.getDay()) % 7 || 7;
  presets.push(
    { id: 'tomorrow', label: 'webmail.snooze.tomorrow', at: at(now, 1, MORNING_HOUR) },
    { id: 'monday', label: 'webmail.snooze.monday', at: at(now, untilMonday, MORNING_HOUR) },
  );
  return presets;
}

/** Por que no vale esa hora de vuelta; null si vale. maxDays llega de la meta del servicio. */
export function snoozeProblem(when: Date | null, now: Date, maxDays: number | null): string | null {
  if (!when || Number.isNaN(when.getTime())) return t('webmail.snooze.required');
  if (when.getTime() < now.getTime() + MIN_LEAD_MS) return t('webmail.snooze.tooSoon');
  if (maxDays !== null && when.getTime() > now.getTime() + maxDays * DAY_MS) {
    return t('webmail.snooze.tooFar', { days: maxDays });
  }
  return null;
}

/** Desde Pospuestos se cambia la hora; Programados y Borradores no se posponen. */
export function canSnooze(role: string): boolean {
  return (
    role !== FOLDER_ROLES.snoozed && role !== FOLDER_ROLES.scheduled && role !== FOLDER_ROLES.drafts
  );
}

/** Plazos de seguimiento que caben en el tope del servicio. */
export function followUpChoices(maxDays: number | null): number[] {
  return FOLLOW_UP_DAYS.filter((days) => maxDays === null || days <= maxDays);
}
