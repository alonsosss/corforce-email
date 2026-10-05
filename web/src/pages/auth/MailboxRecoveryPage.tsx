import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { webmailApi, type MailboxRecoveryResult } from '@/api/webmail';
import { Alert, Button, FormField, Input, PasswordInput } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import {
  LOW_RECOVERY_CODES,
  normalizeRecoveryCode,
  TOTP_DIGITS,
} from '@/pages/webmail/settings/mfa';
import { AuthLayout } from './AuthLayout';

/**
 * Recuperacion de la contrasena de un buzon sin sesion. Exige a la vez el codigo de la aplicacion
 * de autenticacion y uno de recuperacion: con uno solo, el segundo factor pasaria a ser el unico.
 * La respuesta a un rechazo no dice si fallo el buzon o alguno de los codigos.
 */
export default function MailboxRecoveryPage() {
  const [email, setEmail] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [recoveryCode, setRecoveryCode] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<MailboxRecoveryResult | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      setError(t('validation.passwordMismatch'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      setDone(
        await webmailApi.recoverPassword({
          username: email.trim(),
          totp_code: totpCode.replace(/\s+/g, ''),
          recovery_code: normalizeRecoveryCode(recoveryCode),
          new_password: password,
        }),
      );
    } catch (err) {
      // Los codigos ya escritos no sirven para otro intento si alguno se gasto: se vacian.
      setTotpCode('');
      setRecoveryCode('');
      setError(
        errorMessage(err, {
          [ERROR_CODES.PASSWORD_RECOVERY_REJECTED]: 'auth.mailboxRecovery.rejected',
          [ERROR_CODES.RATE_LIMITED]: 'auth.mailboxRecovery.rateLimited',
        }),
      );
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <AuthLayout title={t('auth.mailboxRecovery.title')}>
        <div className="cf-form">
          <div className="cf-form__success" role="status">
            {t('auth.mailboxRecovery.done')}
          </div>
          <Alert tone={done.recovery_remaining < LOW_RECOVERY_CODES ? 'warning' : 'info'}>
            {t('auth.mailboxRecovery.remaining', { count: done.recovery_remaining })}
            {done.recovery_remaining < LOW_RECOVERY_CODES ? (
              <> {t('auth.mailboxRecovery.lowCodes')}</>
            ) : null}
          </Alert>
          {done.app_passwords_revoked > 0 ? (
            <Alert tone="warning">
              {t('auth.mailboxRecovery.appPasswordsRevoked', {
                count: done.app_passwords_revoked,
              })}
            </Alert>
          ) : null}
          <Link className="cf-btn cf-btn--primary cf-btn--block" to={paths.login}>
            {t('auth.reset.goToLogin')}
          </Link>
        </div>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout
      title={t('auth.mailboxRecovery.title')}
      subtitle={t('auth.mailboxRecovery.description')}
      footer={t('auth.mailboxRecovery.noAdmin')}
    >
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField label={t('common.email')} htmlFor="recovery-email" required>
          <Input
            id="recovery-email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
          />
        </FormField>
        <FormField label={t('auth.mailboxRecovery.totpCode')} htmlFor="recovery-totp" required>
          <Input
            id="recovery-totp"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={TOTP_DIGITS}
            value={totpCode}
            onChange={(e) => setTotpCode(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label={t('auth.mailboxRecovery.recoveryCode')}
          htmlFor="recovery-code"
          hint={t('auth.mailboxRecovery.recoveryCodeHint')}
          required
        >
          <Input
            id="recovery-code"
            autoComplete="off"
            autoCapitalize="characters"
            spellCheck={false}
            value={recoveryCode}
            onChange={(e) => setRecoveryCode(e.target.value)}
            required
          />
        </FormField>
        <FormField label={t('auth.reset.newPassword')} htmlFor="recovery-password" required>
          <PasswordInput
            id="recovery-password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </FormField>
        <FormField label={t('auth.reset.confirmPassword')} htmlFor="recovery-confirm" required>
          <PasswordInput
            id="recovery-confirm"
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
          {t('auth.mailboxRecovery.submit')}
        </Button>
        <div className="cf-auth__links">
          <Link to={paths.login}>{t('auth.forgot.backToLogin')}</Link>
        </div>
      </form>
    </AuthLayout>
  );
}
