import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useTabParam } from '@/hooks/useTabParam';
import { Card, EmptyState, PageHeader, Tabs } from '@/design/components';
import { t } from '@/i18n';
import { AttributesTab } from './AttributesTab';
import { ContactsTab } from './ContactsTab';
import { ImportTab } from './ImportTab';
import { ListsTab } from './ListsTab';

type TabId = 'contacts' | 'lists' | 'attributes' | 'imports';

export default function ContactsPage() {
  const { can } = useAccess();
  const tabs: { id: TabId; label: string }[] = [
    ...(can(...PERMISSIONS.contacts.read)
      ? [{ id: 'contacts' as const, label: t('contacts.tab.contacts') }]
      : []),
    ...(can(...PERMISSIONS.contactLists.read)
      ? [{ id: 'lists' as const, label: t('contacts.tab.lists') }]
      : []),
    ...(can(...PERMISSIONS.contactAttributes.read)
      ? [{ id: 'attributes' as const, label: t('contacts.tab.attributes') }]
      : []),
    ...(can(...PERMISSIONS.contacts.import)
      ? [{ id: 'imports' as const, label: t('contacts.tab.imports') }]
      : []),
  ];
  const available = tabs.map((item) => item.id);
  const [tab, setTab] = useTabParam(available, 'contacts');
  const show = (id: TabId) => tab === id && available.includes(id);

  return (
    <div>
      <PageHeader title={t('contacts.title')} description={t('contacts.subtitle')} />
      {tabs.length === 0 ? (
        <Card>
          <EmptyState title={t('contacts.noAccess')} />
        </Card>
      ) : (
        <Tabs items={tabs} value={tab} onChange={setTab} label={t('contacts.title')} />
      )}
      {show('contacts') ? <ContactsTab /> : null}
      {show('lists') ? <ListsTab /> : null}
      {show('attributes') ? <AttributesTab /> : null}
      {show('imports') ? <ImportTab /> : null}
    </div>
  );
}
