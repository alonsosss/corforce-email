import type { ExtractedEvent, ExtractedTask } from '@/api/webmailAssistant';
import { formatDate } from '@/lib/format';
import { t } from '@/i18n';
import { dayKey, newEventForm, parseDay, type EventForm } from '../calendar/calendar';

/** Clave del estado de navegacion con el que el lector pasa a la redaccion una respuesta propuesta. */
const STATE_KEY = 'assistantText';

export function assistantNavigationState(text: string): Record<string, string> {
  return { [STATE_KEY]: text };
}

/** Texto propuesto que llega en el estado de la navegacion, si lo hay. */
export function assistantTextFromState(state: unknown): string | null {
  if (typeof state !== 'object' || state === null) return null;
  const value = (state as Record<string, unknown>)[STATE_KEY];
  return typeof value === 'string' && value.trim() !== '' ? value : null;
}

/** Fecha local de hoy (AAAA-MM-DD): el servicio resuelve con ella "manana" o "el jueves". */
export function todayKey(now: Date = new Date()): string {
  return dayKey(now);
}

/** Caracteres como los cuenta el servicio (runas), no unidades UTF-16. */
export function charCount(text: string): number {
  return Array.from(text).length;
}

const pad = (n: number) => String(n).padStart(2, '0');

function plusOneHour(clock: string): { day: number; time: string } {
  const [h, m] = clock.split(':').map(Number) as [number, number];
  const total = h * 60 + m + 60;
  return {
    day: Math.floor(total / 1440),
    time: `${pad(Math.floor(total / 60) % 24)}:${pad(total % 60)}`,
  };
}

/**
 * Formulario del calendario para una cita propuesta, en la hora local de quien la crea. Sin hora es un
 * evento de todo el dia; sin hora de fin dura una hora. null si la fecha no es valida.
 */
export function eventFormFromExtraction(
  e: ExtractedEvent,
  now: Date = new Date(),
): EventForm | null {
  const day = parseDay(e.date);
  if (!day || !e.title.trim()) return null;
  const form: EventForm = {
    ...newEventForm(day, now),
    title: e.title.trim(),
    location: e.location.trim(),
    description: e.notes.trim(),
  };
  if (!/^\d{2}:\d{2}$/.test(e.start_time)) {
    return { ...form, allDay: true, startDate: e.date, endDate: e.date };
  }
  let endDate = e.date;
  let endTime = e.end_time;
  if (!/^\d{2}:\d{2}$/.test(endTime) || endTime <= e.start_time) {
    const next = plusOneHour(e.start_time);
    endTime = next.time;
    if (next.day > 0) {
      endDate = dayKey(new Date(day.getFullYear(), day.getMonth(), day.getDate() + next.day));
    }
  }
  return { ...form, allDay: false, startDate: e.date, startTime: e.start_time, endDate, endTime };
}

/** Una tarea con fecha se anade al calendario como evento de todo el dia de su vencimiento. */
export function eventFormFromTask(task: ExtractedTask, now: Date = new Date()): EventForm | null {
  if (!task.due_date) return null;
  return eventFormFromExtraction(
    {
      title: task.title,
      date: task.due_date,
      start_time: '',
      end_time: '',
      location: '',
      notes: '',
    },
    now,
  );
}

/** Cuando ocurre una cita propuesta, para la confirmacion. */
export function describeWhen(form: EventForm): string {
  const date = formatDate(`${form.startDate}T12:00:00`);
  return form.allDay
    ? `${date} (${t('webmail.assistant.extract.allDay')})`
    : `${date} ${form.startTime}`;
}
