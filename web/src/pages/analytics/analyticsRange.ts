// Rangos de fechas de los informes. El servicio cuenta los dias en la zona que devuelve
// en `timezone`; los atajos parten del ultimo dia que el propio servicio devolvio, asi que
// no suponen ninguna zona en el navegador.

const DAY_MS = 86_400_000;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Atajos de la barra de filtros, en dias. */
export const RANGE_PRESETS = [7, 30, 90] as const;

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

/** null si el rango vale; si no, la clave del problema. */
export function rangeProblem(from: string, to: string): 'invalidDate' | 'reversed' | null {
  if ((from && !isDay(from)) || (to && !isDay(to))) return 'invalidDate';
  if (from && to && from > to) return 'reversed';
  return null;
}
