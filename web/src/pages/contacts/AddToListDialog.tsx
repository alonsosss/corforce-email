import { useState } from 'react';
import { contactsApi, type ContactList } from '@/api/contacts';
import { useAction } from '@/hooks/useAction';
import { Alert, FormField, Select, useToast } from '@/design/components';
import { t } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';

/** Anade contactos a una lista: los que ya estaban o no existen se cuentan como ignorados. */
export function AddToListDialog({
  lists,
  contactIds,
  onClose,
  onDone,
}: {
  lists: readonly ContactList[];
  contactIds: string[];
  onClose: () => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const [listId, setListId] = useState('');
  const [error, setError] = useState<string | null>(null);

  const action = useAction(async (target: string) => {
    const { data } = await contactsApi.addMembers(target, contactIds);
    toast.success(t('contacts.lists.addedResult', { added: data.added, ignored: data.ignored }));
    onDone();
  });

  return (
    <FormModal
      id="contacts-add-to-list"
      title={t('contacts.lists.addSelected')}
      submitLabel={t('contacts.lists.add')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={async () => {
        setError(listId ? null : t('validation.required'));
        if (listId) await action.run(listId);
      }}
    >
      <p className="cf-text-secondary">
        {t('contacts.lists.addDescription', { n: contactIds.length })}
      </p>
      {lists.length === 0 ? <Alert tone="info">{t('contacts.lists.none')}</Alert> : null}
      <FormField label={t('contacts.column.list')} htmlFor="add-to-list" required error={error}>
        <Select
          id="add-to-list"
          placeholder={t('common.select')}
          options={lists.map((l) => ({ value: l.id, label: l.name }))}
          value={listId}
          onChange={(e) => setListId(e.target.value)}
          invalid={Boolean(error)}
        />
      </FormField>
    </FormModal>
  );
}
