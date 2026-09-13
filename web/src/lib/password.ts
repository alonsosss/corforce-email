import type { PasswordRules } from '@/api/identity';
import { t } from '@/i18n';

export interface RuleCheck {
  key: string;
  label: string;
  /** null cuando la regla no se puede comprobar en el cliente (filtraciones). */
  ok: boolean | null;
}

const SPECIAL = /[^A-Za-z0-9]/;

/** Evalua las reglas del backend sobre un valor: solo para orientar, el servidor decide. */
export function checkPasswordRules(rules: PasswordRules, value: string): RuleCheck[] {
  const checks: RuleCheck[] = [
    {
      key: 'min_length',
      label: t('password.rules.minLength', { n: rules.min_length }),
      ok: value.length >= rules.min_length,
    },
  ];
  if (rules.require_uppercase) {
    checks.push({ key: 'upper', label: t('password.rules.uppercase'), ok: /[A-Z]/.test(value) });
  }
  if (rules.require_lowercase) {
    checks.push({ key: 'lower', label: t('password.rules.lowercase'), ok: /[a-z]/.test(value) });
  }
  if (rules.require_digit) {
    checks.push({ key: 'digit', label: t('password.rules.digit'), ok: /\d/.test(value) });
  }
  if (rules.require_special) {
    checks.push({ key: 'special', label: t('password.rules.special'), ok: SPECIAL.test(value) });
  }
  if (rules.breach_check) {
    checks.push({ key: 'breach', label: t('password.rules.breachCheck'), ok: null });
  }
  return checks;
}
