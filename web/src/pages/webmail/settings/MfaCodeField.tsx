import { FormField, Input } from '@/design/components';
import { t } from '@/i18n';

export interface MfaCodeFieldProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  disabled?: boolean;
}

/**
 * Codigo de la verificacion en dos pasos para confirmar una accion: el de la aplicacion o uno de
 * recuperacion (quien perdio el movil tambien tiene que poder usarlo). Por eso no se limita a
 * cifras.
 */
export function MfaCodeField({ id, value, onChange, error, disabled }: MfaCodeFieldProps) {
  return (
    <FormField
      label={t('webmail.security.code')}
      htmlFor={id}
      error={error}
      hint={t('webmail.security.codeOrRecoveryHint')}
      required
    >
      <Input
        id={id}
        autoComplete="one-time-code"
        autoCapitalize="characters"
        spellCheck={false}
        value={value}
        invalid={Boolean(error)}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value.trim())}
      />
    </FormField>
  );
}
