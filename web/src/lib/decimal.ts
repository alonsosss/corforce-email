import { getLocale } from '@/i18n';

// Importes, tasas y porcentajes llegan del API como texto decimal ("0.2512", "49.00") y se
// muestran sin pasar por float: solo se desplaza la coma y se cambia el separador.

const DECIMAL = /^(\d+)(?:\.(\d+))?$/;

function decimalSeparator(): string {
  const part = new Intl.NumberFormat(getLocale())
    .formatToParts(1.5)
    .find((p) => p.type === 'decimal');
  return part?.value ?? '.';
}

/** Cambia el punto decimal del API por el separador del idioma. */
export function formatDecimalText(value: string): string {
  return DECIMAL.test(value) ? value.replace('.', decimalSeparator()) : value;
}

/**
 * Fraccion decimal ("0.2512") a porcentaje en texto ("25.12"), desplazando dos cifras en
 * la cadena. null si el texto no es un decimal sin signo.
 */
export function fractionToPercentText(fraction: string): string | null {
  const match = DECIMAL.exec(fraction.trim());
  if (!match) return null;
  const [, whole = '', frac = ''] = match;
  const shifted = `${whole}${frac.padEnd(2, '0').slice(0, 2)}`.replace(/^0+(?=\d)/, '');
  const rest = frac.slice(2);
  return rest ? `${shifted}.${rest}` : shifted;
}

/** Tasa del API como porcentaje legible: "0.2512" -> "25,12 %". */
export function formatRate(fraction: string): string {
  const percent = fractionToPercentText(fraction);
  return percent === null ? fraction : `${formatDecimalText(percent)} %`;
}

/** Porcentaje que ya llega en texto ("85.50") como "85,50 %". */
export function formatPercentText(percent: string): string {
  return `${formatDecimalText(percent)} %`;
}

/**
 * Proporcion aproximada para dibujar una barra. Solo sirve para el ancho: el valor que se
 * lee en pantalla es siempre el texto del API.
 */
export function barRatio(percent: string | null): number {
  if (!percent) return 0;
  const n = Number(percent) / 100;
  return Number.isFinite(n) ? Math.min(Math.max(n, 0), 1) : 0;
}
