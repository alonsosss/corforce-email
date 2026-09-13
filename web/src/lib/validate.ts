import { t } from '@/i18n';

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const SLUG = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type FieldErrors<K extends string> = Partial<Record<K, string>>;

export const rules = {
  required: (value: string): string | null => (value.trim() ? null : t('validation.required')),
  email: (value: string): string | null =>
    EMAIL.test(value.trim()) ? null : t('validation.email'),
  minLength:
    (n: number) =>
    (value: string): string | null =>
      value.length >= n ? null : t('validation.minLength', { n }),
  maxLength:
    (n: number) =>
    (value: string): string | null =>
      value.length <= n ? null : t('validation.maxLength', { n }),
  slug: (value: string): string | null => (SLUG.test(value) ? null : t('validation.slug')),
  uuid: (value: string): string | null => (UUID.test(value.trim()) ? null : t('validation.uuid')),
  port: (value: string): string | null => {
    const n = Number(value);
    return Number.isInteger(n) && n >= 1 && n <= 65535 ? null : t('validation.port');
  },
  code6: (value: string): string | null => (/^\d{6}$/.test(value) ? null : t('validation.code6')),
  range:
    (min: number, max: number) =>
    (value: string): string | null => {
      const n = Number(value);
      return Number.isInteger(n) && n >= min && n <= max
        ? null
        : t('validation.range', { min, max });
    },
  nonNegativeInteger: (value: string): string | null => {
    const n = Number(value);
    return value.trim() !== '' && Number.isSafeInteger(n) && n >= 0
      ? null
      : t('validation.nonNegativeInteger');
  },
};

type Rule = (value: string) => string | null;

/** Aplica las reglas en orden y devuelve el primer error. */
export function validateField(value: string, ...checks: Rule[]): string | null {
  for (const check of checks) {
    const error = check(value);
    if (error) return error;
  }
  return null;
}

export function hasErrors<K extends string>(errors: FieldErrors<K>): boolean {
  return Object.values(errors).some((v) => Boolean(v));
}
