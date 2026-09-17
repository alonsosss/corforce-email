import { useState, type FormEvent } from 'react';
import { Link, Navigate, useLocation } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { Button, FormField, Input, PasswordInput } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AuthLayout } from './AuthLayout';

interface LocationState {
  from?: string;
}

export default function LoginPage() {
  const { status, mfaToken, login, completeMfa, cancelMfa } = useAuth();
  const location = useLocation();
  const from = (location.state as LocationState | null)?.from ?? paths.home;

  if (status === 'authenticated') return <Navigate to={from} replace />;
  if (mfaToken) return <MfaStep onSubmit={completeMfa} onCancel={cancelMfa} />;
  return <CredentialsStep onSubmit={login} />;
}

function CredentialsStep({ onSubmit }: { onSubmit: ReturnType<typeof useAuth>['login'] }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [tenantSlug, setTenantSlug] = useState('');
  // La empresa no se pide: el correo la resuelve. Solo hace falta indicarla cuando la misma
  // direccion existe en dos empresas, que es la excepcion, y entonces se pide a proposito.
  const [mostrarEmpresa, setMostrarEmpresa] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onSubmit({
        email: email.trim(),
        password,
        tenant_slug: tenantSlug.trim() || undefined,
      });
    } catch (err) {
      setError(errorMessage(err, { [ERROR_CODES.UNAUTHORIZED]: 'auth.login.invalidCredentials' }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title={t('auth.login.title')} subtitle={t('auth.login.subtitle')}>
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField label={t('common.email')} htmlFor="login-email" required>
          <Input
            id="login-email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
          />
        </FormField>
        <FormField label={t('common.password')} htmlFor="login-password" required>
          <PasswordInput
            id="login-password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </FormField>
        {mostrarEmpresa ? (
          <FormField
            label={t('auth.login.tenantSlug')}
            htmlFor="login-tenant"
            hint={t('auth.login.tenantSlugHint')}
          >
            <Input
              id="login-tenant"
              autoComplete="organization"
              value={tenantSlug}
              onChange={(e) => setTenantSlug(e.target.value)}
              autoFocus
            />
          </FormField>
        ) : null}
        {error ? (
          <div className="cf-form__error" role="alert">
            {error}
          </div>
        ) : null}
        <Button type="submit" variant="primary" block loading={busy}>
          {t('auth.login.submit')}
        </Button>
        <div className="cf-auth__links">
          <Link to={paths.forgotPassword}>{t('auth.login.forgot')}</Link>
          {mostrarEmpresa ? null : (
            <button type="button" onClick={() => setMostrarEmpresa(true)}>
              {t('auth.login.tenantToggle')}
            </button>
          )}
        </div>
      </form>
    </AuthLayout>
  );
}

function MfaStep({
  onSubmit,
  onCancel,
}: {
  onSubmit: (code: string) => Promise<void>;
  onCancel: () => void;
}) {
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onSubmit(code.trim());
    } catch (err) {
      setError(errorMessage(err, { [ERROR_CODES.UNAUTHORIZED]: 'auth.mfa.invalid' }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title={t('auth.mfa.title')} subtitle={t('auth.mfa.description')}>
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField label={t('auth.mfa.code')} htmlFor="mfa-code" required>
          <Input
            id="mfa-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            value={code}
            onChange={(e) => setCode(e.target.value)}
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
          {t('auth.mfa.submit')}
        </Button>
        <Button variant="ghost" block onClick={onCancel}>
          {t('auth.mfa.cancel')}
        </Button>
      </form>
    </AuthLayout>
  );
}
