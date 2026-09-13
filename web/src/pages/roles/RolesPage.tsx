import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { accessApi, type Role } from '@/api/access';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  PageHeader,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { RoleForm } from './RoleForm';

export default function RolesPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [creating, setCreating] = useState(false);
  const roles = useQuery(async () => (await accessApi.listRoles()).data, []);

  const columns: Column<Role>[] = [
    { key: 'name', header: t('common.name'), render: (r) => <strong>{r.name}</strong> },
    {
      key: 'description',
      header: t('common.description'),
      render: (r) => r.description || t('common.dash'),
    },
    {
      key: 'kind',
      header: t('common.status'),
      render: (r) =>
        r.is_system ? (
          <Badge tone="accent">{t('roles.system')}</Badge>
        ) : (
          <Badge>{t('roles.custom')}</Badge>
        ),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (r) => formatDateTime(r.updated_at) },
  ];

  return (
    <div>
      <PageHeader
        title={t('roles.title')}
        description={t('roles.subtitle')}
        actions={
          can(...PERMISSIONS.roles.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('roles.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <DataTable
          columns={columns}
          rows={roles.data ?? []}
          rowKey={(r) => r.id}
          loading={roles.loading}
          error={roles.error}
          onRetry={roles.reload}
          empty={{ title: t('roles.empty') }}
          onRowClick={(r) => navigate(paths.role(r.id))}
        />
      </Card>
      {creating ? (
        <RoleForm
          open
          onClose={() => setCreating(false)}
          onSubmit={async (input) => {
            const { data } = await accessApi.createRole(input);
            toast.success(t('roles.created'));
            setCreating(false);
            navigate(paths.role(data.id));
          }}
        />
      ) : null}
    </div>
  );
}
