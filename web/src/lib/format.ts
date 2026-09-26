import { getLocale, t } from '@/i18n';

const dateTimeFormat = () =>
  new Intl.DateTimeFormat(getLocale(), {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });

const dateFormat = () =>
  new Intl.DateTimeFormat(getLocale(), { year: 'numeric', month: '2-digit', day: '2-digit' });

const timestampFormat = () =>
  new Intl.DateTimeFormat(getLocale(), {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });

function parse(value: string | null | undefined): Date | null {
  if (!value) return null;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function formatDateTime(value: string | null | undefined): string {
  const d = parse(value);
  return d ? dateTimeFormat().format(d) : t('common.dash');
}

export function formatDate(value: string | null | undefined): string {
  const d = parse(value);
  return d ? dateFormat().format(d) : t('common.dash');
}

/** Instante con segundos y milisegundos, para lineas de registro. */
export function formatTimestamp(value: string | null | undefined): string {
  const d = parse(value);
  if (!d) return t('common.dash');
  return `${timestampFormat().format(d)}.${String(d.getMilliseconds()).padStart(3, '0')}`;
}

/**
 * Mayúscula solo en la primera letra, como pide la ortografía en un título de fecha
 * ("septiembre de 2026" pasa a "Septiembre de 2026"; CSS capitalize pondría "De").
 */
export function capitalizeFirst(text: string, locale = getLocale()): string {
  const first = text.codePointAt(0);
  if (first === undefined) return text;
  const head = String.fromCodePoint(first);
  return head.toLocaleUpperCase(locale) + text.slice(head.length);
}

export function fullName(first: string, last: string, fallback = ''): string {
  const name = `${first} ${last}`.trim();
  return name || fallback;
}

/** Fecha local (input type=datetime-local) a RFC 3339 en UTC, como espera el backend. */
export function localToRfc3339(value: string): string | undefined {
  if (!value) return undefined;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** Resume un agente de usuario a navegador y sistema, sin librerias. */
export function summarizeUserAgent(ua: string): string {
  if (!ua) return t('common.dash');
  const browser =
    /Edg\/[\d.]+/.exec(ua)?.[0] ??
    /Firefox\/[\d.]+/.exec(ua)?.[0] ??
    /Chrome\/[\d.]+/.exec(ua)?.[0] ??
    /Safari\/[\d.]+/.exec(ua)?.[0] ??
    null;
  const os = /\(([^)]+)\)/.exec(ua)?.[1]?.split(';')[0]?.trim() ?? null;
  const parts = [browser?.replace('/', ' '), os].filter((p): p is string => Boolean(p));
  return parts.length ? parts.join(' - ') : ua.slice(0, 60);
}

/** Hasta dos iniciales de un nombre o una direccion, para el avatar. */
export function initialsOf(label: string): string {
  return label
    .split(/[\s@.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((word) => Array.from(word)[0] ?? '')
    .join('')
    .toUpperCase();
}
