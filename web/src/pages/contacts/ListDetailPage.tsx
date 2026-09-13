import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { contactsApi, type Contact } from '@/api/contacts';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  ErrorState,
  Input,
  PageHeader,
  Skeleton,
  useToast,
  type Column,
} from '@/design/components';
import { IconEdit, IconPlus, IconTrash } from '@/design/icons';
import { fullName } from '@/lib/format';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { AddMembersDialog } from './AddMembersDialog';
import { selectionColumn, useRowSelection } from './ContactSelection';
import { ConsentBadge, ContactStatusBadge } from './contactStatus';
import { ListForm } from './ListsTab';

const SEARCH_DEBOUNCE_MS = 300;
type Dialog = 'edit' | 'delete' | 'add' | 'remove' | null;

export default function ListDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const selection = useRowSelection();
  const [dialog, setDialog] = useState<Dialog>(null);
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const format = new Intl.NumberFormat(getLocale());

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput]);

  const list = useQuery(async () => (await contactsApi.getList(id)).data, [id]);
  const canReadContacts = can(...PERMISSIONS.contacts.read);
  const members = useQuery(
    () =>
      canReadContacts
        ? contactsApi.list({
            list_id: id,
            page: pager.page,
            per_page: pager.perPage,
            search: search || undefined,
          })
        : Promise.resolve(null),
    [id, pager.page, pager.perPage, search, canReadContacts],
  );
  const { clear } = selection;
  useEffect(() => clear(), [members.data, clear]);

  if (list.error) {
    return (
      <div>
        <PageHeader
          title={t('contacts.lists.title')}
          back={{ to: paths.contacts, label: t('nav.contacts') }}
        />
        <Card>
          <ErrorState
            error={list.error}
            title={t('contacts.lists.notFound')}
            onRetry={list.reload}
          />
        </Card>
      </div>
    );
  }
  if (!list.data) {
    return (
      <Card>
        <Skeleton lines={5} />
      </Card>
    );
  }

  const l = list.data;
  const canUpdate = can(...PERMISSIONS.contactLists.update);
  const close = () => setDialog(null);
  const refresh = () => {
    list.reload();
    members.reload();
  };
  const rows = members.data?.items ?? [];

  const columns: Column<Contact>[] = [
    ...(canUpdate
      ? [
          selectionColumn(
            rows,
            (c) => c.id,
            (c) => c.email,
            selection,
          ),
        ]
      : []),
    {
      key: 'email',
      header: t('common.email'),
      render: (c) => (
        <div className="cf-cell-stack">
          <strong className="cf-mono cf-break">{c.email}</strong>
          {fullName(c.first_name, c.last_name) ? (
            <span className="cf-text-muted cf-text-sm">{fullName(c.first_name, c.last_name)}</span>
          ) : null}
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (c) => <ContactStatusBadge status={c.status} />,
    },
    {
      key: 'consent',
      header: t('contacts.column.consent'),
      render: (c) => <ConsentBadge status={c.consent_status} />,
    },
  ];

  return (
    <div>
      <PageHeader
        title={l.name}
        description={t('contacts.lists.memberCount', { n: format.format(l.member_count) })}
        back={{ to: `${paths.contacts}?tab=lists`, label: t('contacts.tab.lists') }}
        actions={
          <>
            {canUpdate && canReadContacts ? (
              <Button
                variant="primary"
                icon={<IconPlus size={16} />}
                onClick={() => setDialog('add')}
              >
                {t('contacts.lists.addMembers')}
              </Button>
            ) : null}
            {canUpdate ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.contactLists.delete) ? (
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
      {l.description ? (
        <p className="cf-text-secondary" style={{ marginBottom: 'var(--cf-space-4)' }}>
          {l.description}
        </p>
      ) : null}
      <Card flush title={t('contacts.lists.members')}>
        {canReadContacts ? (
          <>
            <div className="cf-toolbar">
              <div className="cf-field">
                <label className="cf-field__label" htmlFor="list-members-search">
                  {t('common.search')}
                </label>
                <Input
                  id="list-members-search"
                  type="search"
                  placeholder={t('contacts.searchPlaceholder')}
                  value={searchInput}
                  onChange={(e) => setSearchInput(e.target.value)}
                />
              </div>
            </div>
            {selection.selected.size > 0 ? (
              <div className="cf-bulkbar" role="region" aria-label={t('common.bulkActions')}>
                <span>{t('common.selectedCount', { n: selection.selected.size })}</span>
                <div className="cf-inline">
                  <Button size="sm" variant="danger" onClick={() => setDialog('remove')}>
                    {t('contacts.lists.removeSelected')}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={selection.clear}>
                    {t('common.clearSelection')}
                  </Button>
                </div>
              </div>
            ) : null}
            <DataTable
              columns={columns}
              rows={rows}
              rowKey={(c) => c.id}
              loading={members.loading}
              error={members.error}
              onRetry={members.reload}
              empty={{ title: t('contacts.lists.noMembers') }}
              onRowClick={(c) => navigate(paths.contact(c.id))}
              pagination={{
                page: members.data?.page ?? pager.page,
                perPage: pager.perPage,
                total: members.data?.total ?? 0,
                totalPages: members.data?.totalPages ?? 0,
                onPageChange: pager.setPage,
              }}
            />
          </>
        ) : (
          <div className="cf-table__state">
            <span className="cf-text-muted">{t('common.missingPermission')}</span>
          </div>
        )}
      </Card>

      {dialog === 'edit' ? (
        <ListForm
          item={l}
          onClose={close}
          onSaved={() => {
            toast.success(t('contacts.lists.updated'));
            close();
            list.reload();
          }}
        />
      ) : null}
      {dialog === 'add' ? (
        <AddMembersDialog
          listId={l.id}
          onClose={close}
          onDone={() => {
            close();
            refresh();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'remove'}
        title={t('contacts.lists.removeSelected')}
        message={t('contacts.lists.removeConfirm', { n: selection.selected.size, name: l.name })}
        confirmLabel={t('contacts.lists.removeSelected')}
        danger
        onCancel={close}
        onConfirm={async () => {
          const { data } = await contactsApi.removeMembers(l.id, [...selection.selected]);
          toast.success(
            t('contacts.lists.removedResult', { removed: data.removed, ignored: data.ignored }),
          );
          close();
          refresh();
        }}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('contacts.lists.delete')}
        message={t('contacts.lists.deleteConfirm', { name: l.name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await contactsApi.deleteList(l.id);
          toast.success(t('contacts.lists.deleted'));
          navigate(`${paths.contacts}?tab=lists`, { replace: true });
        }}
      />
    </div>
  );
}
