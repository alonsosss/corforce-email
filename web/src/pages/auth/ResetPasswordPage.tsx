import { useState, type FormEvent } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { identityApi } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import { Button, ErrorState, FormField, PasswordInput, Skeleton } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AuthLayout } from './AuthLayout';
import { PasswordRulesList } from '@/pages/shared/PasswordRulesList';

export default function ResetPasswordPage() {
  const [params] = useSearchParams();
  const token = params.get('token') ?? '';

  const rules = useQuery(async () => {
    if (!token) return null;
    return (await identityApi.resetPasswordPolicy(token)).data;
  }, [token]);

  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      setError(t('validation.passwordMismatch'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { data } = await identityApi.resetPassword(token, password);
      setDone(data.message);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title={t('auth.reset.title')} subtitle={t('auth.reset.description')}>
      {!token ? (
        <div className="cf-form">
          <div className="cf-form__error" role="alert">
            {t('auth.reset.missingToken')}
          </div>
          <Link className="cf-btn cf-btn--secondary cf-btn--block" to={paths.forgotPassword}>
            {t('auth.forgot.title')}
          </Link>
        </div>
      ) : rules.error ? (
        <div className="cf-form">
          <ErrorState error={rules.error} />
          <Link className="cf-btn cf-btn--secondary cf-btn--block" to={paths.forgotPassword}>
            {t('auth.forgot.title')}
          </Link>
        </div>
      ) : done ? (
        <div className="cf-form">
          <div className="cf-form__success" role="status">
            {done}
          </div>
          <Link className="cf-btn cf-btn--primary cf-btn--block" to={paths.login}>
            {t('auth.reset.goToLogin')}
          </Link>
        </div>
      ) : (
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          {rules.loading || !rules.data ? (
            <Skeleton lines={4} />
          ) : (
            <PasswordRulesList rules={rules.data} value={password} />
          )}
          <FormField label={t('auth.reset.newPassword')} htmlFor="reset-password" required>
            <PasswordInput
              id="reset-password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              autoFocus
            />
          </FormField>
          <FormField label={t('auth.reset.confirmPassword')} htmlFor="reset-confirm" required>
            <PasswordInput
              id="reset-confirm"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              required
            />
          </FormField>
          {error ? (
            <div className="cf-form__error" role="alert">
              {error}
            </div>
          ) : null}
          <Button type="submit" variant="primary" block loading={busy}>
            {t('auth.reset.submit')}
          </Button>
        </form>
      )}
    </AuthLayout>
  );
}
