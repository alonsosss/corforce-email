import { useState } from 'react';
import type { SessionInfo } from '@/api/identity';
import {
  Badge,
  Button,
  ConfirmDialog,
  DataTable,
  type Column,
  type PaginationState,
} from '@/design/components';
import { formatDateTime, summarizeUserAgent } from '@/lib/format';
import { t } from '@/i18n';

export interface SessionsTableProps {
  rows: SessionInfo[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  pagination: PaginationState;
  showUser?: boolean;
  showTenant?: boolean;
  onRevoke?: (session: SessionInfo) => Promise<void>;
}

export function SessionsTable({
  rows,
  loading,
  error,
  onRetry,
  pagination,
  showUser = false,
  showTenant = false,
  onRevoke,
}: SessionsTableProps) {
  const [target, setTarget] = useState<SessionInfo | null>(null);

  const columns: Column<SessionInfo>[] = [
    ...(showUser
      ? [
          {
            key: 'user',
            header: t('session.user'),
            render: (s: SessionInfo) => (
              <div>
                <div>{s.user_name || s.user_email}</div>
                <div className="cf-text-muted cf-text-sm">{s.user_email}</div>
              </div>
            ),
          },
        ]
      : []),
    ...(showTenant
      ? [
          {
            key: 'tenant',
            header: t('session.tenant'),
            render: (s: SessionInfo) => s.tenant_name || s.tenant_id,
          },
        ]
      : []),
    {
      key: 'ip',
      header: t('session.ip'),
      render: (s) => <span className="cf-mono">{s.ip_address}</span>,
    },
    { key: 'device', header: t('session.device'), render: (s) => summarizeUserAgent(s.user_agent) },
    { key: 'login', header: t('session.loginAt'), render: (s) => formatDateTime(s.login_at) },
    { key: 'seen', header: t('session.lastSeen'), render: (s) => formatDateTime(s.last_seen_at) },
    { key: 'expires', header: t('session.expires'), render: (s) => formatDateTime(s.expires_at) },
    {
      key: 'state',
      header: t('common.status'),
      render: (s) =>
        s.revoked ? (
          <Badge tone="danger">{t('session.badge.revoked')}</Badge>
        ) : (
          <Badge tone="success">{t('session.badge.active')}</Badge>
        ),
    },
    ...(onRevoke
      ? [
          {
            key: 'actions',
            header: '',
            align: 'right' as const,
            render: (s: SessionInfo) =>
              s.revoked ? null : (
                <Button size="sm" variant="ghost" onClick={() => setTarget(s)}>
                  {t('session.revoke')}
                </Button>
              ),
          },
        ]
      : []),
  ];

  return (
    <>
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(s) => s.id}
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={{ title: t('session.empty') }}
        pagination={pagination}
      />
      {onRevoke ? (
        <ConfirmDialog
          open={target !== null}
          title={t('session.revoke')}
          message={t('session.revokeConfirm', { user: target?.user_email ?? '' })}
          confirmLabel={t('session.revoke')}
          danger
          onCancel={() => setTarget(null)}
          onConfirm={async () => {
            if (!target) return;
            await onRevoke(target);
            setTarget(null);
          }}
        />
      ) : null}
    </>
  );
}
