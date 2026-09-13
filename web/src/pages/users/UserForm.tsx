import { useState, type FormEvent } from 'react';
import type { CreateUserRequest, UpdateUserRequest, User } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { ERROR_CODES } from '@/api/errors';
import { useAction } from '@/hooks/useAction';
import { Button, FormField, Input, Modal, PasswordInput } from '@/design/components';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';

type Field = 'first_name' | 'last_name' | 'email' | 'password';

export type UserFormProps =
  | {
      mode: 'create';
      open: boolean;
      onClose: () => void;
      onSubmit: (input: CreateUserRequest) => Promise<void>;
    }
  | {
      mode: 'edit';
      open: boolean;
      user: User;
      onClose: () => void;
      onSubmit: (input: UpdateUserRequest) => Promise<void>;
    };

const FORM_ID = 'user-form';

export function UserForm(props: UserFormProps) {
  const { mode, open, onClose } = props;
  const initial = mode === 'edit' ? props.user : null;
  const [firstName, setFirstName] = useState(initial?.first_name ?? '');
  const [lastName, setLastName] = useState(initial?.last_name ?? '');
  const [email, setEmail] = useState(initial?.email ?? '');
  const [password, setPassword] = useState('');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const submitAction = useAction(async () => {
    if (props.mode === 'create') {
      await props.onSubmit({
        email: email.trim(),
        password,
        first_name: firstName.trim(),
        last_name: lastName.trim(),
      });
    } else {
      await props.onSubmit({ first_name: firstName.trim(), last_name: lastName.trim() });
    }
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next: FieldErrors<Field> = {
      first_name: validateField(firstName, rules.required, rules.maxLength(100)) ?? undefined,
      last_name: validateField(lastName, rules.required, rules.maxLength(100)) ?? undefined,
    };
    if (mode === 'create') {
      next.email = validateField(email, rules.required, rules.email) ?? undefined;
      next.password =
        validateField(password, rules.required, rules.minLength(8), rules.maxLength(128)) ??
        undefined;
    }
    setErrors(next);
    if (hasErrors(next)) return;
    await submitAction.run();
  };

  return (
    <Modal
      open={open}
      title={mode === 'create' ? t('users.form.createTitle') : t('users.form.editTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={submitAction.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={submitAction.busy}>
            {mode === 'create' ? t('common.create') : t('common.save')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <div className="cf-form__row">
          <FormField
            label={t('users.form.firstName')}
            htmlFor="user-first"
            required
            error={errors.first_name}
          >
            <Input
              id="user-first"
              value={firstName}
              onChange={(e) => setFirstName(e.target.value)}
              invalid={Boolean(errors.first_name)}
              autoComplete="off"
            />
          </FormField>
          <FormField
            label={t('users.form.lastName')}
            htmlFor="user-last"
            required
            error={errors.last_name}
          >
            <Input
              id="user-last"
              value={lastName}
              onChange={(e) => setLastName(e.target.value)}
              invalid={Boolean(errors.last_name)}
              autoComplete="off"
            />
          </FormField>
        </div>
        <FormField
          label={t('users.form.email')}
          htmlFor="user-email"
          required={mode === 'create'}
          error={errors.email}
        >
          <Input
            id="user-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            invalid={Boolean(errors.email)}
            disabled={mode === 'edit'}
            autoComplete="off"
          />
        </FormField>
        {mode === 'create' ? (
          <FormField
            label={t('users.form.password')}
            htmlFor="user-password"
            required
            error={errors.password}
            hint={t('users.form.passwordHint')}
          >
            <PasswordInput
              id="user-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              invalid={Boolean(errors.password)}
              autoComplete="new-password"
            />
          </FormField>
        ) : null}
        {submitAction.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(submitAction.error, { [ERROR_CODES.CONFLICT]: 'users.exists' })}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
