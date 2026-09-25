import type { FieldType, TemplateVariable } from '@/api/templates';
import { Checkbox, FormField, Input, Textarea } from '@/design/components';
import { t, tEnum } from '@/i18n';
import { listExample, type ValueDraft } from './variables';

export interface VariableValuesFormProps {
  idPrefix: string;
  variables: readonly TemplateVariable[];
  values: ValueDraft;
  onChange: (next: ValueDraft) => void;
  errors?: Record<string, string>;
}

const INPUT_TYPE: Record<Exclude<FieldType, 'boolean'>, string> = {
  string: 'text',
  number: 'number',
  url: 'url',
  email: 'email',
  image: 'url',
};

/** Un campo por variable declarada, con el control que corresponde a su tipo. */
export function VariableValuesForm({
  idPrefix,
  variables,
  values,
  onChange,
  errors,
}: VariableValuesFormProps) {
  if (variables.length === 0) {
    return <span className="cf-text-sm cf-text-muted">{t('templates.preview.noVariables')}</span>;
  }
  return (
    <div className="cf-form__row">
      {variables.map((v) => {
        const id = `${idPrefix}-${v.name}`;
        const error = errors?.[v.name];
        if (v.type === 'boolean') {
          return (
            <div key={v.name} className="cf-field">
              <Checkbox
                id={id}
                label={v.name}
                checked={values[v.name] === true}
                onChange={(e) => onChange({ ...values, [v.name]: e.target.checked })}
              />
              <span className="cf-field__hint">{tEnum('templates.variableType', v.type)}</span>
            </div>
          );
        }
        const raw = values[v.name];
        if (v.type === 'list') {
          return (
            <FormField
              key={v.name}
              label={v.name}
              htmlFor={id}
              required={v.required}
              error={error}
              hint={t('templates.preview.listHint', { example: listExample(v.fields ?? []) })}
            >
              <Textarea
                id={id}
                mono
                rows={4}
                value={typeof raw === 'string' ? raw : ''}
                onChange={(e) => onChange({ ...values, [v.name]: e.target.value })}
                invalid={Boolean(error)}
              />
            </FormField>
          );
        }
        return (
          <FormField
            key={v.name}
            label={v.name}
            htmlFor={id}
            required={v.required}
            error={error}
            hint={tEnum('templates.variableType', v.type)}
          >
            <Input
              id={id}
              type={INPUT_TYPE[v.type]}
              step={v.type === 'number' ? 'any' : undefined}
              value={typeof raw === 'string' ? raw : ''}
              placeholder={
                v.default !== undefined
                  ? t('templates.preview.defaultPlaceholder', { value: String(v.default) })
                  : undefined
              }
              onChange={(e) => onChange({ ...values, [v.name]: e.target.value })}
              invalid={Boolean(error)}
              autoComplete="off"
            />
          </FormField>
        );
      })}
    </div>
  );
}
