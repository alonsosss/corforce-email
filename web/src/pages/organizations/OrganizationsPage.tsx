import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { organizationApi, type Tenant, type TenantStatus } from '@/api/organization';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  PageHeader,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { OrganizationForm } from './OrganizationForm';

export function tenantStatusTone(status: TenantStatus): BadgeTone {
  if (status === 'active') return 'success';
  if (status === 'suspended') return 'warning';
  return 'neutral';
}

export default function OrganizationsPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { isSuperadmin, can } = useAccess();
  const pager = usePagination();
  const [creating, setCreating] = useState(false);

  const tenants = useQuery(
    () => organizationApi.listTenants({ page: pager.page, per_page: pager.perPage }),
    [pager.page, pager.perPage],
  );

  const columns: Column<Tenant>[] = [
    {
      key: 'slug',
      header: t('orgs.column.slug'),
      render: (o) => <span className="cf-mono">{o.slug}</span>,
    },
    { key: 'name', header: t('orgs.column.name'), render: (o) => <strong>{o.name}</strong> },
    {
      key: 'status',
      header: t('orgs.column.status'),
      render: (o) => (
        <Badge tone={tenantStatusTone(o.status)}>{tEnum('orgs.status', o.status)}</Badge>
      ),
    },
    {
      key: 'cell',
      header: t('orgs.column.cell'),
      render: (o) => <span className="cf-mono">{o.cell_id}</span>,
    },
    { key: 'created', header: t('common.createdAt'), render: (o) => formatDateTime(o.created_at) },
  ];

  return (
    <div>
      <PageHeader
        title={t('orgs.title')}
        description={t('orgs.subtitle')}
        actions={
          isSuperadmin && can(...PERMISSIONS.tenants.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('orgs.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <DataTable
          columns={columns}
          rows={tenants.data?.items ?? []}
          rowKey={(o) => o.id}
          loading={tenants.loading}
          error={tenants.error}
          onRetry={tenants.reload}
          empty={{ title: t('orgs.empty') }}
          onRowClick={(o) => navigate(paths.organization(o.id))}
          pagination={{
            page: tenants.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: tenants.data?.total ?? 0,
            totalPages: tenants.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <OrganizationForm
          open
          onClose={() => setCreating(false)}
          onSubmit={async (input) => {
            const { data } = await organizationApi.createTenant(input);
            toast.success(t('orgs.created'));
            setCreating(false);
            navigate(paths.organization(data.id));
          }}
        />
      ) : null}
    </div>
  );
}
