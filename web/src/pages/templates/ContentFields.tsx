import type { TemplatesMeta } from '@/api/templates';
import { FormField, HtmlPreviewFrame, Input, Textarea } from '@/design/components';
import { t } from '@/i18n';
import type { ContentDraft, ContentErrors } from './content';
import { VariablesEditor } from './VariablesEditor';

export interface ContentFieldsProps {
  idPrefix: string;
  value: ContentDraft;
  onChange: (next: ContentDraft) => void;
  errors: ContentErrors;
  meta: TemplatesMeta;
}

export function ContentFields({ idPrefix, value, onChange, errors, meta }: ContentFieldsProps) {
  return (
    <>
      <FormField
        label={t('templates.content.subject')}
        htmlFor={`${idPrefix}-subject`}
        required
        error={errors.subject}
        hint={t('templates.content.subjectHint')}
      >
        <Input
          id={`${idPrefix}-subject`}
          value={value.subject}
          onChange={(e) => onChange({ ...value, subject: e.target.value })}
          invalid={Boolean(errors.subject)}
        />
      </FormField>
      <div className="cf-split">
        <FormField
          label={t('templates.content.html')}
          htmlFor={`${idPrefix}-html`}
          required
          error={errors.html}
          hint={t('templates.content.htmlHint')}
        >
          <Textarea
            id={`${idPrefix}-html`}
            mono
            rows={16}
            value={value.html}
            onChange={(e) => onChange({ ...value, html: e.target.value })}
            invalid={Boolean(errors.html)}
          />
        </FormField>
        <div className="cf-field">
          <span className="cf-field__label">{t('templates.content.sourcePreview')}</span>
          <HtmlPreviewFrame
            html={value.html}
            title={t('templates.content.sourcePreview')}
            height={320}
          />
        </div>
      </div>
      <FormField
        label={t('templates.content.text')}
        htmlFor={`${idPrefix}-text`}
        hint={t('templates.content.textHint')}
      >
        <Textarea
          id={`${idPrefix}-text`}
          rows={6}
          value={value.text}
          onChange={(e) => onChange({ ...value, text: e.target.value })}
        />
      </FormField>
      <div className="cf-form__section">{t('templates.variables.title')}</div>
      <span className="cf-text-sm cf-text-secondary">{t('templates.variables.hint')}</span>
      {meta.reserved_variables.length ? (
        <span className="cf-text-sm cf-text-secondary">
          {t('templates.variables.reservedHint')}{' '}
          <span className="cf-inline-list" style={{ display: 'inline-flex' }}>
            {meta.reserved_variables.map((v) => (
              <code key={v.name} className="cf-mono">
                {v.name}
              </code>
            ))}
          </span>
        </span>
      ) : null}
      <VariablesEditor
        drafts={value.variables}
        onChange={(variables) => onChange({ ...value, variables })}
        types={meta.variable_types}
        maxVariables={meta.limits.max_variables}
        errors={errors.variables}
        countError={errors.variablesCount}
      />
    </>
  );
}
