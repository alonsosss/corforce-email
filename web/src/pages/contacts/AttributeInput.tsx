import type { AttributeDefinition } from '@/api/contacts';
import { Input, Select } from '@/design/components';
import { t } from '@/i18n';

/** Control del tipo declarado del atributo. El valor es siempre texto (ver attributeValue.ts). */
export function AttributeInput({
  id,
  definition,
  value,
  onChange,
  invalid,
}: {
  id: string;
  definition: AttributeDefinition;
  value: string;
  onChange: (next: string) => void;
  invalid?: boolean;
}) {
  if (definition.type === 'boolean') {
    return (
      <Select
        id={id}
        placeholder={t('contacts.attributes.noValue')}
        options={[
          { value: 'true', label: t('common.yes') },
          { value: 'false', label: t('common.no') },
        ]}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        invalid={invalid}
      />
    );
  }
  return (
    <Input
      id={id}
      type={definition.type === 'number' ? 'number' : definition.type === 'date' ? 'date' : 'text'}
      step={definition.type === 'number' ? 'any' : undefined}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      invalid={invalid}
      autoComplete="off"
    />
  );
}

export function attributeLabel(definition: AttributeDefinition): string {
  return definition.label || definition.key;
}
