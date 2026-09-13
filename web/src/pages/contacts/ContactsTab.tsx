import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { CONTACT_STATUSES, contactsApi, type Contact, type ContactStatus } from '@/api/contacts';
import { PICKER_PAGE_SIZE } from '@/api/paging';
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
  Select,
  useToast,
  type Column,
} from '@/design/components';
import { IconPlus } from '@/design/icons';
import { formatDateTime, fullName } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { AddToListDialog } from './AddToListDialog';
import { ContactForm } from './ContactForm';
import { selectionColumn, useRowSelection } from './ContactSelection';
import { ConsentBadge, ContactStatusBadge } from './contactStatus';

const SEARCH_DEBOUNCE_MS = 300;
const VISIBLE_TAGS = 3;

export function ContactsTab() {
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const pager = usePagination();
  const selection = useRowSelection();
  const [searchInput, setSearchInput] = useState('');
  const [tagInput, setTagInput] = useState('');
  const [search, setSearch] = useState('');
  const [tag, setTag] = useState('');
  const [status, setStatus] = useState<ContactStatus | ''>('');
  const [listId, setListId] = useState('');
  const [creating, setCreating] = useState(false);
  const [addingToList, setAddingToList] = useState(false);

  const canReadLists = can(...PERMISSIONS.contactLists.read);
  const canUpdateLists = can(...PERMISSIONS.contactLists.update);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setSearch(searchInput.trim());
      setTag(tagInput.trim());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable; solo el texto escrito dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchInput, tagInput]);

  const lists = useQuery(
    () =>
      canReadLists
        ? contactsApi.listLists({ page: 1, per_page: PICKER_PAGE_SIZE })
        : Promise.resolve(null),
    [canReadLists],
  );

  const contacts = useQuery(
    () =>
      contactsApi.list({
        page: pager.page,
        per_page: pager.perPage,
        search: search || undefined,
        status: status || undefined,
        tag: tag || undefined,
        list_id: listId || undefined,
      }),
    [pager.page, pager.perPage, search, status, tag, listId],
  );

  const { clear } = selection;
  useEffect(() => clear(), [contacts.data, clear]);

  const rows = contacts.data?.items ?? [];
  const columns: Column<Contact>[] = [
    ...(canUpdateLists
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
    {
      key: 'tags',
      header: t('contacts.column.tags'),
      render: (c) => {
        const tags = c.tags ?? [];
        if (!tags.length) return t('common.dash');
        return (
          <span className="cf-inline-list">
            {tags.slice(0, VISIBLE_TAGS).map((tagName) => (
              <Badge key={tagName}>{tagName}</Badge>
            ))}
            {tags.length > VISIBLE_TAGS ? (
              <Badge>{t('contacts.moreTags', { n: tags.length - VISIBLE_TAGS })}</Badge>
            ) : null}
          </span>
        );
      },
    },
    {
      key: 'source',
      header: t('contacts.column.source'),
      render: (c) => tEnum('contacts.source', c.source),
    },
    { key: 'created', header: t('common.createdAt'), render: (c) => formatDateTime(c.created_at) },
  ];

  return (
    <Card
      flush
      title={t('contacts.list.title')}
      description={t('contacts.list.description')}
      actions={
        can(...PERMISSIONS.contacts.create) ? (
          <Button variant="primary" icon={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('contacts.new')}
          </Button>
        ) : null
      }
    >
      <div className="cf-toolbar">
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="contacts-search">
            {t('common.search')}
          </label>
          <Input
            id="contacts-search"
            type="search"
            placeholder={t('contacts.searchPlaceholder')}
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
          />
        </div>
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="contacts-status">
            {t('common.status')}
          </label>
          <Select
            id="contacts-status"
            placeholder={t('common.all')}
            options={CONTACT_STATUSES.map((s) => ({
              value: s,
              label: tEnum('contacts.status', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as ContactStatus | '');
              pager.reset();
            }}
          />
        </div>
        <div className="cf-field">
          <label className="cf-field__label" htmlFor="contacts-tag">
            {t('contacts.column.tag')}
          </label>
          <Input
            id="contacts-tag"
            placeholder={t('contacts.tagPlaceholder')}
            value={tagInput}
            onChange={(e) => setTagInput(e.target.value)}
          />
        </div>
        {canReadLists ? (
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="contacts-list">
              {t('contacts.column.list')}
            </label>
            <Select
              id="contacts-list"
              placeholder={t('common.all')}
              options={(lists.data?.items ?? []).map((l) => ({ value: l.id, label: l.name }))}
              value={listId}
              onChange={(e) => {
                setListId(e.target.value);
                pager.reset();
              }}
            />
          </div>
        ) : null}
      </div>
      {selection.selected.size > 0 ? (
        <div className="cf-bulkbar" role="region" aria-label={t('common.bulkActions')}>
          <span>{t('common.selectedCount', { n: selection.selected.size })}</span>
          <div className="cf-inline">
            <Button size="sm" variant="primary" onClick={() => setAddingToList(true)}>
              {t('contacts.lists.addSelected')}
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
        loading={contacts.loading}
        error={contacts.error}
        onRetry={contacts.reload}
        empty={{ title: t('contacts.empty'), description: t('contacts.emptyDescription') }}
        onRowClick={(c) => navigate(paths.contact(c.id))}
        pagination={{
          page: contacts.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: contacts.data?.total ?? 0,
          totalPages: contacts.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {creating ? (
        <ContactForm
          contact={null}
          onClose={() => setCreating(false)}
          onSaved={(created) => {
            toast.success(t('contacts.created'));
            setCreating(false);
            navigate(paths.contact(created.id));
          }}
        />
      ) : null}
      {addingToList ? (
        <AddToListDialog
          lists={lists.data?.items ?? []}
          contactIds={[...selection.selected]}
          onClose={() => setAddingToList(false)}
          onDone={() => {
            setAddingToList(false);
            selection.clear();
            lists.reload();
          }}
        />
      ) : null}
    </Card>
  );
}
