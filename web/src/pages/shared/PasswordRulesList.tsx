import type { PasswordRules } from '@/api/identity';
import { IconCheck, IconInfo } from '@/design/icons';
import { checkPasswordRules } from '@/lib/password';
import { t } from '@/i18n';

export interface PasswordRulesListProps {
  rules: PasswordRules;
  value: string;
}

/** Reglas de contrasena de la empresa, marcando las que el valor escrito ya cumple. */
export function PasswordRulesList({ rules, value }: PasswordRulesListProps) {
  const checks = checkPasswordRules(rules, value);
  return (
    <div>
      <div className="cf-field__label">{t('password.rules.title')}</div>
      <ul className="cf-rules">
        {checks.map((check) => (
          <li key={check.key} className={check.ok ? 'cf-rules__ok' : undefined}>
            {check.ok === null ? <IconInfo size={14} /> : <IconCheck size={14} />}
            {check.label}
          </li>
        ))}
      </ul>
    </div>
  );
}
