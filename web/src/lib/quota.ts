import { getLocale, t } from '@/i18n';

// Cuotas en la interfaz: se escriben en MiB o GiB y el API recibe bytes enteros
// (mail-directory los guarda en int64). 0 significa sin limite en todo el directorio.

export type QuotaUnit = 'MiB' | 'GiB';
export const QUOTA_UNITS: readonly QuotaUnit[] = ['MiB', 'GiB'];

const MIB = 1024 * 1024;
const GIB = 1024 * MIB;
const UNIT_BYTES: Record<QuotaUnit, number> = { MiB: MIB, GiB: GIB };
const AMOUNT = /^\d+(?:[.,]\d+)?$/;

export interface QuotaAmount {
  amount: string;
  unit: QuotaUnit;
}

/**
 * Cantidad escrita a bytes enteros. null si no es un numero no negativo o si el resultado
 * no cabe como entero exacto: ni NaN ni Infinity pueden llegar al API.
 */
export function quotaToBytes(amount: string, unit: QuotaUnit): number | null {
  const text = amount.trim();
  if (!AMOUNT.test(text)) return null;
  const value = Number(text.replace(',', '.'));
  if (!Number.isFinite(value)) return null;
  const bytes = Math.round(value * UNIT_BYTES[unit]);
  return Number.isSafeInteger(bytes) ? bytes : null;
}

/** Bytes a la unidad que los expresa sin decimales largos, para editarlos. */
export function bytesToQuota(bytes: number): QuotaAmount {
  if (!Number.isFinite(bytes) || bytes <= 0) return { amount: '0', unit: 'MiB' };
  if (bytes % GIB === 0) return { amount: String(bytes / GIB), unit: 'GiB' };
  return { amount: String(Math.round((bytes / MIB) * 100) / 100), unit: 'MiB' };
}

const SIZE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const;

/** Tamano legible en unidades binarias (1 KiB = 1024 B). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return t('common.dash');
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < SIZE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : value < 10 ? 2 : 1;
  const number = new Intl.NumberFormat(getLocale(), { maximumFractionDigits: digits }).format(
    value,
  );
  return `${number} ${SIZE_UNITS[unit] ?? ''}`.trim();
}

/** Cuota legible: 0 es sin limite. */
export function formatQuota(bytes: number): string {
  return bytes === 0 ? t('quota.unlimited') : formatBytes(bytes);
}

/** Proporcion usada entre 0 y 1; null si la cuota es ilimitada o los datos no son validos. */
export function usageRatio(used: number, limit: number): number | null {
  if (!Number.isFinite(used) || !Number.isFinite(limit) || limit <= 0 || used < 0) return null;
  return Math.min(used / limit, 1);
}
