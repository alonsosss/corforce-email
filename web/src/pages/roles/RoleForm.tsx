import { useState, type FormEvent } from 'react';
import type { Role, RoleInput } from '@/api/access';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { Button, FormField, Input, Modal } from '@/design/components';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';

export interface RoleFormProps {
  open: boolean;
  role?: Role;
  onClose: () => void;
  onSubmit: (input: RoleInput) => Promise<void>;
}

const FORM_ID = 'role-form';

export function RoleForm({ open, role, onClose, onSubmit }: RoleFormProps) {
  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [nameError, setNameError] = useState<string | null>(null);
  const action = useAction(() => onSubmit({ name: name.trim(), description: description.trim() }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const error = validateField(name, rules.required, rules.maxLength(100));
    setNameError(error);
    if (error) return;
    await action.run();
  };

  return (
    <Modal
      open={open}
      title={role ? t('roles.form.editTitle') : t('roles.form.createTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={action.busy}>
            {role ? t('common.save') : t('common.create')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField label={t('common.name')} htmlFor="role-name" required error={nameError}>
          <Input
            id="role-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            invalid={Boolean(nameError)}
            autoComplete="off"
          />
        </FormField>
        <FormField label={t('common.description')} htmlFor="role-description">
          <textarea
            id="role-description"
            className="cf-textarea"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </FormField>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error, { [ERROR_CODES.CONFLICT]: 'roles.exists' })}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
