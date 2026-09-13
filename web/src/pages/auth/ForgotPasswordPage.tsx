import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { identityApi } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { Button, FormField, Input } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AuthLayout } from './AuthLayout';

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // La respuesta es identica exista o no el correo (anti-enumeracion); su mensaje es
      // la voz del servidor y se muestra tal cual.
      const { data } = await identityApi.forgotPassword(email.trim());
      setSent(data.message);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title={t('auth.forgot.title')} subtitle={t('auth.forgot.description')}>
      {sent ? (
        <div className="cf-form">
          <div className="cf-form__success" role="status">
            {sent}
          </div>
          <Link className="cf-btn cf-btn--secondary cf-btn--block" to={paths.login}>
            {t('auth.forgot.backToLogin')}
          </Link>
        </div>
      ) : (
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <FormField label={t('common.email')} htmlFor="forgot-email" required>
            <Input
              id="forgot-email"
              type="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoFocus
            />
          </FormField>
          {error ? (
            <div className="cf-form__error" role="alert">
              {error}
            </div>
          ) : null}
          <Button type="submit" variant="primary" block loading={busy}>
            {t('auth.forgot.submit')}
          </Button>
          <div className="cf-auth__links">
            <Link to={paths.login}>{t('auth.forgot.backToLogin')}</Link>
          </div>
        </form>
      )}
    </AuthLayout>
  );
}
