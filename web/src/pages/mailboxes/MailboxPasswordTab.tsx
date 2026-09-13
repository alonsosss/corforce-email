import { useState, type FormEvent } from 'react';
import {
  directoryMeta,
  mailDirectoryApi,
  type DirectoryMeta,
  type Mailbox,
} from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { Button, Card, FormField, PasswordInput, useToast } from '@/design/components';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';
import { ResourceGate } from '@/pages/shared/ResourceGate';

type Field = 'password' | 'confirm';

export function MailboxPasswordTab({ mailbox }: { mailbox: Mailbox }) {
  const meta = useResource(directoryMeta);
  return (
    <ResourceGate resource={meta}>
      {(rules) => <PasswordForm mailbox={mailbox} meta={rules} />}
    </ResourceGate>
  );
}

function PasswordForm({ mailbox, meta }: { mailbox: Mailbox; meta: DirectoryMeta }) {
  const toast = useToast();
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  const action = useAction(async () => {
    await mailDirectoryApi.setMailboxPassword(mailbox.id, password);
    setPassword('');
    setConfirm('');
    toast.success(t('mailboxes.password.changed'));
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next: FieldErrors<Field> = {
      password:
        validateField(
          password,
          rules.required,
          rules.minLength(meta.mailbox.password_min_length),
          rules.maxLength(meta.mailbox.password_max_length),
        ) ?? undefined,
      confirm: confirm === password ? undefined : t('validation.passwordMismatch'),
    };
    setErrors(next);
    if (hasErrors(next)) return;
    await action.run();
  };

  return (
    <Card
      title={t('mailboxes.password.title')}
      description={t('mailboxes.password.description', { address: mailbox.username })}
    >
      <form
        className="cf-form"
        onSubmit={(e) => void submit(e)}
        noValidate
        style={{ maxWidth: 480 }}
      >
        <FormField
          label={t('mailboxes.password.new')}
          htmlFor="mailbox-new-password"
          required
          error={errors.password}
          hint={t('mailboxes.form.passwordHint', { n: meta.mailbox.password_min_length })}
        >
          <PasswordInput
            id="mailbox-new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            invalid={Boolean(errors.password)}
            autoComplete="new-password"
          />
        </FormField>
        <FormField
          label={t('mailboxes.form.confirmPassword')}
          htmlFor="mailbox-new-confirm"
          required
          error={errors.confirm}
        >
          <PasswordInput
            id="mailbox-new-confirm"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            invalid={Boolean(errors.confirm)}
            autoComplete="new-password"
          />
        </FormField>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error)}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={action.busy}>
            {t('mailboxes.password.submit')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
