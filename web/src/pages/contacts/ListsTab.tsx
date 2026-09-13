import { useState } from 'react';
import { Link } from 'react-router-dom';
import { contactsApi, type ContactList } from '@/api/contacts';
import type { PageQuery } from '@/api/types';
import { PERMISSIONS } from '@/access/permissions';
import { useAction } from '@/hooks/useAction';
import { FormField, Input, Textarea, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { rules, validateField } from '@/lib/validate';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';

const loadLists = (query: PageQuery) => contactsApi.listLists(query);

export function ListsTab() {
  const format = new Intl.NumberFormat(getLocale());
  const columns: Column<ContactList>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (l) => (
        <div className="cf-cell-stack">
          <Link to={paths.contactList(l.id)}>
            <strong>{l.name}</strong>
          </Link>
          {l.description ? <span className="cf-text-muted cf-text-sm">{l.description}</span> : null}
        </div>
      ),
    },
    {
      key: 'members',
      header: t('contacts.lists.members'),
      align: 'right',
      render: (l) => format.format(l.member_count),
    },
    { key: 'updated', header: t('common.updatedAt'), render: (l) => formatDateTime(l.updated_at) },
  ];

  return (
    <ResourceTab<ContactList>
      permissions={PERMISSIONS.contactLists}
      load={loadLists}
      remove={contactsApi.deleteList}
      columns={columns}
      Form={ListForm}
      texts={{
        title: t('contacts.lists.title'),
        description: t('contacts.lists.description'),
        create: t('contacts.lists.new'),
        empty: t('contacts.lists.empty'),
        created: t('contacts.lists.created'),
        updated: t('contacts.lists.updated'),
        deleted: t('contacts.lists.deleted'),
        deleteTitle: t('contacts.lists.delete'),
        deleteConfirm: (l) => t('contacts.lists.deleteConfirm', { name: l.name }),
      }}
    />
  );
}

export function ListForm({ item, onClose, onSaved }: ResourceFormProps<ContactList>) {
  const [name, setName] = useState(item?.name ?? '');
  const [description, setDescription] = useState(item?.description ?? '');
  const [error, setError] = useState<string | null>(null);

  const action = useAction(async () => {
    const body = { name: name.trim(), description: description.trim() };
    if (item) await contactsApi.updateList(item.id, body);
    else await contactsApi.createList(body);
    onSaved();
  });

  return (
    <FormModal
      id="contact-list-form"
      title={item ? t('contacts.lists.editTitle') : t('contacts.lists.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        const next = validateField(name, rules.required);
        setError(next);
        if (!next) await action.run();
      }}
    >
      <FormField label={t('common.name')} htmlFor="contact-list-name" required error={error}>
        <Input
          id="contact-list-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          invalid={Boolean(error)}
        />
      </FormField>
      <FormField label={t('common.description')} htmlFor="contact-list-description">
        <Textarea
          id="contact-list-description"
          rows={3}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
    </FormModal>
  );
}
