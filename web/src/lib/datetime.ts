const pad = (n: number): string => String(n).padStart(2, '0');

/** Fecha RFC 3339 del API al formato de un input datetime-local, en hora local. */
export function toDatetimeLocal(value: string | null | undefined): string {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Cierto si la fecha ya paso. */
export function isPast(value: string | null | undefined, now: Date = new Date()): boolean {
  if (!value) return false;
  const d = new Date(value);
  return !Number.isNaN(d.getTime()) && d.getTime() < now.getTime();
}

/** Zonas IANA que conoce el navegador, para sugerir sin copiar ninguna lista. */
export function browserTimezones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  return intl.supportedValuesOf?.('timeZone') ?? [];
}
