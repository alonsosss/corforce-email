import { useState } from 'react';
import {
  API_KEY_ERRORS,
  API_KEY_STATUS,
  apiKeysApi,
  type ApiKey,
  type ApiKeyStatus,
  type CreatedApiKey,
  type SmtpSettings,
} from '@/api/apiKeys';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  DescriptionList,
  ErrorState,
  PageHeader,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconBan, IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { ApiKeyForm } from './ApiKeyForm';
import { ApiKeySecretDialog } from './ApiKeySecretDialog';
import { scopeKey, scopeLabel } from './apiKeyScopes';

const STATUS_TONE: Record<ApiKeyStatus, BadgeTone> = {
  [API_KEY_STATUS.active]: 'success',
  [API_KEY_STATUS.expired]: 'warning',
  [API_KEY_STATUS.revoked]: 'danger',
};

const REVOKE_ERRORS = { [API_KEY_ERRORS.API_KEY_REVOKED]: 'apiKeys.error.alreadyRevoked' } as const;

export default function ApiKeysPage() {
  const { can } = useAccess();
  if (!can(...PERMISSIONS.apiKeys.read)) {
    return <MissingPermission title={t('apiKeys.title')} description={t('apiKeys.subtitle')} />;
  }
  return <ApiKeys />;
}

function ApiKeys() {
  const toast = useToast();
  const { can } = useAccess();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedApiKey | null>(null);
  const [revoking, setRevoking] = useState<ApiKey | null>(null);

  const keys = useQuery(() => apiKeysApi.list(), []);
  const settings = useQuery(() => apiKeysApi.settings(), []);
  const smtp = settings.data?.smtp ?? null;

  const canCreate = can(...PERMISSIONS.apiKeys.create);
  const canRevoke = can(...PERMISSIONS.apiKeys.revoke);

  const columns: Column<ApiKey>[] = [
    { key: 'name', header: t('common.name'), render: (k) => <strong>{k.name}</strong> },
    {
      key: 'prefix',
      header: t('apiKeys.column.prefix'),
      render: (k) => <code className="cf-mono">{k.prefix}</code>,
    },
    {
      key: 'scopes',
      header: t('apiKeys.column.scopes'),
      render: (k) => (
        <div className="cf-cell-stack">
          {k.scopes.map((s) => (
            <span key={scopeKey(s)} title={scopeKey(s)}>
              {scopeLabel(s)}
            </span>
          ))}
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (k) => (
        <Badge tone={STATUS_TONE[k.status] ?? 'neutral'}>{tEnum('apiKeys.status', k.status)}</Badge>
      ),
    },
    { key: 'created', header: t('common.createdAt'), render: (k) => formatDateTime(k.created_at) },
    {
      key: 'expires',
      header: t('apiKeys.column.expires'),
      render: (k) => (k.expires_at ? formatDateTime(k.expires_at) : t('apiKeys.neverExpires')),
    },
    {
      key: 'lastUsed',
      header: t('apiKeys.column.lastUsed'),
      render: (k) =>
        k.last_used_at ? (
          <div className="cf-cell-stack">
            <span>{formatDateTime(k.last_used_at)}</span>
            {k.last_used_ip ? (
              <code className="cf-mono cf-text-muted cf-text-sm">{k.last_used_ip}</code>
            ) : null}
          </div>
        ) : (
          t('apiKeys.neverUsed')
        ),
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (k) =>
        canRevoke && k.status === API_KEY_STATUS.active ? (
          <div className="cf-table__actions">
            <Button
              size="sm"
              variant="ghost"
              icon={<IconBan size={14} />}
              onClick={() => setRevoking(k)}
            >
              {t('apiKeys.revoke')}
            </Button>
          </div>
        ) : null,
    },
  ];

  return (
    <div className="cf-stack">
      <PageHeader
        title={t('apiKeys.title')}
        description={t('apiKeys.subtitle')}
        actions={
          canCreate ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('apiKeys.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <DataTable
          columns={columns}
          rows={keys.data ?? []}
          rowKey={(k) => k.id}
          loading={keys.loading}
          error={keys.error}
          onRetry={keys.reload}
          empty={{ title: t('apiKeys.empty'), description: t('apiKeys.emptyDescription') }}
        />
      </Card>
      {settings.error ? (
        <Card title={t('apiKeys.smtp.title')}>
          <ErrorState error={settings.error} onRetry={settings.reload} />
        </Card>
      ) : smtp ? (
        <SmtpCard smtp={smtp} />
      ) : null}

      {creating ? (
        <ApiKeyForm
          onClose={() => setCreating(false)}
          onCreated={(result) => {
            setCreating(false);
            setCreated(result);
            keys.reload();
          }}
        />
      ) : null}
      {created ? (
        <ApiKeySecretDialog result={created} smtp={smtp} onClose={() => setCreated(null)} />
      ) : null}
      <ConfirmDialog
        open={revoking !== null}
        title={t('apiKeys.revokeTitle')}
        message={t('apiKeys.revokeConfirm', {
          name: revoking?.name ?? '',
          prefix: revoking?.prefix ?? '',
        })}
        confirmLabel={t('apiKeys.revoke')}
        danger
        errorOverrides={REVOKE_ERRORS}
        onCancel={() => setRevoking(null)}
        onConfirm={async () => {
          if (!revoking) return;
          const revoked = await apiKeysApi.revoke(revoking.id);
          keys.setData((current) =>
            current ? current.map((k) => (k.id === revoked.id ? revoked : k)) : current,
          );
          toast.success(t('apiKeys.revoked'));
          setRevoking(null);
        }}
      />
    </div>
  );
}

function SmtpCard({ smtp }: { smtp: SmtpSettings }) {
  return (
    <Card title={t('apiKeys.smtp.title')} description={t('apiKeys.smtp.description')}>
      <DescriptionList
        items={[
          { label: t('apiKeys.smtp.host'), value: <code className="cf-mono">{smtp.host}</code> },
          {
            label: t('apiKeys.smtp.starttlsPort'),
            value: <code className="cf-mono">{smtp.starttls_port}</code>,
          },
          {
            label: t('apiKeys.smtp.tlsPort'),
            value: <code className="cf-mono">{smtp.tls_port}</code>,
          },
          { label: t('apiKeys.smtp.username'), value: t('apiKeys.smtp.usernameIsPrefix') },
          { label: t('apiKeys.smtp.password'), value: t('apiKeys.smtp.passwordIsKey') },
        ]}
      />
    </Card>
  );
}
