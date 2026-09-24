import type { BookingSlot } from '@/api/booking';

const DAY_MS = 86_400_000;
/** Dias que se ven de una vez en la pagina publica. */
export const BOOKING_PAGE_DAYS = 7;

const pad = (n: number) => String(n).padStart(2, '0');

/** Dia local (zona del visitante) como AAAA-MM-DD. */
export function localDay(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** Ventana de dias que se pide: desde el inicio del dia de hoy mas offset semanas. */
export function bookingWindow(now: Date, offset: number): { start: Date; end: Date } {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const start = new Date(today.getTime() + offset * BOOKING_PAGE_DAYS * DAY_MS);
  const from = offset === 0 ? now : start;
  return { start: from, end: new Date(start.getTime() + BOOKING_PAGE_DAYS * DAY_MS) };
}

/** Si hay otra semana dentro de la antelacion maxima que admite la pagina. */
export function hasNextWindow(offset: number, maxAdvanceDays: number): boolean {
  return (offset + 1) * BOOKING_PAGE_DAYS < maxAdvanceDays;
}

/** Huecos agrupados por dia local del visitante, en orden. */
export function slotsByDay(slots: readonly BookingSlot[]): { day: string; slots: BookingSlot[] }[] {
  const days = new Map<string, BookingSlot[]>();
  for (const slot of [...slots].sort((a, b) => a.start.localeCompare(b.start))) {
    const key = localDay(new Date(slot.start));
    days.set(key, [...(days.get(key) ?? []), slot]);
  }
  return [...days.entries()].map(([day, list]) => ({ day, slots: list }));
}

const EMAIL = /^[^\s@<>()",;:\\[\]]+@[^\s@<>()",;:\\[\]]+\.[^\s@<>()",;:\\[\]]+$/;

/** Correo del visitante con la misma forma que admite el servicio. */
export function validVisitorEmail(value: string): boolean {
  const v = value.trim();
  return v.length <= 254 && EMAIL.test(v);
}
