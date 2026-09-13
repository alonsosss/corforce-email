import { RATE_LIMIT_UNITS, type RateLimitUnit } from '@/api/mailSecurity';

// Limite de tasa en el formato que lee ratelimit.lua de Rspamd: "N / 1h" (espejo de
// ValidateRateLimitValue en mail-security).
const VALUE = /^(\d+) \/ 1([smhd])$/;
const AMOUNT = /^\d+$/;

export interface RateLimitParts {
  amount: string;
  unit: RateLimitUnit;
}

function isUnit(value: string): value is RateLimitUnit {
  return (RATE_LIMIT_UNITS as readonly string[]).includes(value);
}

export function parseRateLimit(value: string): RateLimitParts | null {
  const match = VALUE.exec(value.trim());
  if (!match) return null;
  const [, amount = '', unit = ''] = match;
  return isUnit(unit) ? { amount, unit } : null;
}

/** Compone el valor; null si la cantidad no es un entero no negativo representable. */
export function formatRateLimit(amount: string, unit: RateLimitUnit): string | null {
  const text = amount.trim();
  if (!AMOUNT.test(text)) return null;
  const n = Number(text);
  return Number.isSafeInteger(n) ? `${n} / 1${unit}` : null;
}
