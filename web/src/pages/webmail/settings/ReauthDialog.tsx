import { useState, type FormEvent } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { webmailApi, type Reauthentication } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Button, FormField, Modal, PasswordInput } from '@/design/components';
import { t } from '@/i18n';
import { filtersErrorMessage } from './filters';
import { MfaCodeField } from './MfaCodeField';

export interface ReauthDialogProps {
  /** Direcciones de fuera de la empresa que se anaden (details.addresses del 403). */
  addresses: readonly string[];
  /** Repite el guardado con la confirmacion; si falla, el dialogo sigue abierto con el error. */
  onConfirm: (reauth: Reauthentication) => Promise<void>;
  onCancel: () => void;
}

/**
 * Reenviar a una direccion de fuera de la empresa deja una copia del correo fuera que sobrevive
 * a un cambio de contrasena: el servicio exige volver a escribirla (y el codigo con la
 * verificacion en dos pasos).
 */
export function ReauthDialog({ addresses, onConfirm, onCancel }: ReauthDialogProps) {
  const security = useQuery((signal) => webmailApi.security(signal), []);
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [codeDemanded, setCodeDemanded] = useState(false);
  const [problems, setProblems] = useState<{ password?: string; code?: string }>({});
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const needsCode = codeDemanded || security.data?.mfa.enabled === true;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const found: { password?: string; code?: string } = {};
    if (!password) found.password = t('webmail.password.currentRequired');
    if (needsCode && !code) found.code = t('webmail.security.codeMissing');
    setProblems(found);
    if (found.password || found.code) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm({ current_password: password, code: needsCode ? code : undefined });
    } catch (err) {
      setBusy(false);
      const kind = errorCode(err);
      if (kind === ERROR_CODES.INVALID_CREDENTIALS) {
        setPassword('');
        setProblems({ password: t('webmail.password.currentWrong') });
      } else if (kind === ERROR_CODES.MFA_REQUIRED || kind === ERROR_CODES.INVALID_MFA_CODE) {
        setCodeDemanded(true);
        setCode('');
        setProblems({ code: errorMessage(err) });
      } else {
        setError(err);
      }
    }
  };

  return (
    <Modal
      open
      title={t('webmail.reauth.title')}
      onClose={() => {
        if (!busy) onCancel();
      }}
      footer={
        <>
          <Button onClick={onCancel} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="wm-reauth" variant="primary" loading={busy}>
            {t('webmail.reauth.submit')}
          </Button>
        </>
      }
    >
      <form id="wm-reauth" className="cf-form" noValidate onSubmit={(e) => void submit(e)}>
        <p className="cf-modal__message">{t('webmail.reauth.description')}</p>
        {addresses.length ? (
          <ul className="cf-wm-reauth__list" aria-label={t('webmail.reauth.addresses')}>
            {addresses.map((address) => (
              <li key={address}>{address}</li>
            ))}
          </ul>
        ) : null}
        <FormField
          label={t('webmail.password.current')}
          htmlFor="wm-reauth-password"
          error={problems.password}
          required
        >
          <PasswordInput
            id="wm-reauth-password"
            autoComplete="current-password"
            value={password}
            invalid={Boolean(problems.password)}
            disabled={busy}
            onChange={(e) => {
              setPassword(e.target.value);
              setProblems((p) => ({ ...p, password: undefined }));
            }}
          />
        </FormField>
        {needsCode ? (
          <MfaCodeField
            id="wm-reauth-code"
            value={code}
            error={problems.code}
            disabled={busy}
            onChange={(value) => {
              setCode(value);
              setProblems((p) => ({ ...p, code: undefined }));
            }}
          />
        ) : null}
        {error ? (
          <div className="cf-form__error" role="alert">
            {filtersErrorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
