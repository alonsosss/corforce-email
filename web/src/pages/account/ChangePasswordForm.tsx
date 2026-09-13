import { useState, type FormEvent } from 'react';
import { identityApi } from '@/api/identity';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAuth } from '@/auth/useAuth';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, FormField, PasswordInput, Skeleton, useToast } from '@/design/components';
import { PasswordRulesList } from '@/pages/shared/PasswordRulesList';
import { t } from '@/i18n';

export function ChangePasswordForm() {
  const { endSession } = useAuth();
  const toast = useToast();
  const policy = useQuery(async () => (await identityApi.passwordPolicy()).data, []);

  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [mismatch, setMismatch] = useState(false);

  const change = useAction(() => identityApi.changePassword(current, next));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const bad = next !== confirm;
    setMismatch(bad);
    if (bad) return;
    if (await change.run()) {
      // El servidor cierra TODAS las sesiones del usuario, incluida esta; se termina en
      // el cliente sin llamar a logout, que solo produciria un 401.
      toast.success(t('account.password.changed'));
      endSession();
    }
  };

  return (
    <Card title={t('account.tab.password')}>
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        {policy.loading || !policy.data ? (
          <Skeleton lines={4} />
        ) : (
          <PasswordRulesList rules={policy.data} value={next} />
        )}
        <FormField label={t('account.password.current')} htmlFor="pw-current" required>
          <PasswordInput
            id="pw-current"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
          />
        </FormField>
        <div className="cf-form__row">
          <FormField label={t('account.password.new')} htmlFor="pw-new" required>
            <PasswordInput
              id="pw-new"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              required
            />
          </FormField>
          <FormField
            label={t('account.password.confirm')}
            htmlFor="pw-confirm"
            required
            error={mismatch ? t('validation.passwordMismatch') : null}
          >
            <PasswordInput
              id="pw-confirm"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              invalid={mismatch}
              required
            />
          </FormField>
        </div>
        {change.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(change.error, {
              [ERROR_CODES.UNAUTHORIZED]: 'account.password.wrongCurrent',
            })}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={change.busy}>
            {t('account.password.submit')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
