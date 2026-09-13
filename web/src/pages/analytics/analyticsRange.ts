// Rangos de fechas de los informes. El servicio cuenta los dias en la zona que devuelve
// en `timezone`; los atajos parten del ultimo dia que el propio servicio devolvio, asi que
// no suponen ninguna zona en el navegador. El rango maximo y el tope del ranking de
// dominios llegan en GET /analytics/meta.

const DAY_MS = 86_400_000;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Atajos de la barra de filtros, en dias: solo se ofrecen los que caben en el maximo. */
const RANGE_PRESETS = [7, 30, 90] as const;

export type RangeProblem = 'invalidDate' | 'reversed' | 'tooLong';

export function isDay(value: string): boolean {
  return DATE.test(value) && !Number.isNaN(Date.parse(`${value}T00:00:00Z`));
}

/** Suma dias a un AAAA-MM-DD sin pasar por la zona del navegador. */
export function shiftDay(day: string, delta: number): string {
  const base = Date.parse(`${day}T00:00:00Z`);
  return new Date(base + delta * DAY_MS).toISOString().slice(0, 10);
}

/** Los ultimos `days` dias que terminan en `to`, ambos incluidos. */
export function presetRange(days: number, to: string): { from: string; to: string } {
  return { from: shiftDay(to, -(days - 1)), to };
}

/** Atajos que no superan el rango maximo del servicio, mas su rango por defecto. */
export function availablePresets(maxDays: number, defaultDays: number): number[] {
  const all = new Set<number>([...RANGE_PRESETS, defaultDays]);
  return [...all].filter((days) => days >= 1 && days <= maxDays).sort((a, b) => a - b);
}

/** Dias del rango, ambos extremos incluidos. */
export function rangeDays(from: string, to: string): number {
  return Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / DAY_MS) + 1;
}

/** null si el rango vale; si no, la clave del problema. */
export function rangeProblem(from: string, to: string, maxDays?: number): RangeProblem | null {
  if ((from && !isDay(from)) || (to && !isDay(to))) return 'invalidDate';
  if (from && to && from > to) return 'reversed';
  if (from && to && maxDays !== undefined && rangeDays(from, to) > maxDays) return 'tooLong';
  return null;
}

/**
 * Tamano del ranking de dominios escrito a mano: vacio usa el del servicio. null si vale;
 * si no, el texto no es un entero entre 1 y el maximo que publica el servicio.
 */
export function parseDomainLimit(
  raw: string,
  maxLimit: number,
): { limit: number | undefined } | null {
  const text = raw.trim();
  if (!text) return { limit: undefined };
  if (!/^\d+$/.test(text)) return null;
  const value = Number(text);
  return Number.isSafeInteger(value) && value >= 1 && value <= maxLimit ? { limit: value } : null;
}
