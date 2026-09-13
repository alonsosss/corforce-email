import { useState, type FormEvent } from 'react';
import { organizationApi, type CreateTenantRequest } from '@/api/organization';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, FormField, Input, Modal, PasswordInput, Select } from '@/design/components';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';

export interface OrganizationFormProps {
  open: boolean;
  onClose: () => void;
  onSubmit: (input: CreateTenantRequest) => Promise<void>;
}

type Field =
  'slug' | 'name' | 'admin_email' | 'admin_password' | 'admin_first_name' | 'admin_last_name';

const FORM_ID = 'organization-form';

export function OrganizationForm({ open, onClose, onSubmit }: OrganizationFormProps) {
  const cells = useQuery(async () => (await organizationApi.listCells()).data, []);
  const [slug, setSlug] = useState('');
  const [name, setName] = useState('');
  const [cellCode, setCellCode] = useState('');
  const [adminEmail, setAdminEmail] = useState('');
  const [adminPassword, setAdminPassword] = useState('');
  const [adminFirst, setAdminFirst] = useState('');
  const [adminLast, setAdminLast] = useState('');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const action = useAction(() =>
    onSubmit({
      slug: slug.trim(),
      name: name.trim(),
      cell_code: cellCode || undefined,
      admin_email: adminEmail.trim(),
      admin_password: adminPassword,
      admin_first_name: adminFirst.trim() || undefined,
      admin_last_name: adminLast.trim() || undefined,
    }),
  );

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next: FieldErrors<Field> = {
      slug: validateField(slug, rules.required, rules.slug, rules.maxLength(63)) ?? undefined,
      name: validateField(name, rules.required, rules.maxLength(255)) ?? undefined,
      admin_email:
        validateField(adminEmail, rules.required, rules.email, rules.maxLength(255)) ?? undefined,
      admin_password:
        validateField(adminPassword, rules.required, rules.minLength(8), rules.maxLength(128)) ??
        undefined,
      admin_first_name: validateField(adminFirst, rules.maxLength(100)) ?? undefined,
      admin_last_name: validateField(adminLast, rules.maxLength(100)) ?? undefined,
    };
    setErrors(next);
    if (hasErrors(next)) return;
    await action.run();
  };

  // Solo las celdas activas admiten empresas nuevas (domain.ErrCellNotAssignable).
  const cellOptions = (cells.data ?? [])
    .filter((c) => c.status === 'active')
    .map((c) => ({ value: c.code, label: `${c.code} (${c.region})` }));

  return (
    <Modal
      open={open}
      title={t('orgs.form.createTitle')}
      onClose={onClose}
      size="lg"
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={action.busy}>
            {t('common.create')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <div className="cf-form__row">
          <FormField
            label={t('orgs.form.slug')}
            htmlFor="org-slug"
            required
            error={errors.slug}
            hint={t('orgs.form.slugHint')}
          >
            <Input
              id="org-slug"
              className="cf-mono"
              value={slug}
              onChange={(e) => setSlug(e.target.value.toLowerCase())}
              invalid={Boolean(errors.slug)}
              autoComplete="off"
            />
          </FormField>
          <FormField label={t('orgs.form.name')} htmlFor="org-name" required error={errors.name}>
            <Input
              id="org-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              invalid={Boolean(errors.name)}
            />
          </FormField>
        </div>
        <FormField label={t('orgs.form.cell')} htmlFor="org-cell">
          <Select
            id="org-cell"
            placeholder={t('orgs.form.cellDefault')}
            options={cellOptions}
            value={cellCode}
            onChange={(e) => setCellCode(e.target.value)}
            disabled={cells.loading}
          />
        </FormField>
        <div className="cf-form__section">{t('orgs.form.adminSection')}</div>
        <div className="cf-form__row">
          <FormField
            label={t('orgs.form.adminFirstName')}
            htmlFor="org-admin-first"
            error={errors.admin_first_name}
          >
            <Input
              id="org-admin-first"
              value={adminFirst}
              onChange={(e) => setAdminFirst(e.target.value)}
            />
          </FormField>
          <FormField
            label={t('orgs.form.adminLastName')}
            htmlFor="org-admin-last"
            error={errors.admin_last_name}
          >
            <Input
              id="org-admin-last"
              value={adminLast}
              onChange={(e) => setAdminLast(e.target.value)}
            />
          </FormField>
        </div>
        <div className="cf-form__row">
          <FormField
            label={t('orgs.form.adminEmail')}
            htmlFor="org-admin-email"
            required
            error={errors.admin_email}
          >
            <Input
              id="org-admin-email"
              type="email"
              value={adminEmail}
              onChange={(e) => setAdminEmail(e.target.value)}
              invalid={Boolean(errors.admin_email)}
              autoComplete="off"
            />
          </FormField>
          <FormField
            label={t('orgs.form.adminPassword')}
            htmlFor="org-admin-password"
            required
            error={errors.admin_password}
          >
            <PasswordInput
              id="org-admin-password"
              value={adminPassword}
              onChange={(e) => setAdminPassword(e.target.value)}
              invalid={Boolean(errors.admin_password)}
              autoComplete="new-password"
            />
          </FormField>
        </div>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error, { [ERROR_CODES.CONFLICT]: 'orgs.exists' })}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
