import { useEffect, useState } from 'react';
import { contactsApi, type Contact } from '@/api/contacts';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { DataTable, FormField, Input, useToast, type Column } from '@/design/components';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { selectionColumn, useRowSelection } from './ContactSelection';
import { ContactStatusBadge } from './contactStatus';

const SEARCH_DEBOUNCE_MS = 300;
const RESULTS_PER_PAGE = 20;

/** Busca contactos y anade los marcados a la lista. */
export function AddMembersDialog({
  listId,
  onClose,
  onDone,
}: {
  listId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const selection = useRowSelection();
  const [input, setInput] = useState('');
  const [search, setSearch] = useState('');
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const handle = window.setTimeout(() => setSearch(input.trim()), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
  }, [input]);

  const results = useQuery(
    () => contactsApi.list({ page: 1, per_page: RESULTS_PER_PAGE, search: search || undefined }),
    [search],
  );

  const action = useAction(async (ids: string[]) => {
    const { data } = await contactsApi.addMembers(listId, ids);
    toast.success(t('contacts.lists.addedResult', { added: data.added, ignored: data.ignored }));
    onDone();
  });

  const rows = results.data?.items ?? [];
  const columns: Column<Contact>[] = [
    selectionColumn(
      rows,
      (c) => c.id,
      (c) => c.email,
      selection,
    ),
    {
      key: 'email',
      header: t('common.email'),
      render: (c) => <span className="cf-mono cf-break">{c.email}</span>,
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (c) => <ContactStatusBadge status={c.status} />,
    },
  ];

  return (
    <FormModal
      id="list-add-members"
      title={t('contacts.lists.addMembers')}
      submitLabel={t('contacts.lists.addCount', { n: selection.selected.size })}
      busy={action.busy}
      error={action.error}
      size="lg"
      onClose={onClose}
      onSubmit={async () => {
        const ids = [...selection.selected];
        setError(ids.length ? null : t('contacts.lists.selectSome'));
        if (ids.length) await action.run(ids);
      }}
    >
      <FormField label={t('common.search')} htmlFor="list-add-search" error={error}>
        <Input
          id="list-add-search"
          type="search"
          placeholder={t('contacts.searchPlaceholder')}
          value={input}
          onChange={(e) => setInput(e.target.value)}
        />
      </FormField>
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        loading={results.loading}
        error={results.error}
        onRetry={results.reload}
        empty={{ title: t('contacts.empty') }}
      />
    </FormModal>
  );
}
