import { TEMPLATE_MAX_VARIABLES, VARIABLE_TYPES, type VariableType } from '@/api/templates';
import { Button, Checkbox, Input, Select } from '@/design/components';
import { IconPlus, IconTrash } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { newVariableDraft, type VariableDraft } from './variables';

export interface VariablesEditorProps {
  drafts: VariableDraft[];
  onChange: (next: VariableDraft[]) => void;
  errors?: Record<string, string>;
  countError?: string;
}

export function VariablesEditor({ drafts, onChange, errors, countError }: VariablesEditorProps) {
  const update = (key: string, patch: Partial<VariableDraft>) =>
    onChange(drafts.map((d) => (d.key === key ? { ...d, ...patch } : d)));

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
                options={VARIABLE_TYPES.map((type) => ({
                  value: type,
                  label: tEnum('templates.variableType', type),
                }))}
                value={draft.type}
                onChange={(e) =>
                  update(draft.key, { type: e.target.value as VariableType, defaultValue: '' })
                }
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
              {draft.type === 'boolean' ? (
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
          disabled={drafts.length >= TEMPLATE_MAX_VARIABLES}
        >
          {t('templates.variables.add')}
        </Button>
      </div>
    </div>
  );
}
