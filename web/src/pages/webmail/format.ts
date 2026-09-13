import type { MailAddress } from '@/api/webmail';
import { getLocale, t } from '@/i18n';

/** Nombre si lo hay; si no, la direccion. */
export function addressLabel(address: MailAddress): string {
  return address.name.trim() || address.email;
}

/** Lista legible: "Nombre <direccion>" o la direccion sola. */
export function addressList(list: readonly MailAddress[] | null | undefined): string {
  return (list ?? [])
    .map((a) => (a.name.trim() ? `${a.name.trim()} <${a.email}>` : a.email))
    .join(', ');
}

function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

/** Fecha del listado: la hora si es de hoy; si no, el dia (con ano si no es el actual). */
export function formatMailDate(value: string | null, now: Date = new Date()): string {
  if (!value) return t('common.dash');
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return t('common.dash');
  const format = sameDay(date, now)
    ? new Intl.DateTimeFormat(getLocale(), { hour: '2-digit', minute: '2-digit' })
    : new Intl.DateTimeFormat(getLocale(), {
        day: '2-digit',
        month: '2-digit',
        year: date.getFullYear() === now.getFullYear() ? undefined : 'numeric',
      });
  return format.format(date);
}

// Controles C0 y C1 y marcas de direccion de texto (el mismo criterio que
// domain.SanitizeFilename del webmail): un U+202E dentro de un nombre de fichero invierte
// lo que sigue y hace pasar un ejecutable por un documento.
const HIDDEN_RANGES: readonly (readonly [number, number])[] = [
  [0x0000, 0x001f],
  [0x007f, 0x009f],
  [0x061c, 0x061c],
  [0x200e, 0x200f],
  [0x202a, 0x202e],
  [0x2066, 0x2069],
];

function isHidden(char: string): boolean {
  const code = char.codePointAt(0) ?? 0;
  return HIDDEN_RANGES.some(([from, to]) => code >= from && code <= to);
}

/** Nombre de un adjunto para mostrarlo, sin caracteres invisibles que disfracen la extension. */
export function displayFilename(name: string, fallback: string): string {
  const visible = Array.from(name)
    .filter((char) => !isHidden(char))
    .join('')
    .trim();
  return visible || fallback;
}

/** Entero positivo de un parametro de la URL; null si falta o no lo es. */
export function parsePositiveInt(raw: string | null): number | null {
  if (!raw || !/^\d{1,10}$/.test(raw)) return null;
  const value = Number(raw);
  return Number.isSafeInteger(value) && value > 0 ? value : null;
}
