import { useState } from 'react';
import {
  TEMPLATE_DESCRIPTION_MAX_LENGTH,
  TEMPLATE_KINDS,
  TEMPLATE_NAME_MAX_LENGTH,
  templatesApi,
  type CreateTemplateRequest,
  type Template,
  type TemplateKind,
} from '@/api/templates';
import { ERROR_CODES } from '@/api/errors';
import { useAction } from '@/hooks/useAction';
import { FormField, Input, Select, Textarea } from '@/design/components';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ContentFields } from './ContentFields';
import { contentFromDraft, emptyContent, type ContentDraft, type ContentErrors } from './content';

export interface TemplateCreateFormProps {
  onClose: () => void;
  onCreated: (template: Template) => void;
}

export function TemplateCreateForm({ onClose, onCreated }: TemplateCreateFormProps) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [kind, setKind] = useState<TemplateKind | ''>('');
  const [content, setContent] = useState<ContentDraft>(emptyContent());
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [contentErrors, setContentErrors] = useState<ContentErrors>({});

  const action = useAction(async (input: CreateTemplateRequest) => {
    const { data } = await templatesApi.create(input);
    onCreated(data);
  });

  const submit = async () => {
    const next = {
      name:
        validateField(name, rules.required, rules.maxLength(TEMPLATE_NAME_MAX_LENGTH)) ?? undefined,
      description:
        validateField(description, rules.maxLength(TEMPLATE_DESCRIPTION_MAX_LENGTH)) ?? undefined,
      kind: kind ? undefined : t('validation.required'),
    };
    const parsed = contentFromDraft(content);
    setErrors(next);
    setContentErrors(parsed.errors);
    if (Object.values(next).some(Boolean) || !parsed.content || !kind) return;
    await action.run({
      name: name.trim(),
      description: description.trim(),
      kind,
      ...parsed.content,
    });
  };

  return (
    <FormModal
      id="template-create-form"
      title={t('templates.form.createTitle')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      errorOverrides={{ [ERROR_CODES.CONFLICT]: 'templates.exists' }}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      <div className="cf-form__row">
        <FormField label={t('common.name')} htmlFor="template-name" required error={errors.name}>
          <Input
            id="template-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            invalid={Boolean(errors.name)}
          />
        </FormField>
        <FormField
          label={t('templates.column.kind')}
          htmlFor="template-kind"
          required
          error={errors.kind}
          hint={kind ? tEnum('templates.kindHint', kind) : undefined}
        >
          <Select
            id="template-kind"
            placeholder={t('common.select')}
            options={TEMPLATE_KINDS.map((k) => ({ value: k, label: tEnum('templates.kind', k) }))}
            value={kind}
            onChange={(e) => setKind(e.target.value as TemplateKind | '')}
            invalid={Boolean(errors.kind)}
          />
        </FormField>
      </div>
      <FormField
        label={t('common.description')}
        htmlFor="template-description"
        error={errors.description}
      >
        <Textarea
          id="template-description"
          rows={2}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <ContentFields
        idPrefix="template-create"
        value={content}
        onChange={setContent}
        errors={contentErrors}
      />
    </FormModal>
  );
}
