import { Link } from 'react-router-dom';
import { contactsApi } from '@/api/contacts';
import { formsApi, type SubscriptionForm } from '@/api/forms';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import type { PageQuery } from '@/api/types';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import { Badge, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { ResourceTab } from '@/pages/shared/ResourceTab';
import { formStatusTone } from './formStatus';
import { SubscriptionFormModal } from './SubscriptionFormModal';

const loadForms = (query: PageQuery) => formsApi.list(query);

export function FormsTab() {
  const { can } = useAccess();
  const canReadLists = can(...PERMISSIONS.contactLists.read);
  const lists = useQuery(
    async () =>
      canReadLists
        ? new Map(
            (await contactsApi.listLists({ page: 1, per_page: PICKER_PAGE_SIZE })).items.map(
              (l) => [l.id, l.name],
            ),
          )
        : null,
    [canReadLists],
  );

  const columns: Column<SubscriptionForm>[] = [
    {
      key: 'name',
      header: t('common.name'),
      render: (f) => (
        <Link to={paths.contactForm(f.id)}>
          <strong>{f.name}</strong>
        </Link>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (f) => (
        <Badge tone={formStatusTone(f.status)}>{tEnum('contacts.forms.status', f.status)}</Badge>
      ),
    },
    {
      key: 'list',
      header: t('contacts.forms.list'),
      render: (f) => lists.data?.get(f.list_id) ?? <span className="cf-mono">{f.list_id}</span>,
    },
    { key: 'updated', header: t('common.updatedAt'), render: (f) => formatDateTime(f.updated_at) },
  ];

  return (
    <ResourceTab<SubscriptionForm>
      permissions={PERMISSIONS.subscriptionForms}
      load={loadForms}
      remove={formsApi.remove}
      columns={columns}
      Form={SubscriptionFormModal}
      texts={{
        title: t('contacts.forms.title'),
        description: t('contacts.forms.description'),
        create: t('contacts.forms.new'),
        empty: t('contacts.forms.empty'),
        created: t('contacts.forms.created'),
        updated: t('contacts.forms.updated'),
        deleted: t('contacts.forms.deleted'),
        deleteTitle: t('contacts.forms.delete'),
        deleteConfirm: (f) => t('contacts.forms.deleteConfirm', { name: f.name }),
      }}
    />
  );
}
