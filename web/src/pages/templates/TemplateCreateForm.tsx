import { useState } from 'react';
import {
  templatesApi,
  templatesMeta,
  type CreateTemplateRequest,
  type Template,
  type TemplateKind,
  type TemplatesMeta,
} from '@/api/templates';
import { ERROR_CODES } from '@/api/errors';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { FormField, Input, Select, Textarea } from '@/design/components';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { ContentFields } from './ContentFields';
import { contentFromDraft, emptyContent, type ContentDraft, type ContentErrors } from './content';

export interface TemplateCreateFormProps {
  onClose: () => void;
  onCreated: (template: Template) => void;
}

export function TemplateCreateForm(props: TemplateCreateFormProps) {
  const meta = useResource(templatesMeta);
  return (
    <ResourceGate
      resource={meta}
      modal={{ title: t('templates.form.createTitle'), onClose: props.onClose }}
    >
      {(data) => <CreateForm {...props} meta={data} />}
    </ResourceGate>
  );
}

function CreateForm({
  onClose,
  onCreated,
  meta,
}: TemplateCreateFormProps & { meta: TemplatesMeta }) {
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
        validateField(name, rules.required, rules.maxLength(meta.limits.max_name_length)) ??
        undefined,
      description:
        validateField(description, rules.maxLength(meta.limits.max_description_length)) ??
        undefined,
      kind: kind ? undefined : t('validation.required'),
    };
    const parsed = contentFromDraft(content, meta);
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
            options={meta.kinds.map((k) => ({ value: k, label: tEnum('templates.kind', k) }))}
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
        meta={meta}
      />
    </FormModal>
  );
}
