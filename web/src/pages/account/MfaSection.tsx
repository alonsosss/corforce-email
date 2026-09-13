import { useState, type FormEvent } from 'react';
import { identityApi, type MfaSetupResponse } from '@/api/identity';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAuth } from '@/auth/useAuth';
import { useAction } from '@/hooks/useAction';
import {
  Badge,
  Button,
  Card,
  FormField,
  Input,
  Modal,
  PasswordInput,
  Skeleton,
  useToast,
} from '@/design/components';
import { QrCode } from '@/pages/shared/QrCode';
import { t } from '@/i18n';

export function MfaSection() {
  const { user, reloadUser, endSession } = useAuth();
  const toast = useToast();
  const [setup, setSetup] = useState<MfaSetupResponse | null>(null);
  const [code, setCode] = useState('');
  const [disableOpen, setDisableOpen] = useState(false);

  const start = useAction(async () => {
    const { data } = await identityApi.mfaSetup();
    setSetup(data);
    setCode('');
  });

  const activate = useAction(async () => {
    if (!setup) return;
    await identityApi.mfaActivate(setup.secret, code.trim());
  });

  const submitActivate = async (e: FormEvent) => {
    e.preventDefault();
    if (await activate.run()) {
      toast.success(t('account.mfa.activated'));
      setSetup(null);
      await reloadUser().catch(() => undefined);
    }
  };

  if (!user) {
    return (
      <Card>
        <Skeleton lines={3} />
      </Card>
    );
  }

  return (
    <div className="cf-stack">
      <Card
        title={t('account.tab.mfa')}
        actions={
          user.mfa_enabled ? (
            <Button variant="danger" onClick={() => setDisableOpen(true)}>
              {t('account.mfa.disable')}
            </Button>
          ) : (
            <Button variant="primary" loading={start.busy} onClick={() => void start.run()}>
              {t('account.mfa.enable')}
            </Button>
          )
        }
      >
        <div className="cf-inline">
          {user.mfa_enabled ? (
            <Badge tone="success">{t('common.enabled')}</Badge>
          ) : (
            <Badge>{t('common.disabled')}</Badge>
          )}
          <span className="cf-text-secondary">
            {user.mfa_enabled ? t('account.mfa.statusEnabled') : t('account.mfa.statusDisabled')}
          </span>
        </div>
        {start.error ? (
          <div className="cf-form__error" role="alert" style={{ marginTop: 'var(--cf-space-3)' }}>
            {errorMessage(start.error)}
          </div>
        ) : null}
      </Card>

      {setup ? (
        <Card title={t('account.mfa.setupTitle')} description={t('account.mfa.setupDescription')}>
          <form className="cf-form" onSubmit={(e) => void submitActivate(e)} noValidate>
            <div
              style={{
                display: 'flex',
                gap: 'var(--cf-space-5)',
                flexWrap: 'wrap',
                alignItems: 'flex-start',
              }}
            >
              <QrCode value={setup.provisioning_uri} label={t('account.mfa.qrAlt')} />
              <div className="cf-stack" style={{ gap: 'var(--cf-space-3)', flex: '1 1 240px' }}>
                <FormField label={t('account.mfa.secret')} htmlFor="mfa-secret">
                  <Input id="mfa-secret" className="cf-mono" value={setup.secret} readOnly />
                </FormField>
                <FormField label={t('account.mfa.code')} htmlFor="mfa-activate-code" required>
                  <Input
                    id="mfa-activate-code"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    maxLength={6}
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    required
                  />
                </FormField>
              </div>
            </div>
            {activate.error ? (
              <div className="cf-form__error" role="alert">
                {errorMessage(activate.error, {
                  [ERROR_CODES.UNAUTHORIZED]: 'account.mfa.invalid',
                })}
              </div>
            ) : null}
            <div className="cf-form__actions">
              <Button onClick={() => setSetup(null)}>{t('common.cancel')}</Button>
              <Button type="submit" variant="primary" loading={activate.busy}>
                {t('account.mfa.activate')}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}

      <DisableMfaDialog
        open={disableOpen}
        onClose={() => setDisableOpen(false)}
        onDisabled={() => {
          toast.success(t('account.mfa.disabledDone'));
          // Desactivar MFA revoca todas las sesiones del usuario en el servidor.
          endSession();
        }}
      />
    </div>
  );
}

function DisableMfaDialog({
  open,
  onClose,
  onDisabled,
}: {
  open: boolean;
  onClose: () => void;
  onDisabled: () => void;
}) {
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const disable = useAction(() => identityApi.mfaDisable(password, code.trim()));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await disable.run()) onDisabled();
  };

  return (
    <Modal
      open={open}
      title={t('account.mfa.disableTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={disable.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="mfa-disable-form" variant="danger" loading={disable.busy}>
            {t('account.mfa.disable')}
          </Button>
        </>
      }
    >
      <form id="mfa-disable-form" className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <p className="cf-modal__message">{t('account.mfa.disableDescription')}</p>
        <FormField label={t('auth.stepUp.password')} htmlFor="mfa-disable-password" required>
          <PasswordInput
            id="mfa-disable-password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </FormField>
        <FormField label={t('account.mfa.code')} htmlFor="mfa-disable-code" required>
          <Input
            id="mfa-disable-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
        </FormField>
        {disable.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(disable.error, { [ERROR_CODES.UNAUTHORIZED]: 'account.mfa.invalid' })}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
