import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ERROR_CODES, errorCode, errorDetail } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { webmailApi } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Alert, Button, Card, FormField, PasswordInput } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { useWebmailStore } from '@/webmail/store';
import { MfaCodeField } from './MfaCodeField';

type Field = 'current' | 'next' | 'repeat' | 'code';

/**
 * Cambio de contrasena del buzon. El servicio comprueba la actual (y, con la verificacion en
 * dos pasos, un codigo) y, al cambiarla, revoca todas las sesiones del buzon, tambien esta: se
 * vuelve al inicio de sesion con un aviso.
 */
export function PasswordSettings() {
  const navigate = useNavigate();
  const endSession = useWebmailStore((s) => s.endAfterPasswordChange);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [repeat, setRepeat] = useState('');
  const [code, setCode] = useState('');
  // Si el estado no se pudo leer, el servicio lo dice con MFA_REQUIRED y se pide entonces.
  const security = useQuery((signal) => webmailApi.security(signal), []);
  const [codeDemanded, setCodeDemanded] = useState(false);
  const needsCode = codeDemanded || security.data?.mfa.enabled === true;
  const [problems, setProblems] = useState<Partial<Record<Field, string>>>({});
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    const found: Partial<Record<Field, string>> = {};
    if (!current) found.current = t('webmail.password.currentRequired');
    if (!next) found.next = t('webmail.password.newRequired');
    else if (next === current) found.next = t('webmail.password.sameAsCurrent');
    if (next && repeat !== next) found.repeat = t('webmail.password.mismatch');
    if (needsCode && !code) found.code = t('webmail.security.codeMissing');
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      await webmailApi.changePassword(current, next, needsCode ? code : undefined);
      endSession();
      navigate(paths.webmailLogin, { replace: true });
    } catch (err) {
      setBusy(false);
      const errCode = errorCode(err);
      if (errCode === ERROR_CODES.INVALID_CREDENTIALS) {
        setProblems({ current: t('webmail.password.currentWrong') });
        setCurrent('');
      } else if (errCode === ERROR_CODES.MFA_REQUIRED || errCode === ERROR_CODES.INVALID_MFA_CODE) {
        setCodeDemanded(true);
        setCode('');
        setProblems({ code: errorMessage(err) });
      } else if (
        errCode === ERROR_CODES.PASSWORD_POLICY ||
        errCode === ERROR_CODES.PASSWORD_REUSED ||
        errCode === ERROR_CODES.PASSWORD_BREACHED ||
        (errCode === ERROR_CODES.VALIDATION_ERROR && errorDetail(err, 'field') === 'new_password')
      ) {
        setProblems({ next: errorMessage(err) });
      } else {
        setError(err);
      }
    }
  };

  const field = (
    id: Field,
    label: string,
    value: string,
    set: (v: string) => void,
    auto: string,
  ) => (
    <FormField label={label} htmlFor={`wm-password-${id}`} error={problems[id]} required>
      <PasswordInput
        id={`wm-password-${id}`}
        autoComplete={auto}
        value={value}
        invalid={Boolean(problems[id])}
        disabled={busy}
        onChange={(e) => {
          set(e.target.value);
          setProblems((p) => ({ ...p, [id]: undefined }));
        }}
      />
    </FormField>
  );

  return (
    <Card title={t('webmail.password.title')} description={t('webmail.password.description')}>
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {field('current', t('webmail.password.current'), current, setCurrent, 'current-password')}
        {field('next', t('webmail.password.new'), next, setNext, 'new-password')}
        {field('repeat', t('webmail.password.repeat'), repeat, setRepeat, 'new-password')}
        {needsCode ? (
          <MfaCodeField
            id="wm-password-code"
            value={code}
            error={problems.code}
            disabled={busy}
            onChange={(value) => {
              setCode(value);
              setProblems((p) => ({ ...p, code: undefined }));
            }}
          />
        ) : null}
        <Alert tone="info">{t('webmail.password.signOutNote')}</Alert>
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={busy}>
            {t('webmail.password.submit')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
