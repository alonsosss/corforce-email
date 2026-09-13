import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { identityApi, type User } from '@/api/identity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  Input,
  PageHeader,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus, IconSearch } from '@/design/icons';
import { formatDateTime, fullName } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { UserForm } from './UserForm';

const SEARCH_DEBOUNCE_MS = 300;

export function userStatusTone(status: User['status']): 'success' | 'warning' | 'neutral' {
  if (status === 'active') return 'success';
  if (status === 'locked') return 'warning';
  return 'neutral';
}

export default function UsersPage() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable (useCallback); solo el texto dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const users = useQuery(
    () =>
      identityApi.listUsers({
        page: pager.page,
        per_page: pager.perPage,
        search: search || undefined,
      }),
    [pager.page, pager.perPage, search],
  );

  const columns: Column<User>[] = [
    {
      key: 'name',
      header: t('users.column.name'),
      render: (u) => fullName(u.first_name, u.last_name, u.email),
    },
    { key: 'email', header: t('users.column.email'), render: (u) => u.email },
    {
      key: 'status',
      header: t('users.column.status'),
      render: (u) => (
        <Badge tone={userStatusTone(u.status)}>{tEnum('users.status', u.status)}</Badge>
      ),
    },
    {
      key: 'mfa',
      header: t('users.column.mfa'),
      render: (u) =>
        u.mfa_enabled ? <Badge tone="info">{t('users.mfaOn')}</Badge> : t('users.mfaOff'),
    },
    {
      key: 'last',
      header: t('users.column.lastLogin'),
      render: (u) => formatDateTime(u.last_login_at),
    },
  ];

  return (
    <div>
      <PageHeader
        title={t('users.title')}
        description={t('users.subtitle')}
        actions={
          can(...PERMISSIONS.users.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => setCreating(true)}
            >
              {t('users.new')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <div className="cf-toolbar">
          <div className="cf-field" style={{ maxWidth: 360 }}>
            <div className="cf-input-group">
              <Input
                aria-label={t('common.search')}
                placeholder={t('users.searchPlaceholder')}
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
              />
              <span className="cf-input-group__addon" style={{ right: 10 }}>
                <IconSearch size={16} />
              </span>
            </div>
          </div>
        </div>
        <DataTable
          columns={columns}
          rows={users.data?.items ?? []}
          rowKey={(u) => u.id}
          loading={users.loading}
          error={users.error}
          onRetry={users.reload}
          empty={{ title: t('users.empty'), description: t('users.emptyDescription') }}
          onRowClick={(u) => navigate(paths.user(u.id))}
          pagination={{
            page: users.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: users.data?.total ?? 0,
            totalPages: users.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {creating ? (
        <UserForm
          mode="create"
          open
          onClose={() => setCreating(false)}
          onSubmit={async (input) => {
            const { data } = await identityApi.createUser(input);
            toast.success(t('users.created'));
            setCreating(false);
            navigate(paths.user(data.id));
          }}
        />
      ) : null}
    </div>
  );
}
