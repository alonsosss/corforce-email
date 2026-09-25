import type { FieldType, VariableType } from '@/api/templates';
import { Button, Checkbox, Input, Select } from '@/design/components';
import { IconPlus, IconTrash } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { newFieldDraft, newVariableDraft, type FieldDraft, type VariableDraft } from './variables';

export interface VariablesEditorProps {
  drafts: VariableDraft[];
  onChange: (next: VariableDraft[]) => void;
  /** Tipos y topes que publica GET /templates/meta. */
  types: readonly VariableType[];
  fieldTypes: readonly FieldType[];
  maxVariables: number;
  maxListFields: number;
  errors?: Record<string, string>;
  countError?: string;
}

export function VariablesEditor({
  drafts,
  onChange,
  types,
  fieldTypes,
  maxVariables,
  maxListFields,
  errors,
  countError,
}: VariablesEditorProps) {
  const update = (key: string, patch: Partial<VariableDraft>) =>
    onChange(drafts.map((d) => (d.key === key ? { ...d, ...patch } : d)));
  const updateField = (draft: VariableDraft, fieldKey: string, patch: Partial<FieldDraft>) =>
    update(draft.key, {
      fields: draft.fields.map((f) => (f.key === fieldKey ? { ...f, ...patch } : f)),
    });

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
      {drafts.length === 0 ? (
        <span className="cf-text-sm cf-text-muted">{t('templates.variables.none')}</span>
      ) : null}
      {drafts.map((draft) => {
        const error = errors?.[draft.key];
        return (
          <div key={draft.key} className="cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
            <div className="cf-var-row">
              <Input
                aria-label={t('templates.variables.name')}
                className="cf-mono"
                placeholder={t('templates.variables.namePlaceholder')}
                value={draft.name}
                onChange={(e) => update(draft.key, { name: e.target.value })}
                invalid={Boolean(error)}
                spellCheck={false}
              />
              <Select
                aria-label={t('templates.variables.type')}
                options={types.map((type) => ({
                  value: type,
                  label: tEnum('templates.variableType', type),
                }))}
                value={draft.type}
                onChange={(e) => {
                  const type = e.target.value as VariableType;
                  update(draft.key, {
                    type,
                    defaultValue: '',
                    fields:
                      type === 'list' && draft.fields.length === 0
                        ? [newFieldDraft()]
                        : draft.fields,
                  });
                }}
              />
              <Checkbox
                label={t('templates.variables.required')}
                checked={draft.required}
                onChange={(e) =>
                  update(draft.key, {
                    required: e.target.checked,
                    defaultValue: e.target.checked ? '' : draft.defaultValue,
                  })
                }
              />
              {draft.type === 'list' ? (
                <span />
              ) : draft.type === 'boolean' ? (
                <Select
                  aria-label={t('templates.variables.default')}
                  placeholder={t('templates.variables.noDefault')}
                  options={[
                    { value: 'true', label: t('common.yes') },
                    { value: 'false', label: t('common.no') },
                  ]}
                  value={draft.defaultValue}
                  onChange={(e) => update(draft.key, { defaultValue: e.target.value })}
                  disabled={draft.required}
                />
              ) : (
                <Input
                  aria-label={t('templates.variables.default')}
                  type={draft.type === 'number' ? 'number' : 'text'}
                  placeholder={t('templates.variables.defaultPlaceholder')}
                  value={draft.defaultValue}
                  onChange={(e) => update(draft.key, { defaultValue: e.target.value })}
                  disabled={draft.required}
                />
              )}
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon={<IconTrash size={14} />}
                onClick={() => onChange(drafts.filter((d) => d.key !== draft.key))}
              >
                {t('templates.variables.remove')}
              </Button>
            </div>
            {draft.type === 'list' ? (
              <ListFieldsEditor
                draft={draft}
                fieldTypes={fieldTypes}
                maxFields={maxListFields}
                onUpdate={(fieldKey, patch) => updateField(draft, fieldKey, patch)}
                onChange={(fields) => update(draft.key, { fields })}
              />
            ) : null}
            {error ? (
              <span className="cf-field__error" role="alert">
                {error}
              </span>
            ) : null}
          </div>
        );
      })}
      {countError ? (
        <span className="cf-field__error" role="alert">
          {countError}
        </span>
      ) : null}
      <div>
        <Button
          size="sm"
          icon={<IconPlus size={14} />}
          onClick={() => onChange([...drafts, newVariableDraft()])}
          disabled={drafts.length >= maxVariables}
        >
          {t('templates.variables.add')}
        </Button>
      </div>
    </div>
  );
}

interface ListFieldsEditorProps {
  draft: VariableDraft;
  fieldTypes: readonly FieldType[];
  maxFields: number;
  onUpdate: (fieldKey: string, patch: Partial<FieldDraft>) => void;
  onChange: (fields: FieldDraft[]) => void;
}

/** Campos de cada elemento de una lista: nombre, tipo simple y si es requerido. */
function ListFieldsEditor({
  draft,
  fieldTypes,
  maxFields,
  onUpdate,
  onChange,
}: ListFieldsEditorProps) {
  return (
    <div className="cf-var-fields cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
      <span className="cf-form__section">{t('templates.variables.fields')}</span>
      <span className="cf-text-sm cf-text-muted">{t('templates.variables.fieldsHint')}</span>
      {draft.fields.map((field) => (
        <div key={field.key} className="cf-var-row">
          <Input
            aria-label={t('templates.variables.fieldName')}
            className="cf-mono"
            placeholder={t('templates.variables.fieldNamePlaceholder')}
            value={field.name}
            onChange={(e) => onUpdate(field.key, { name: e.target.value })}
            spellCheck={false}
          />
          <Select
            aria-label={t('templates.variables.type')}
            options={fieldTypes.map((type) => ({
              value: type,
              label: tEnum('templates.variableType', type),
            }))}
            value={field.type}
            onChange={(e) => onUpdate(field.key, { type: e.target.value as FieldType })}
          />
          <Checkbox
            label={t('templates.variables.required')}
            checked={field.required}
            onChange={(e) => onUpdate(field.key, { required: e.target.checked })}
          />
          <span />
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconTrash size={14} />}
            onClick={() => onChange(draft.fields.filter((f) => f.key !== field.key))}
          >
            {t('templates.variables.removeField')}
          </Button>
        </div>
      ))}
      <div>
        <Button
          size="sm"
          icon={<IconPlus size={14} />}
          onClick={() => onChange([...draft.fields, newFieldDraft()])}
          disabled={draft.fields.length >= maxFields}
        >
          {t('templates.variables.addField')}
        </Button>
      </div>
    </div>
  );
}
