import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { accessApi } from '@/api/access';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconEdit, IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { RoleForm } from './RoleForm';
import { PermissionMatrix } from './PermissionMatrix';

export default function RoleDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const role = useQuery(async () => (await accessApi.getRole(id)).data, [id]);
  const catalog = useQuery(async () => (await accessApi.listPermissions()).data, []);
  const granted = useQuery(async () => (await accessApi.getRolePermissions(id)).data, [id]);

  useEffect(() => {
    if (granted.data) setSelected(new Set(granted.data.map((p) => p.id)));
  }, [granted.data]);

  const save = useAction(async () => {
    await accessApi.setRolePermissions(id, [...selected]);
    granted.reload();
  });

  if (role.error) {
    return (
      <div>
        <PageHeader title={t('roles.title')} back={{ to: paths.roles, label: t('nav.roles') }} />
        <Card>
          <ErrorState error={role.error} title={t('roles.notFound')} onRetry={role.reload} />
        </Card>
      </div>
    );
  }
  if (!role.data) {
    return (
      <Card>
        <Skeleton lines={5} />
      </Card>
    );
  }

  const r = role.data;
  const editable = !r.is_system && can(...PERMISSIONS.roles.update);
  const deletable = !r.is_system && can(...PERMISSIONS.roles.delete);
  const dirty =
    granted.data !== null &&
    (selected.size !== granted.data.length || granted.data.some((p) => !selected.has(p.id)));

  return (
    <div className="cf-stack">
      <PageHeader
        title={r.name}
        description={r.description || undefined}
        back={{ to: paths.roles, label: t('nav.roles') }}
        actions={
          <>
            {editable ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setEditing(true)}>
                {t('common.edit')}
              </Button>
            ) : null}
            {deletable ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDeleting(true)}
              >
                {t('roles.delete')}
              </Button>
            ) : null}
          </>
        }
      />
      <Card>
        <DescriptionList
          items={[
            {
              label: t('common.status'),
              value: r.is_system ? (
                <Badge tone="accent">{t('roles.system')}</Badge>
              ) : (
                <Badge>{t('roles.custom')}</Badge>
              ),
            },
            { label: t('common.createdAt'), value: formatDateTime(r.created_at) },
            { label: t('common.updatedAt'), value: formatDateTime(r.updated_at) },
            { label: t('common.id'), value: <span className="cf-mono">{r.id}</span> },
          ]}
        />
      </Card>
      <Card
        title={t('roles.permissions.title')}
        description={
          r.is_system ? t('roles.permissions.systemLocked') : t('roles.permissions.description')
        }
        flush
        actions={
          editable ? (
            <>
              <span className="cf-text-secondary cf-text-sm" style={{ alignSelf: 'center' }}>
                {t('roles.permissions.selected', { n: selected.size })}
              </span>
              <Button
                variant="primary"
                disabled={!dirty}
                loading={save.busy}
                onClick={() => {
                  void save.run().then((ok) => {
                    if (ok) toast.success(t('roles.permissions.saved'));
                  });
                }}
              >
                {t('roles.permissions.save')}
              </Button>
            </>
          ) : null
        }
      >
        {catalog.error || granted.error ? (
          <ErrorState
            error={catalog.error ?? granted.error}
            onRetry={() => {
              catalog.reload();
              granted.reload();
            }}
          />
        ) : !catalog.data || !granted.data ? (
          <div className="cf-card__body">
            <Skeleton lines={6} />
          </div>
        ) : catalog.data.length === 0 ? (
          <EmptyState title={t('roles.permissions.empty')} />
        ) : (
          <PermissionMatrix
            catalog={catalog.data}
            selected={selected}
            disabled={!editable}
            onChange={setSelected}
          />
        )}
        {save.error ? (
          <div className="cf-card__body">
            <div className="cf-form__error" role="alert">
              {errorMessage(save.error)}
            </div>
          </div>
        ) : null}
      </Card>

      {editing ? (
        <RoleForm
          open
          role={r}
          onClose={() => setEditing(false)}
          onSubmit={async (input) => {
            const { data } = await accessApi.updateRole(r.id, input);
            role.setData(data);
            toast.success(t('roles.updated'));
            setEditing(false);
          }}
        />
      ) : null}
      <ConfirmDialog
        open={deleting}
        title={t('roles.delete')}
        message={t('roles.deleteConfirm', { name: r.name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setDeleting(false)}
        onConfirm={async () => {
          await accessApi.deleteRole(r.id);
          toast.success(t('roles.deleted'));
          navigate(paths.roles, { replace: true });
        }}
      />
    </div>
  );
}
