import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { registerStepUpHandler, type StepUpGrant } from '@/api/client';
import { identityApi } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { ERROR_CODES } from '@/api/errors';
import { Button, FormField, Input, Modal, PasswordInput } from '@/design/components';
import { IconLock } from '@/design/icons';
import { t } from '@/i18n';

interface PendingRequest {
  resolve: (grant: StepUpGrant | null) => void;
}

/**
 * Re-autenticacion para acciones criticas. El cliente HTTP recibe 403 STEP_UP_REQUIRED,
 * llama al manejador registrado aqui, y este modal pide contrasena (y codigo MFA si lo
 * hay), obtiene el token en POST /auth/step-up y lo devuelve para reintentar con X-Step-Up.
 */
export function StepUpModal() {
  const [pending, setPending] = useState<PendingRequest | null>(null);
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    registerStepUpHandler(
      () =>
        new Promise<StepUpGrant | null>((resolve) => {
          setPassword('');
          setCode('');
          setError(null);
          setPending({ resolve });
        }),
    );
    return () => registerStepUpHandler(null);
  }, []);

  const finish = useCallback(
    (grant: StepUpGrant | null) => {
      pending?.resolve(grant);
      setPending(null);
    },
    [pending],
  );

  const cancel = useCallback(() => {
    if (!busy) finish(null);
  }, [busy, finish]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const { data } = await identityApi.stepUp(password, code);
      finish({ token: data.step_up_token, expiresIn: data.expires_in });
    } catch (err) {
      setError(errorMessage(err, { [ERROR_CODES.UNAUTHORIZED]: 'auth.stepUp.invalid' }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={pending !== null}
      title={t('auth.stepUp.title')}
      onClose={cancel}
      footer={
        <>
          <Button onClick={cancel} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="cf-step-up-form"
            loading={busy}
            icon={<IconLock size={16} />}
          >
            {t('auth.stepUp.submit')}
          </Button>
        </>
      }
    >
      <form id="cf-step-up-form" className="cf-form" onSubmit={(e) => void submit(e)}>
        <p className="cf-modal__message">{t('auth.stepUp.description')}</p>
        <FormField label={t('auth.stepUp.password')} htmlFor="step-up-password" required>
          <PasswordInput
            id="step-up-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </FormField>
        <FormField
          label={t('auth.stepUp.code')}
          htmlFor="step-up-code"
          hint={t('auth.stepUp.codeHint')}
        >
          <Input
            id="step-up-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
          />
        </FormField>
        {error ? (
          <div className="cf-form__error" role="alert">
            {error}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
