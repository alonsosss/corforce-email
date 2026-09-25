import type { Ref } from 'react';
import type { TemplatesMeta } from '@/api/templates';
import { Button, FormField, Input, Textarea } from '@/design/components';
import { t } from '@/i18n';
import type { ContentErrors } from '../content';
import type { VariableDraft } from '../variables';
import { VariablesEditor } from '../VariablesEditor';

export interface DocumentDraft {
  subject: string;
  preheader: string;
  text: string;
  variables: VariableDraft[];
}

export interface DocumentPanelProps {
  value: DocumentDraft;
  onChange: (next: DocumentDraft) => void;
  errors: ContentErrors;
  meta: TemplatesMeta;
  preheaderRef: Ref<HTMLInputElement>;
  onGenerateText: () => void;
  readOnly: boolean;
}

/** Ajustes del correo que no son del lienzo: asunto, preheader, texto plano y variables. */
export function DocumentPanel({
  value,
  onChange,
  errors,
  meta,
  preheaderRef,
  onGenerateText,
  readOnly,
}: DocumentPanelProps) {
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <FormField
        label={t('templates.content.subject')}
        htmlFor="editor-subject"
        required
        error={errors.subject}
        hint={t('templates.content.subjectHint')}
      >
        <Input
          id="editor-subject"
          value={value.subject}
          readOnly={readOnly}
          onChange={(e) => onChange({ ...value, subject: e.target.value })}
          invalid={Boolean(errors.subject)}
        />
      </FormField>
      <FormField
        label={t('templates.editor.preheader')}
        htmlFor="editor-preheader"
        hint={t('templates.editor.preheaderHint')}
      >
        <Input
          id="editor-preheader"
          ref={preheaderRef}
          value={value.preheader}
          readOnly={readOnly}
          onChange={(e) => onChange({ ...value, preheader: e.target.value })}
        />
      </FormField>
      <FormField
        label={t('templates.content.text')}
        htmlFor="editor-text"
        hint={t('templates.content.textHint')}
      >
        <Textarea
          id="editor-text"
          rows={6}
          value={value.text}
          readOnly={readOnly}
          onChange={(e) => onChange({ ...value, text: e.target.value })}
        />
      </FormField>
      {!readOnly ? (
        <div>
          <Button size="sm" onClick={onGenerateText}>
            {t('templates.editor.generateText')}
          </Button>
        </div>
      ) : null}
      <div className="cf-form__section">{t('templates.variables.title')}</div>
      <span className="cf-text-sm cf-text-secondary">{t('templates.variables.hint')}</span>
      <VariablesEditor
        drafts={value.variables}
        onChange={(variables) => onChange({ ...value, variables })}
        types={meta.variable_types}
        fieldTypes={meta.field_types}
        maxListFields={meta.limits.max_list_fields}
        maxVariables={meta.limits.max_variables}
        errors={errors.variables}
        countError={errors.variablesCount}
      />
    </div>
  );
}
