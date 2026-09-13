import { FormField, Input } from '@/design/components';
import { t } from '@/i18n';

export interface ObjectFieldProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  disabled?: boolean;
  label?: string;
}

/** Objeto de una politica de seguridad: un buzon (usuario@dominio) o un dominio entero. */
export function ObjectField({ id, value, onChange, error, disabled, label }: ObjectFieldProps) {
  return (
    <FormField
      label={label ?? t('security.object')}
      htmlFor={id}
      required={!disabled}
      error={error}
      hint={t('security.objectHint')}
    >
      <Input
        id={id}
        className="cf-mono"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        invalid={Boolean(error)}
        autoComplete="off"
        spellCheck={false}
      />
    </FormField>
  );
}
