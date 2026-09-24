import { useState } from 'react';
import { largeFilesApi, type LargeFile } from '@/api/largeFiles';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  CopyButton,
  DataTable,
  EmptyState,
  ErrorState,
  Meter,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import { IconTrash } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { t, tEnum } from '@/i18n';
import { formatDate } from '@/lib/format';
import { formatBytes, usageRatio } from '@/lib/quota';
import { largeFileStateTone } from '../largeFiles';

/** Enlaces de ficheros grandes del buzon: estado, descargas, caducidad, copiar y revocar. */
export function LargeFilesSettings() {
  const toast = useToast();
  const listing = useQuery((signal) => largeFilesApi.list(signal), []);
  const [revoking, setRevoking] = useState<LargeFile | null>(null);

  const title = t('webmail.largeFiles.title');
  if (listing.error && !listing.data) {
    return (
      <Card title={title}>
        <ErrorState
          error={listing.error}
          title={t('webmail.largeFiles.unavailable')}
          onRetry={listing.reload}
        />
      </Card>
    );
  }
  if (!listing.data) {
    return (
      <Card title={title}>
        <Skeleton lines={6} />
      </Card>
    );
  }
  const { enabled, items, usage, limits } = listing.data;
  if (!enabled) {
    return (
      <Card title={title}>
        <EmptyState title={t('webmail.largeFiles.disabled')} />
      </Card>
    );
  }

  const columns: Column<LargeFile>[] = [
    {
      key: 'name',
      header: t('webmail.largeFiles.col.name'),
      render: (f) => <span className="cf-wm-largefiles__name">{f.name}</span>,
    },
    {
      key: 'size',
      header: t('webmail.largeFiles.col.size'),
      align: 'right',
      render: (f) => formatBytes(f.size_bytes),
    },
    {
      key: 'state',
      header: t('webmail.largeFiles.col.state'),
      render: (f) => (
        <Badge tone={largeFileStateTone(f.state)}>
          {tEnum('webmail.largeFiles.state', f.state)}
        </Badge>
      ),
    },
    {
      key: 'downloads',
      header: t('webmail.largeFiles.col.downloads'),
      align: 'right',
      render: (f) => t('webmail.largeFiles.downloads', { n: f.downloads, max: f.max_downloads }),
    },
    {
      key: 'expires',
      header: t('webmail.largeFiles.col.expires'),
      render: (f) => formatDate(f.expires_at),
    },
    {
      key: 'actions',
      header: t('common.actions'),
      align: 'right',
      render: (f) =>
        f.state === 'active' && f.url ? (
          <span className="cf-wm-largefiles__actions">
            <CopyButton value={f.url} label={t('webmail.largeFiles.copy', { name: f.name })} />
            <Button
              size="sm"
              variant="ghost"
              icon={<IconTrash size={14} />}
              aria-label={t('webmail.largeFiles.revokeLabel', { name: f.name })}
              onClick={() => setRevoking(f)}
            >
              {t('webmail.largeFiles.revoke')}
            </Button>
          </span>
        ) : null,
    },
  ];

  const ratio = usageRatio(usage.mailbox_bytes, limits.mailbox_quota_bytes) ?? 0;
  return (
    <Card title={title}>
      <p className="cf-text-sm cf-text-muted">{t('webmail.largeFiles.description')}</p>
      <div className="cf-wm-largefiles__usage">
        <Meter ratio={ratio} label={t('webmail.largeFiles.usageLabel')} />
        <span className="cf-text-sm">
          {t('webmail.largeFiles.usage', {
            used: formatBytes(usage.mailbox_bytes),
            quota: formatBytes(limits.mailbox_quota_bytes),
            n: usage.mailbox_active,
            max: limits.max_active_per_mailbox,
          })}
        </span>
      </div>
      <DataTable
        columns={columns}
        rows={items}
        rowKey={(f) => f.id}
        loading={listing.loading && items.length === 0}
        empty={{ title: t('webmail.largeFiles.empty') }}
      />
      <ConfirmDialog
        open={revoking !== null}
        title={t('webmail.largeFiles.revokeTitle')}
        message={revoking ? t('webmail.largeFiles.revokeConfirm', { name: revoking.name }) : ''}
        confirmLabel={t('webmail.largeFiles.revoke')}
        danger
        onCancel={() => setRevoking(null)}
        onConfirm={async () => {
          if (!revoking) return;
          const revoked = await largeFilesApi.revoke(revoking.id);
          listing.setData((current) =>
            current
              ? { ...current, items: current.items.map((f) => (f.id === revoked.id ? revoked : f)) }
              : current,
          );
          setRevoking(null);
          listing.reload();
          toast.success(t('webmail.largeFiles.revoked'));
        }}
      />
    </Card>
  );
}
