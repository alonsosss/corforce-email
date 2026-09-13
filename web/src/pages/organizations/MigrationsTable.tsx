import { organizationApi, type TenantMigrationInfo } from '@/api/organization';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import {
  Badge,
  Button,
  DataTable,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { t, tEnum } from '@/i18n';

export function migrationTone(status: TenantMigrationInfo['status']): BadgeTone {
  if (status === 'ok') return 'success';
  if (status === 'pending') return 'warning';
  return 'danger';
}

export interface MigrationsTableProps {
  rows: TenantMigrationInfo[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  onMigrated: () => void;
  canRun: boolean;
  showTenant?: boolean;
}

export function MigrationsTable({
  rows,
  loading,
  error,
  onRetry,
  onMigrated,
  canRun,
  showTenant = true,
}: MigrationsTableProps) {
  const toast = useToast();
  const migrate = useAction(async (tenantId: string) => {
    await organizationApi.migrateTenant(tenantId);
  });

  const columns: Column<TenantMigrationInfo>[] = [
    ...(showTenant
      ? [
          {
            key: 'tenant',
            header: t('orgs.migrations.column.tenant'),
            render: (m: TenantMigrationInfo) => <span className="cf-mono">{m.slug}</span>,
          },
        ]
      : []),
    {
      key: 'db',
      header: t('orgs.migrations.column.db'),
      render: (m) => <span className="cf-mono">{m.db_name}</span>,
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (m) => (
        <div>
          <Badge tone={migrationTone(m.status)}>{tEnum('orgs.migrations.status', m.status)}</Badge>
          {m.error ? (
            <div className="cf-text-sm" style={{ color: 'var(--cf-danger-strong)' }}>
              {m.error}
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'applied',
      header: t('orgs.migrations.column.applied'),
      align: 'right',
      render: (m) => m.applied,
    },
    {
      key: 'pending',
      header: t('orgs.migrations.column.pending'),
      render: (m) =>
        m.pending?.length ? (
          <span className="cf-mono cf-text-sm" title={m.pending.join('\n')}>
            {m.pending.length}
          </span>
        ) : (
          t('common.dash')
        ),
    },
    {
      key: 'baselined',
      header: t('orgs.migrations.column.baselined'),
      render: (m) => (m.baselined ? t('common.yes') : t('common.no')),
    },
    ...(canRun
      ? [
          {
            key: 'actions',
            header: '',
            align: 'right' as const,
            render: (m: TenantMigrationInfo) => (
              <Button
                size="sm"
                icon={<IconRefresh size={14} />}
                loading={migrate.busy}
                onClick={() => {
                  void migrate.run(m.tenant_id).then((ok) => {
                    if (ok) {
                      toast.success(t('orgs.migrations.migrated'));
                      onMigrated();
                    } else {
                      toast.error(
                        errorMessage(migrate.error, {
                          [ERROR_CODES.CONFLICT]: 'orgs.migrations.locked',
                        }),
                      );
                    }
                  });
                }}
              >
                {t('orgs.migrations.runOne')}
              </Button>
            ),
          },
        ]
      : []),
  ];

  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(m) => m.tenant_id}
      loading={loading}
      error={error}
      onRetry={onRetry}
      empty={{ title: t('orgs.migrations.empty') }}
    />
  );
}
