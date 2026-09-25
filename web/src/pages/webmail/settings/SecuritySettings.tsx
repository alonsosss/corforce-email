import { useState, type FormEvent } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import {
  APP_PASSWORD_PROTOCOLS,
  webmailApi,
  type AppPasswordProtocol,
  type CreatedWebmailAppPassword,
  type WebmailAppPassword,
  type WebmailMfaSetup,
  type WebmailMfaStatus,
  type WebmailSecurity,
} from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  ConfirmDialog,
  CopyButton,
  DataTable,
  DescriptionList,
  ErrorState,
  FormField,
  Input,
  Modal,
  PasswordInput,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import { IconDownload, IconPlus, IconTrash } from '@/design/icons';
import { saveBlob } from '@/lib/download';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { QrCode } from '@/pages/shared/QrCode';
import { useWebmailStore } from '@/webmail/store';
import { MfaCodeField } from './MfaCodeField';
import { groupSecret, LOW_RECOVERY_CODES, recoveryCodesText, TOTP_DIGITS } from './mfa';

/**
 * Seguridad del buzon: verificacion en dos pasos y contrasenas de aplicacion. Con la
 * verificacion activa, los programas de correo (IMAP, POP3, SMTP) ya no aceptan la contrasena
 * principal: necesitan una contrasena de aplicacion.
 */
export function SecuritySettings() {
  const security = useQuery((signal) => webmailApi.security(signal), []);

  if (security.error && !security.data) {
    return (
      <Card title={t('webmail.security.mfa.title')}>
        <ErrorState
          error={security.error}
          title={t('webmail.security.unavailable')}
          onRetry={security.reload}
        />
      </Card>
    );
  }
  if (!security.data) {
    return (
      <Card title={t('webmail.security.mfa.title')}>
        <Skeleton lines={6} />
      </Card>
    );
  }
  return (
    <div className="cf-stack">
      <MfaCard mfa={security.data.mfa} onChanged={security.reload} />
      <AppPasswordsCard security={security.data} onChanged={security.reload} />
    </div>
  );
}

type Problems<F extends string> = Partial<Record<F, string>>;

/** Errores de reautenticacion que se pintan junto a su campo. */
function reauthProblem(err: unknown): Problems<'password' | 'code'> | null {
  const code = errorCode(err);
  if (code === ERROR_CODES.INVALID_CREDENTIALS) {
    return { password: t('webmail.password.currentWrong') };
  }
  if (code === ERROR_CODES.INVALID_MFA_CODE) return { code: t('error.code.INVALID_MFA_CODE') };
  if (code === ERROR_CODES.MFA_REQUIRED) return { code: t('error.code.MFA_REQUIRED') };
  return null;
}

/** Codigo de la aplicacion autenticadora, solo cifras. */
function CodeInput({
  id,
  value,
  onChange,
  error,
  disabled,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  disabled?: boolean;
}) {
  return (
    <FormField label={t('webmail.security.code')} htmlFor={id} error={error} required>
      <Input
        id={id}
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={TOTP_DIGITS}
        value={value}
        invalid={Boolean(error)}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value.replace(/\s+/g, ''))}
      />
    </FormField>
  );
}

function MfaCard({ mfa, onChanged }: { mfa: WebmailMfaStatus; onChanged: () => void }) {
  const toast = useToast();
  const [dialog, setDialog] = useState<'enable' | 'disable' | 'regenerate' | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const low = mfa.enabled && mfa.recovery_remaining < LOW_RECOVERY_CODES;

  return (
    <Card
      title={t('webmail.security.mfa.title')}
      description={t('webmail.security.mfa.description')}
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            {
              label: t('webmail.security.mfa.status'),
              value: (
                <Badge tone={mfa.enabled ? 'success' : 'neutral'}>
                  {t(mfa.enabled ? 'webmail.security.mfa.on' : 'webmail.security.mfa.off')}
                </Badge>
              ),
            },
            ...(mfa.enabled
              ? [
                  {
                    label: t('webmail.security.mfa.enabledAt'),
                    value: formatDateTime(mfa.enabled_at),
                  },
                  {
                    label: t('webmail.security.mfa.recoveryRemaining'),
                    value: String(mfa.recovery_remaining),
                  },
                ]
              : []),
          ]}
        />
        {low ? (
          <Alert tone="warning">
            {t('webmail.security.mfa.recoveryLow', { n: mfa.recovery_remaining })}
          </Alert>
        ) : null}
        <Alert tone={mfa.enabled ? 'info' : 'warning'} title={t('webmail.security.mfa.appsTitle')}>
          {t('webmail.security.mfa.appsNotice')}
        </Alert>
        <div className="cf-inline">
          {mfa.enabled ? (
            <>
              <Button onClick={() => setDialog('regenerate')}>
                {t('webmail.security.mfa.regenerate')}
              </Button>
              <Button variant="danger" onClick={() => setDialog('disable')}>
                {t('webmail.security.mfa.disable')}
              </Button>
            </>
          ) : (
            <Button variant="primary" onClick={() => setDialog('enable')}>
              {t('webmail.security.mfa.enable')}
            </Button>
          )}
        </div>
      </div>
      {dialog === 'enable' ? (
        <EnableMfaDialog
          onClose={() => setDialog(null)}
          onActivated={(recovery, otherSessionsClosed) => {
            setDialog(null);
            setCodes(recovery);
            if (otherSessionsClosed) toast.info(t('webmail.security.mfa.otherSessionsClosed'));
            onChanged();
          }}
        />
      ) : null}
      {dialog === 'disable' ? (
        <DisableMfaDialog
          onClose={() => setDialog(null)}
          onDisabled={() => {
            setDialog(null);
            toast.success(t('webmail.security.mfa.disabledDone'));
            onChanged();
          }}
        />
      ) : null}
      {dialog === 'regenerate' ? (
        <RegenerateCodesDialog
          onClose={() => setDialog(null)}
          onRegenerated={(recovery) => {
            setDialog(null);
            setCodes(recovery);
            onChanged();
          }}
        />
      ) : null}
      {codes ? <RecoveryCodesDialog codes={codes} onClose={() => setCodes(null)} /> : null}
    </Card>
  );
}

/**
 * Activacion en dos pasos: la contrasena actual (una sesion robada no puede activar su propio
 * TOTP y dejar fuera al dueno) y, con el QR escaneado, un codigo que confirma el secreto. Nada
 * se guarda hasta ese codigo.
 */
function EnableMfaDialog({
  onClose,
  onActivated,
}: {
  onClose: () => void;
  onActivated: (codes: string[], otherSessionsClosed: boolean) => void;
}) {
  const [setup, setSetup] = useState<WebmailMfaSetup | null>(null);
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [problems, setProblems] = useState<Problems<'password' | 'code'>>({});
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const start = async () => {
    if (!password) {
      setProblems({ password: t('webmail.password.currentRequired') });
      return;
    }
    setBusy(true);
    setError(null);
    try {
      setSetup(await webmailApi.mfaSetup(password));
      setPassword('');
      setProblems({});
    } catch (err) {
      const field = reauthProblem(err);
      if (field) setProblems(field);
      else setError(err);
    } finally {
      setBusy(false);
    }
  };

  const activate = async () => {
    if (!setup) return;
    if (code.length !== TOTP_DIGITS) {
      setProblems({ code: t('webmail.security.codeRequired', { n: TOTP_DIGITS }) });
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { recovery_codes, other_sessions_closed } = await webmailApi.mfaActivate(
        setup.secret,
        code,
      );
      onActivated(recovery_codes, other_sessions_closed);
    } catch (err) {
      setCode('');
      // La preparacion caduco (10 min) o se hizo en otra pestana: se vuelve a pedir la contrasena.
      if (errorCode(err) === ERROR_CODES.MFA_SETUP_EXPIRED) {
        setSetup(null);
        setProblems({});
        setError(err);
        setBusy(false);
        return;
      }
      const field = reauthProblem(err);
      if (field) setProblems(field);
      else setError(err);
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void (setup ? activate() : start());
  };

  return (
    <Modal
      open
      size="lg"
      title={t('webmail.security.mfa.enableTitle')}
      onClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="wm-mfa-enable" variant="primary" loading={busy}>
            {t(setup ? 'webmail.security.mfa.activate' : 'webmail.security.mfa.continue')}
          </Button>
        </>
      }
    >
      <form id="wm-mfa-enable" className="cf-form" noValidate onSubmit={submit}>
        {setup ? (
          <>
            <p className="cf-modal__message">{t('webmail.security.mfa.scan')}</p>
            <div className="cf-wm-mfa-setup">
              <QrCode value={setup.provisioning_uri} label={t('webmail.security.mfa.qrAlt')} />
              <div className="cf-stack" style={{ gap: 'var(--cf-space-3)', flex: '1 1 240px' }}>
                <FormField
                  label={t('webmail.security.mfa.secret')}
                  htmlFor="wm-mfa-secret"
                  hint={t('webmail.security.mfa.secretHint')}
                >
                  <div className="cf-secret">
                    <span id="wm-mfa-secret" className="cf-secret__value cf-mono">
                      {groupSecret(setup.secret)}
                    </span>
                    <CopyButton value={setup.secret} />
                  </div>
                </FormField>
                <CodeInput
                  id="wm-mfa-activate-code"
                  value={code}
                  onChange={(value) => {
                    setCode(value);
                    setProblems({});
                  }}
                  error={problems.code}
                  disabled={busy}
                />
              </div>
            </div>
          </>
        ) : (
          <>
            <Alert tone="warning" title={t('webmail.security.mfa.appsTitle')}>
              {t('webmail.security.mfa.appsNotice')}
            </Alert>
            <p className="cf-modal__message">{t('webmail.security.mfa.passwordStep')}</p>
            <FormField
              label={t('webmail.password.current')}
              htmlFor="wm-mfa-enable-password"
              error={problems.password}
              required
            >
              <PasswordInput
                id="wm-mfa-enable-password"
                autoComplete="current-password"
                value={password}
                invalid={Boolean(problems.password)}
                disabled={busy}
                onChange={(e) => {
                  setPassword(e.target.value);
                  setProblems({});
                }}
              />
            </FormField>
          </>
        )}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}

function DisableMfaDialog({
  onClose,
  onDisabled,
}: {
  onClose: () => void;
  onDisabled: () => void;
}) {
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [problems, setProblems] = useState<Problems<'password' | 'code'>>({});
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const found: Problems<'password' | 'code'> = {};
    if (!password) found.password = t('webmail.password.currentRequired');
    if (!code) found.code = t('webmail.security.codeMissing');
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      await webmailApi.disableMfa(password, code);
      onDisabled();
    } catch (err) {
      setCode('');
      const field = reauthProblem(err);
      if (field) setProblems(field);
      else setError(err);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={t('webmail.security.mfa.disableTitle')}
      onClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="wm-mfa-disable" variant="danger" loading={busy}>
            {t('webmail.security.mfa.disable')}
          </Button>
        </>
      }
    >
      <form id="wm-mfa-disable" className="cf-form" noValidate onSubmit={(e) => void submit(e)}>
        <p className="cf-modal__message">{t('webmail.security.mfa.disableDescription')}</p>
        <FormField
          label={t('webmail.password.current')}
          htmlFor="wm-mfa-disable-password"
          error={problems.password}
          required
        >
          <PasswordInput
            id="wm-mfa-disable-password"
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
        <MfaCodeField
          id="wm-mfa-disable-code"
          value={code}
          onChange={(value) => {
            setCode(value);
            setProblems((p) => ({ ...p, code: undefined }));
          }}
          error={problems.code}
          disabled={busy}
        />
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}

function RegenerateCodesDialog({
  onClose,
  onRegenerated,
}: {
  onClose: () => void;
  onRegenerated: (codes: string[]) => void;
}) {
  const [code, setCode] = useState('');
  const [problem, setProblem] = useState<string | undefined>();
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!code) {
      setProblem(t('webmail.security.codeMissing'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { recovery_codes } = await webmailApi.regenerateRecoveryCodes(code);
      onRegenerated(recovery_codes);
    } catch (err) {
      setCode('');
      const field = reauthProblem(err);
      if (field?.code) setProblem(field.code);
      else setError(err);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={t('webmail.security.mfa.regenerateTitle')}
      onClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="wm-mfa-regenerate" variant="primary" loading={busy}>
            {t('webmail.security.mfa.regenerate')}
          </Button>
        </>
      }
    >
      <form id="wm-mfa-regenerate" className="cf-form" noValidate onSubmit={(e) => void submit(e)}>
        <p className="cf-modal__message">{t('webmail.security.mfa.regenerateDescription')}</p>
        <MfaCodeField
          id="wm-mfa-regenerate-code"
          value={code}
          onChange={(value) => {
            setCode(value);
            setProblem(undefined);
          }}
          error={problem}
          disabled={busy}
        />
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}

/**
 * Los codigos de recuperacion solo existen en esta respuesta: el servicio guarda su huella. No
 * se cierra sin confirmar que se guardaron.
 */
function RecoveryCodesDialog({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const mailbox = useWebmailStore((s) => s.session?.username ?? '');
  const [saved, setSaved] = useState(false);
  const text = recoveryCodesText(mailbox, codes);

  return (
    <Modal
      open
      dismissible={false}
      title={t('webmail.security.recovery.title')}
      onClose={onClose}
      footer={
        <Button variant="primary" disabled={!saved} onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <Alert tone="warning" title={t('webmail.security.recovery.onceTitle')}>
          {t('webmail.security.recovery.once')}
        </Alert>
        <ul className="cf-wm-recovery-codes" aria-label={t('webmail.security.recovery.title')}>
          {codes.map((code) => (
            <li key={code} className="cf-mono">
              {code}
            </li>
          ))}
        </ul>
        <div className="cf-inline">
          <CopyButton
            value={codes.join('\n')}
            label={t('webmail.security.recovery.copy')}
            withText
          />
          <Button
            size="sm"
            variant="ghost"
            icon={<IconDownload size={14} />}
            onClick={() =>
              saveBlob(
                new Blob([text], { type: 'text/plain' }),
                t('webmail.security.recovery.filename', { mailbox }),
              )
            }
          >
            {t('webmail.security.recovery.download')}
          </Button>
        </div>
        <Checkbox
          label={t('webmail.security.recovery.confirm')}
          checked={saved}
          onChange={(e) => setSaved(e.target.checked)}
        />
      </div>
    </Modal>
  );
}

function protocolLabel(protocol: AppPasswordProtocol): string {
  return tEnum('mail.protocol', `${protocol}_access`);
}

function enabledProtocols(item: WebmailAppPassword): AppPasswordProtocol[] {
  return APP_PASSWORD_PROTOCOLS.filter((protocol) => item[`${protocol}_access`]);
}

function AppPasswordsCard({
  security,
  onChanged,
}: {
  security: WebmailSecurity;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedWebmailAppPassword | null>(null);
  const [revoking, setRevoking] = useState<WebmailAppPassword | null>(null);
  const items = security.app_passwords;
  const max = security.app_passwords_max;
  const full = max !== null && items.length >= max;

  const columns: Column<WebmailAppPassword>[] = [
    { key: 'name', header: t('common.name'), render: (p) => <strong>{p.name}</strong> },
    {
      key: 'protocols',
      header: t('webmail.security.apps.protocols'),
      render: (p) => (
        <span className="cf-inline-list">
          {enabledProtocols(p).map((protocol) => (
            <Badge key={protocol} tone="info">
              {protocolLabel(protocol)}
            </Badge>
          ))}
        </span>
      ),
    },
    { key: 'created', header: t('common.createdAt'), render: (p) => formatDateTime(p.created_at) },
    {
      key: 'used',
      header: t('appPasswords.column.lastUsed'),
      render: (p) => (p.last_used_at ? formatDateTime(p.last_used_at) : t('common.dash')),
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (p) => (
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon={<IconTrash size={16} />}
          onClick={() => setRevoking(p)}
        >
          {t('webmail.security.apps.revokeName', { name: p.name })}
        </Button>
      ),
    },
  ];

  return (
    <Card
      flush
      title={t('appPasswords.title')}
      description={t('webmail.security.apps.description')}
      actions={
        <Button
          size="sm"
          variant="primary"
          icon={<IconPlus size={16} />}
          disabled={full}
          onClick={() => setCreating(true)}
        >
          {t('appPasswords.new')}
        </Button>
      }
    >
      {full ? (
        <div style={{ padding: 'var(--cf-space-4)' }}>
          <Alert tone="info">{t('webmail.security.apps.full', { max: max ?? items.length })}</Alert>
        </div>
      ) : null}
      <DataTable
        columns={columns}
        rows={items}
        rowKey={(p) => p.id}
        empty={{ title: t('appPasswords.empty') }}
      />
      {creating ? (
        <CreateAppPasswordDialog
          mfaEnabled={security.mfa.enabled}
          onClose={() => setCreating(false)}
          onCreated={(result) => {
            setCreating(false);
            setCreated(result);
            onChanged();
          }}
        />
      ) : null}
      {created ? (
        <Modal
          open
          dismissible={false}
          title={t('appPasswords.created.title', { name: created.name })}
          onClose={() => setCreated(null)}
          footer={
            <Button variant="primary" onClick={() => setCreated(null)}>
              {t('appPasswords.created.done')}
            </Button>
          }
        >
          <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
            <Alert tone="warning" title={t('appPasswords.created.warningTitle')}>
              {t('appPasswords.created.warning')}
            </Alert>
            <div className="cf-secret">
              <span className="cf-secret__value" aria-label={t('appPasswords.created.valueLabel')}>
                {created.password}
              </span>
              <CopyButton value={created.password} withText />
            </div>
          </div>
        </Modal>
      ) : null}
      <ConfirmDialog
        open={revoking !== null}
        title={t('appPasswords.revoke')}
        message={t('webmail.security.apps.revokeConfirm', { name: revoking?.name ?? '' })}
        confirmLabel={t('appPasswords.revoke')}
        danger
        onCancel={() => setRevoking(null)}
        onConfirm={async () => {
          if (!revoking) return;
          await webmailApi.deleteAppPassword(revoking.id);
          toast.success(t('appPasswords.revoked'));
          setRevoking(null);
          onChanged();
        }}
      />
    </Card>
  );
}

type AppPasswordField = 'name' | 'protocols' | 'password' | 'code';

function CreateAppPasswordDialog({
  mfaEnabled,
  onClose,
  onCreated,
}: {
  mfaEnabled: boolean;
  onClose: () => void;
  onCreated: (result: CreatedWebmailAppPassword) => void;
}) {
  const [name, setName] = useState('');
  const [protocols, setProtocols] = useState<Record<AppPasswordProtocol, boolean>>(() => {
    const all = {} as Record<AppPasswordProtocol, boolean>;
    for (const protocol of APP_PASSWORD_PROTOCOLS) all[protocol] = true;
    return all;
  });
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [problems, setProblems] = useState<Problems<AppPasswordField>>({});
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const found: Problems<AppPasswordField> = {};
    if (!name.trim()) found.name = t('webmail.security.apps.nameRequired');
    if (!APP_PASSWORD_PROTOCOLS.some((protocol) => protocols[protocol])) {
      found.protocols = t('webmail.security.apps.protocolsRequired');
    }
    if (!password) found.password = t('webmail.password.currentRequired');
    if (mfaEnabled && !code) found.code = t('webmail.security.codeMissing');
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      const result = await webmailApi.createAppPassword(
        { name: name.trim(), ...protocols },
        { current_password: password, code: mfaEnabled ? code : undefined },
      );
      onCreated(result);
    } catch (err) {
      setBusy(false);
      const field = reauthProblem(err);
      if (field) {
        if (field.password) setPassword('');
        setCode('');
        setProblems(field);
      } else setError(err);
    }
  };

  return (
    <Modal
      open
      title={t('appPasswords.form.title')}
      onClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="wm-app-password" variant="primary" loading={busy}>
            {t('common.create')}
          </Button>
        </>
      }
    >
      <form id="wm-app-password" className="cf-form" noValidate onSubmit={(e) => void submit(e)}>
        <FormField
          label={t('common.name')}
          htmlFor="wm-app-password-name"
          error={problems.name}
          hint={t('appPasswords.form.nameHint')}
          required
        >
          <Input
            id="wm-app-password-name"
            autoComplete="off"
            value={name}
            invalid={Boolean(problems.name)}
            disabled={busy}
            onChange={(e) => {
              setName(e.target.value);
              setProblems((p) => ({ ...p, name: undefined }));
            }}
          />
        </FormField>
        <fieldset className="cf-wm-fieldset">
          <legend className="cf-form__section">{t('webmail.security.apps.protocols')}</legend>
          <div className="cf-inline-list" style={{ gap: 'var(--cf-space-4)' }}>
            {APP_PASSWORD_PROTOCOLS.map((protocol) => (
              <Checkbox
                key={protocol}
                label={protocolLabel(protocol)}
                checked={protocols[protocol]}
                disabled={busy}
                onChange={(e) => {
                  setProtocols((current) => ({ ...current, [protocol]: e.target.checked }));
                  setProblems((p) => ({ ...p, protocols: undefined }));
                }}
              />
            ))}
          </div>
          {problems.protocols ? (
            <span className="cf-field__error" role="alert">
              {problems.protocols}
            </span>
          ) : null}
        </fieldset>
        <FormField
          label={t('webmail.password.current')}
          htmlFor="wm-app-password-current"
          error={problems.password}
          hint={t('webmail.security.apps.passwordHint')}
          required
        >
          <PasswordInput
            id="wm-app-password-current"
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
        {mfaEnabled ? (
          <MfaCodeField
            id="wm-app-password-code"
            value={code}
            onChange={(value) => {
              setCode(value);
              setProblems((p) => ({ ...p, code: undefined }));
            }}
            error={problems.code}
            disabled={busy}
          />
        ) : null}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
