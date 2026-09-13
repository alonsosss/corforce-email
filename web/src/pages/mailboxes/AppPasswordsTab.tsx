import { useState } from 'react';
import {
  mailDirectoryApi,
  type AppPassword,
  type CreatedAppPassword,
  type Mailbox,
} from '@/api/mailDirectory';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  ConfirmDialog,
  CopyButton,
  DataTable,
  FormField,
  Input,
  Modal,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { RowActions } from '@/pages/shared/RowActions';
import { ActiveBadge } from '@/pages/shared/StatusBadges';
import { APP_PASSWORD_ACCESS_KEYS, AccessCheckboxes, ProtocolBadges, allAccess } from './access';

const NAME_MAX_LENGTH = 100;

export function AppPasswordsTab({ mailbox }: { mailbox: Mailbox }) {
  const toast = useToast();
  const { can } = useAccess();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedAppPassword | null>(null);
  const [revoking, setRevoking] = useState<AppPassword | null>(null);

  const list = useQuery(() => mailDirectoryApi.listAppPasswords(mailbox.id), [mailbox.id]);

  const toggle = useAction(async (item: AppPassword) => {
    await mailDirectoryApi.updateAppPassword(mailbox.id, item.id, { active: !item.active });
    list.reload();
  });

  const canUpdate = can(...PERMISSIONS.appPasswords.update);
  const canDelete = can(...PERMISSIONS.appPasswords.delete);

  const columns: Column<AppPassword>[] = [
    { key: 'name', header: t('common.name'), render: (p) => <strong>{p.name}</strong> },
    {
      key: 'protocols',
      header: t('mailboxes.column.protocols'),
      render: (p) => <ProtocolBadges value={p} />,
    },
    { key: 'active', header: t('common.status'), render: (p) => <ActiveBadge active={p.active} /> },
    {
      key: 'used',
      header: t('appPasswords.column.lastUsed'),
      render: (p) => formatDateTime(p.last_used_at),
    },
    { key: 'created', header: t('common.createdAt'), render: (p) => formatDateTime(p.created_at) },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (p) => (
        <RowActions onDelete={canDelete ? () => setRevoking(p) : undefined}>
          {canUpdate ? (
            <Button
              size="sm"
              variant="ghost"
              loading={toggle.busy}
              onClick={() => {
                void toggle.run(p).then((ok) => {
                  if (ok)
                    toast.success(
                      p.active ? t('appPasswords.deactivated') : t('appPasswords.activated'),
                    );
                  else toast.error(errorMessage(toggle.error));
                });
              }}
            >
              {p.active ? t('common.deactivate') : t('common.activate')}
            </Button>
          ) : null}
        </RowActions>
      ),
    },
  ];

  return (
    <Card
      flush
      title={t('appPasswords.title')}
      description={t('appPasswords.description')}
      actions={
        can(...PERMISSIONS.appPasswords.create) ? (
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('appPasswords.new')}
          </Button>
        ) : null
      }
    >
      <DataTable
        columns={columns}
        rows={list.data ?? []}
        rowKey={(p) => p.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        empty={{ title: t('appPasswords.empty') }}
      />
      {creating ? (
        <AppPasswordForm
          mailboxId={mailbox.id}
          onClose={() => setCreating(false)}
          onCreated={(result) => {
            setCreating(false);
            setCreated(result);
            list.reload();
          }}
        />
      ) : null}
      {created ? <OneTimePassword result={created} onClose={() => setCreated(null)} /> : null}
      <ConfirmDialog
        open={revoking !== null}
        title={t('appPasswords.revoke')}
        message={t('appPasswords.revokeConfirm', { name: revoking?.name ?? '' })}
        confirmLabel={t('appPasswords.revoke')}
        danger
        onCancel={() => setRevoking(null)}
        onConfirm={async () => {
          if (!revoking) return;
          await mailDirectoryApi.deleteAppPassword(mailbox.id, revoking.id);
          toast.success(t('appPasswords.revoked'));
          setRevoking(null);
          list.reload();
        }}
      />
    </Card>
  );
}

function AppPasswordForm({
  mailboxId,
  onClose,
  onCreated,
}: {
  mailboxId: string;
  onClose: () => void;
  onCreated: (result: CreatedAppPassword) => void;
}) {
  const [name, setName] = useState('');
  const [access, setAccess] = useState(allAccess(APP_PASSWORD_ACCESS_KEYS, true));
  const [nameError, setNameError] = useState<string | null>(null);

  const action = useAction(async () => {
    const { data } = await mailDirectoryApi.createAppPassword(mailboxId, {
      name: name.trim(),
      ...access,
    });
    onCreated(data);
  });

  const submit = async () => {
    const error = validateField(name, rules.required, rules.maxLength(NAME_MAX_LENGTH));
    setNameError(error);
    if (error) return;
    await action.run();
  };

  return (
    <FormModal
      id="app-password-form"
      title={t('appPasswords.form.title')}
      submitLabel={t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('common.name')}
        htmlFor="app-password-name"
        required
        error={nameError}
        hint={t('appPasswords.form.nameHint')}
      >
        <Input
          id="app-password-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          invalid={Boolean(nameError)}
          autoComplete="off"
        />
      </FormField>
      <div className="cf-form__section">{t('mailboxes.form.access')}</div>
      <AccessCheckboxes keys={APP_PASSWORD_ACCESS_KEYS} value={access} onChange={setAccess} />
    </FormModal>
  );
}

/**
 * La contrasena generada solo existe en esta respuesta: el servidor guarda su hash. Se
 * muestra una vez, sin cierre accidental, y se descarta del estado al cerrar.
 */
function OneTimePassword({ result, onClose }: { result: CreatedAppPassword; onClose: () => void }) {
  return (
    <Modal
      open
      dismissible={false}
      title={t('appPasswords.created.title', { name: result.app_password.name })}
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
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
            {result.password}
          </span>
          <CopyButton value={result.password} withText />
        </div>
      </div>
    </Modal>
  );
}
