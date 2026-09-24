import { Input, Select } from '@/design/components';
import { t } from '@/i18n';
import { namedSelectOptions, type NamedOption } from './workflowOptions';

/** Selector por nombre si el rol puede leer la coleccion; si no, el identificador a mano. */
export function IdPicker({
  id,
  options,
  value,
  onChange,
  emptyLabel,
  disabled,
  invalid,
  required,
}: {
  id: string;
  options: NamedOption[] | null;
  value: string;
  onChange: (value: string) => void;
  emptyLabel: string;
  disabled: boolean;
  invalid: boolean;
  required?: boolean;
}) {
  if (options) {
    return (
      <Select
        id={id}
        placeholder={emptyLabel}
        options={namedSelectOptions(options, value)}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        invalid={invalid}
        required={required}
      />
    );
  }
  return (
    <Input
      id={id}
      className="cf-mono"
      placeholder={t('automations.editor.idPlaceholder')}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      invalid={invalid}
      autoComplete="off"
      spellCheck={false}
    />
  );
}
