import { useState, type FormEvent } from 'react';
import { identityApi, type User } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, FormField, Modal, PasswordInput, Skeleton } from '@/design/components';
import { PasswordRulesList } from '@/pages/shared/PasswordRulesList';
import { fullName } from '@/lib/format';
import { t } from '@/i18n';

export interface ResetPasswordDialogProps {
  open: boolean;
  user: User;
  onClose: () => void;
  onDone: () => void;
}

const FORM_ID = 'admin-reset-password';

/** Reinicio de contrasena por un administrador. El servidor exige step-up. */
export function ResetPasswordDialog({ open, user, onClose, onDone }: ResetPasswordDialogProps) {
  const policy = useQuery(async () => (await identityApi.passwordPolicy()).data, []);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [mismatch, setMismatch] = useState(false);
  const reset = useAction(() => identityApi.adminResetPassword(user.id, password));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const bad = password !== confirm;
    setMismatch(bad);
    if (bad) return;
    if (await reset.run()) onDone();
  };

  return (
    <Modal
      open={open}
      title={t('users.resetPassword')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={reset.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={FORM_ID} variant="primary" loading={reset.busy}>
            {t('users.resetPassword')}
          </Button>
        </>
      }
    >
      <form id={FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <p className="cf-modal__message">
          {t('users.resetPasswordDescription', {
            name: fullName(user.first_name, user.last_name, user.email),
          })}
        </p>
        {policy.loading || !policy.data ? (
          <Skeleton lines={3} />
        ) : (
          <PasswordRulesList rules={policy.data} value={password} />
        )}
        <FormField label={t('auth.reset.newPassword')} htmlFor="admin-reset-new" required>
          <PasswordInput
            id="admin-reset-new"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label={t('auth.reset.confirmPassword')}
          htmlFor="admin-reset-confirm"
          required
          error={mismatch ? t('validation.passwordMismatch') : null}
        >
          <PasswordInput
            id="admin-reset-confirm"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            invalid={mismatch}
            required
          />
        </FormField>
        {reset.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(reset.error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
