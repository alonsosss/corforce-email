import { useState } from 'react';
import { mailDirectoryApi, type DirectoryDomain } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { Card, DataTable, useToast, type Column } from '@/design/components';
import { formatQuota } from '@/lib/quota';
import { t } from '@/i18n';
import { ActiveBadge, YesNo } from '@/pages/shared/StatusBadges';
import { RowActions } from '@/pages/shared/RowActions';
import { DirectoryDomainForm } from './DirectoryDomainForm';

function limitText(value: number): string {
  return value === 0 ? t('directory.noLimit') : String(value);
}

/** Limites y cuotas de cada dominio en el directorio de la celda (lo que leen los motores). */
export function DirectoryTab() {
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [editing, setEditing] = useState<DirectoryDomain | null>(null);

  const domains = useQuery(
    () => mailDirectoryApi.listDomains({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );
  const canUpdate = can(...PERMISSIONS.domains.update);

  const columns: Column<DirectoryDomain>[] = [
    {
      key: 'domain',
      header: t('domains.column.domain'),
      render: (d) => (
        <div className="cf-cell-stack">
          <strong className="cf-mono">{d.domain}</strong>
          {d.description ? <span className="cf-text-muted cf-text-sm">{d.description}</span> : null}
        </div>
      ),
    },
    { key: 'active', header: t('common.status'), render: (d) => <ActiveBadge active={d.active} /> },
    {
      key: 'mailboxes',
      header: t('directory.column.maxMailboxes'),
      align: 'right',
      render: (d) => limitText(d.max_mailboxes),
    },
    {
      key: 'aliases',
      header: t('directory.column.maxAliases'),
      align: 'right',
      render: (d) => limitText(d.max_aliases),
    },
    {
      key: 'defaultQuota',
      header: t('directory.column.defaultQuota'),
      align: 'right',
      render: (d) => formatQuota(d.default_quota_bytes),
    },
    {
      key: 'maxQuota',
      header: t('directory.column.maxQuota'),
      align: 'right',
      render: (d) => formatQuota(d.max_quota_bytes),
    },
    {
      key: 'quota',
      header: t('directory.column.totalQuota'),
      align: 'right',
      render: (d) => formatQuota(d.quota_bytes),
    },
    {
      key: 'backupmx',
      header: t('directory.column.backupMx'),
      render: (d) => <YesNo value={d.backupmx} />,
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (d) => <RowActions onEdit={canUpdate ? () => setEditing(d) : undefined} />,
    },
  ];

  return (
    <Card flush title={t('directory.title')} description={t('directory.description')}>
      <DataTable
        columns={columns}
        rows={domains.data?.items ?? []}
        rowKey={(d) => d.id}
        loading={domains.loading}
        error={domains.error}
        onRetry={domains.reload}
        empty={{ title: t('directory.empty'), description: t('directory.emptyDescription') }}
        pagination={{
          page: domains.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: domains.data?.total ?? 0,
          totalPages: domains.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {editing ? (
        <DirectoryDomainForm
          domain={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            toast.success(t('directory.updated'));
            setEditing(null);
            domains.reload();
          }}
        />
      ) : null}
    </Card>
  );
}
