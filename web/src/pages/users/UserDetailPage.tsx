import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { identityApi } from '@/api/identity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAuth } from '@/auth/useAuth';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  ErrorState,
  PageHeader,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconEdit, IconKey, IconTrash } from '@/design/icons';
import { formatDateTime, fullName } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { UserForm } from './UserForm';
import { UserRolesCard } from './UserRolesCard';
import { ResetPasswordDialog } from './ResetPasswordDialog';
import { userStatusTone } from './UsersPage';

type Dialog = 'edit' | 'deactivate' | 'activate' | 'delete' | 'reset' | null;

export default function UserDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const { userId: currentUserId } = useAuth();
  const [dialog, setDialog] = useState<Dialog>(null);

  const user = useQuery(async () => (await identityApi.getUser(id)).data, [id]);
  const close = () => setDialog(null);

  if (user.error) {
    return (
      <div>
        <PageHeader title={t('users.title')} back={{ to: paths.users, label: t('nav.users') }} />
        <Card>
          <ErrorState error={user.error} title={t('users.notFound')} onRetry={user.reload} />
        </Card>
      </div>
    );
  }
  if (!user.data) {
    return (
      <Card>
        <Skeleton lines={5} />
      </Card>
    );
  }

  const u = user.data;
  const name = fullName(u.first_name, u.last_name, u.email);
  const isSelf = u.id === currentUserId;
  const canUpdate = can(...PERMISSIONS.users.update);
  const canDelete = can(...PERMISSIONS.users.delete);

  return (
    <div className="cf-stack">
      <PageHeader
        title={name}
        description={u.email}
        back={{ to: paths.users, label: t('nav.users') }}
        actions={
          <>
            {canUpdate ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {canUpdate && !isSelf ? (
              u.status === 'inactive' ? (
                <Button onClick={() => setDialog('activate')}>{t('users.activate')}</Button>
              ) : (
                <Button onClick={() => setDialog('deactivate')}>{t('users.deactivate')}</Button>
              )
            ) : null}
            {canUpdate && !isSelf ? (
              <Button icon={<IconKey size={16} />} onClick={() => setDialog('reset')}>
                {t('users.resetPassword')}
              </Button>
            ) : null}
            {canDelete && !isSelf ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDialog('delete')}
              >
                {t('common.delete')}
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
              value: (
                <Badge tone={userStatusTone(u.status)}>{tEnum('users.status', u.status)}</Badge>
              ),
            },
            {
              label: t('users.column.mfa'),
              value: u.mfa_enabled ? (
                <Badge tone="info">{t('users.mfaOn')}</Badge>
              ) : (
                t('users.mfaOff')
              ),
            },
            { label: t('users.column.lastLogin'), value: formatDateTime(u.last_login_at) },
            { label: t('common.createdAt'), value: formatDateTime(u.created_at) },
            { label: t('common.updatedAt'), value: formatDateTime(u.updated_at) },
            { label: t('common.id'), value: <span className="cf-mono">{u.id}</span> },
          ]}
        />
      </Card>
      <UserRolesCard userId={u.id} />

      {dialog === 'edit' ? (
        <UserForm
          mode="edit"
          open
          user={u}
          onClose={close}
          onSubmit={async (input) => {
            const { data } = await identityApi.updateUser(u.id, input);
            user.setData(data);
            toast.success(t('users.updated'));
            close();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'deactivate'}
        title={t('users.deactivate')}
        message={t('users.deactivateConfirm', { name })}
        confirmLabel={t('users.deactivate')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await identityApi.deactivateUser(u.id);
          toast.success(t('users.deactivated'));
          close();
          user.reload();
        }}
      />
      <ConfirmDialog
        open={dialog === 'activate'}
        title={t('users.activate')}
        message={name}
        confirmLabel={t('users.activate')}
        onCancel={close}
        onConfirm={async () => {
          const { data } = await identityApi.updateUser(u.id, { status: 'active' });
          user.setData(data);
          toast.success(t('users.activated'));
          close();
        }}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('users.delete')}
        message={t('users.deleteConfirm', { name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await identityApi.deleteUser(u.id);
          toast.success(t('users.deleted'));
          navigate(paths.users, { replace: true });
        }}
      />
      {dialog === 'reset' ? (
        <ResetPasswordDialog
          open
          user={u}
          onClose={close}
          onDone={() => {
            toast.success(t('users.resetPasswordDone'));
            close();
          }}
        />
      ) : null}
    </div>
  );
}
