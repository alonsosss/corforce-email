import { useState, type FormEvent } from 'react';
import { Link, Navigate, useLocation } from 'react-router-dom';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import {
  Alert,
  Button,
  ErrorState,
  FormField,
  Input,
  LoadingBlock,
  PasswordInput,
} from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AuthLayout } from '@/pages/auth/AuthLayout';
import { useWebmailStore } from '@/webmail/store';
import { normalizeRecoveryCode, TOTP_DIGITS } from './settings/mfa';

interface LocationState {
  from?: string;
}

/** Solo se vuelve a una pantalla del propio webmail tras entrar. */
function returnPath(state: unknown): string {
  const from = (state as LocationState | null)?.from;
  if (!from || !from.startsWith(paths.webmail) || from.startsWith(paths.webmailLogin)) {
    return paths.webmail;
  }
  return from;
}

export function WebmailLoginPage() {
  const status = useWebmailStore((s) => s.status);
  const checkError = useWebmailStore((s) => s.checkError);
  const expired = useWebmailStore((s) => s.expired);
  const passwordChanged = useWebmailStore((s) => s.passwordChanged);
  const mfaExpired = useWebmailStore((s) => s.mfaExpired);
  const location = useLocation();

  if (status === 'authenticated') return <Navigate to={returnPath(location.state)} replace />;
  if (status === 'checking') {
    return (
      <div className="cf-fullscreen">
        <LoadingBlock label={t('webmail.checking')} />
      </div>
    );
  }

  return (
    <AuthLayout
      title={t('webmail.login.title')}
      subtitle={t('webmail.login.subtitle')}
      footer={<Link to={paths.login}>{t('webmail.login.platformLink')}</Link>}
    >
      {status === 'unavailable' ? (
        <ErrorState
          error={checkError}
          title={t('webmail.unavailable')}
          onRetry={() => void useWebmailStore.getState().check()}
        />
      ) : status === 'mfa' ? (
        <MfaStep />
      ) : (
        <LoginForm expired={expired} passwordChanged={passwordChanged} mfaExpired={mfaExpired} />
      )}
    </AuthLayout>
  );
}

function LoginForm({
  expired,
  passwordChanged,
  mfaExpired,
}: {
  expired: boolean;
  passwordChanged: boolean;
  mfaExpired: boolean;
}) {
  const login = useWebmailStore((s) => s.login);
  const acknowledgeExpired = useWebmailStore((s) => s.acknowledgeExpired);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!username.trim() || !password) {
      setError(t('webmail.login.missing'));
      return;
    }
    setBusy(true);
    setError(null);
    acknowledgeExpired();
    try {
      await login(username.trim(), password);
    } catch (err) {
      // El servicio responde igual a un buzon inexistente y a una contrasena mala; la
      // interfaz tampoco los distingue.
      setPassword('');
      setError(
        errorMessage(err, {
          [ERROR_CODES.INVALID_CREDENTIALS]: 'webmail.login.invalid',
          [ERROR_CODES.RATE_LIMITED]: 'webmail.login.rateLimited',
        }),
      );
      setBusy(false);
    }
  };

  return (
    <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
      {passwordChanged ? (
        <Alert tone="success">{t('webmail.login.passwordChanged')}</Alert>
      ) : mfaExpired ? (
        <Alert tone="warning">{t('webmail.login.mfa.expired')}</Alert>
      ) : expired ? (
        <Alert tone="warning">{t('webmail.login.expired')}</Alert>
      ) : null}
      <FormField
        label={t('webmail.login.username')}
        htmlFor="wm-username"
        required
        hint={t('webmail.login.usernameHint')}
      >
        <Input
          id="wm-username"
          type="email"
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          required
          autoFocus
        />
      </FormField>
      <FormField label={t('common.password')} htmlFor="wm-password" required>
        <PasswordInput
          id="wm-password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
      </FormField>
      {error ? (
        <div className="cf-form__error" role="alert">
          {error}
        </div>
      ) : null}
      <Button type="submit" variant="primary" block loading={busy}>
        {t('webmail.login.submit')}
      </Button>
    </form>
  );
}

/**
 * Segundo paso con la verificacion en dos pasos activa: el codigo de la aplicacion
 * autenticadora o, si no se tiene a mano, uno de los codigos de recuperacion.
 */
function MfaStep() {
  const verifyMfa = useWebmailStore((s) => s.verifyMfa);
  const cancelMfa = useWebmailStore((s) => s.cancelMfa);
  const username = useWebmailStore((s) => s.mfaUsername);
  const [recovery, setRecovery] = useState(false);
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const value = recovery ? normalizeRecoveryCode(code) : code.replace(/\s+/g, '');
    if (!value) {
      setError(t(recovery ? 'webmail.login.mfa.recoveryMissing' : 'webmail.login.mfa.missing'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await verifyMfa(value);
    } catch (err) {
      setCode('');
      setError(
        errorMessage(err, {
          [ERROR_CODES.INVALID_MFA_CODE]: 'webmail.login.mfa.invalid',
          [ERROR_CODES.RATE_LIMITED]: 'webmail.login.rateLimited',
        }),
      );
      setBusy(false);
    }
  };

  const switchMode = () => {
    setRecovery((v) => !v);
    setCode('');
    setError(null);
  };

  return (
    <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
      <p className="cf-text-secondary">
        {t(recovery ? 'webmail.login.mfa.recoveryDescription' : 'webmail.login.mfa.description', {
          mailbox: username ?? '',
          n: TOTP_DIGITS,
        })}
      </p>
      {recovery ? (
        <FormField label={t('webmail.login.mfa.recoveryCode')} htmlFor="wm-mfa-code" required>
          <Input
            key="recovery"
            id="wm-mfa-code"
            className="cf-mono"
            autoComplete="off"
            autoCapitalize="characters"
            spellCheck={false}
            placeholder={t('webmail.login.mfa.recoveryPlaceholder')}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
            autoFocus
          />
        </FormField>
      ) : (
        <FormField label={t('webmail.login.mfa.code')} htmlFor="wm-mfa-code" required>
          <Input
            key="totp"
            id="wm-mfa-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="[0-9]*"
            maxLength={TOTP_DIGITS}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
            autoFocus
          />
        </FormField>
      )}
      {error ? (
        <div className="cf-form__error" role="alert">
          {error}
        </div>
      ) : null}
      <Button type="submit" variant="primary" block loading={busy}>
        {t('webmail.login.mfa.submit')}
      </Button>
      <Button variant="ghost" block onClick={switchMode} disabled={busy}>
        {t(recovery ? 'webmail.login.mfa.useTotp' : 'webmail.login.mfa.useRecovery')}
      </Button>
      <Button variant="ghost" block onClick={cancelMfa} disabled={busy}>
        {t('webmail.login.mfa.cancel')}
      </Button>
    </form>
  );
}
