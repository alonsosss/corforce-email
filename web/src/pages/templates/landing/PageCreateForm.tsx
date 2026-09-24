import { useState } from 'react';
import { ERROR_CODES } from '@/api/errors';
import { pagesApi, pagesMeta, type LandingPage, type PagesMeta } from '@/api/pages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { Checkbox, FormField, Input } from '@/design/components';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { publicUrlFor, slugError, slugFromName } from './slug';

export interface PageCreateFormProps {
  onClose: () => void;
  onCreated: (page: LandingPage) => void;
}

export function PageCreateForm(props: PageCreateFormProps) {
  const meta = useResource(pagesMeta);
  return (
    <ResourceGate
      resource={meta}
      modal={{ title: t('templates.pages.form.createTitle'), onClose: props.onClose }}
    >
      {(data) => <CreateForm {...props} meta={data} />}
    </ResourceGate>
  );
}

function CreateForm({ onClose, onCreated, meta }: PageCreateFormProps & { meta: PagesMeta }) {
  const { can } = useAccess();
  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  // Mientras no se toque a mano, el slug sigue al nombre.
  const [slugEdited, setSlugEdited] = useState(false);
  const [noindex, setNoindex] = useState(true);
  const [errors, setErrors] = useState<{ name?: string; slug?: string }>({});

  const action = useAction(async () => {
    const detail = await pagesApi.create({ name: name.trim(), slug, noindex });
    onCreated(detail.page);
  });

  const submit = async () => {
    const next = {
      name: validateField(name, rules.required, rules.maxLength(meta.max_name_length)) ?? undefined,
      slug: slugError(slug, meta) ?? undefined,
    };
    setErrors(next);
    if (next.name || next.slug) return;
    await action.run();
  };

  return (
    <FormModal
      id="page-create-form"
      title={t('templates.pages.form.createTitle')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      errorOverrides={{ [ERROR_CODES.CONFLICT]: 'templates.pages.exists' }}
      onClose={onClose}
      onSubmit={submit}
      submitDisabled={!can(...PERMISSIONS.landingPages.create)}
    >
      <FormField label={t('common.name')} htmlFor="page-name" required error={errors.name}>
        <Input
          id="page-name"
          value={name}
          maxLength={meta.max_name_length}
          onChange={(e) => {
            setName(e.target.value);
            if (!slugEdited) setSlug(slugFromName(e.target.value, meta.max_slug_length));
          }}
          invalid={Boolean(errors.name)}
        />
      </FormField>
      <SlugField
        id="page-slug"
        value={slug}
        meta={meta}
        error={errors.slug}
        onChange={(value) => {
          setSlug(value);
          setSlugEdited(true);
        }}
      />
      <NoindexField checked={noindex} onChange={setNoindex} />
    </FormModal>
  );
}

export function SlugField({
  id,
  value,
  meta,
  error,
  onChange,
}: {
  id: string;
  value: string;
  meta: Pick<PagesMeta, 'public_prefix' | 'max_slug_length'>;
  error?: string;
  onChange: (value: string) => void;
}) {
  return (
    <FormField
      label={t('templates.pages.slug')}
      htmlFor={id}
      required
      error={error}
      hint={t('templates.pages.slugHint', { url: publicUrlFor(meta.public_prefix, value) })}
    >
      <Input
        id={id}
        value={value}
        spellCheck={false}
        autoCapitalize="off"
        maxLength={meta.max_slug_length}
        onChange={(e) => onChange(e.target.value.toLowerCase())}
        invalid={Boolean(error)}
      />
    </FormField>
  );
}

export function NoindexField({
  checked,
  onChange,
}: {
  checked: boolean;
  onChange: (value: boolean) => void;
}) {
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
      <Checkbox
        label={t('templates.pages.noindex')}
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="cf-text-sm cf-text-secondary">{t('templates.pages.noindexHint')}</span>
    </div>
  );
}
