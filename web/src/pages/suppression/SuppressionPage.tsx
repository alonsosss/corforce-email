import { suppressionMeta } from '@/api/suppression';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useResource } from '@/hooks/useResource';
import { useTabParam } from '@/hooks/useTabParam';
import { PageHeader, Tabs } from '@/design/components';
import { t } from '@/i18n';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { CheckTab } from './CheckTab';
import { EntriesTab } from './EntriesTab';
import { ImportTab } from './ImportTab';
import { StatsTab } from './StatsTab';

type TabId = 'entries' | 'import' | 'check' | 'stats';

export default function SuppressionPage() {
  const { can } = useAccess();
  const meta = useResource(suppressionMeta);
  const canRead = can(...PERMISSIONS.suppressionEntries.read);
  const tabs: { id: TabId; label: string }[] = [
    ...(canRead ? [{ id: 'entries' as const, label: t('suppression.tab.entries') }] : []),
    ...(can(...PERMISSIONS.suppressionEntries.import)
      ? [{ id: 'import' as const, label: t('suppression.tab.import') }]
      : []),
    ...(canRead ? [{ id: 'check' as const, label: t('suppression.tab.check') }] : []),
    ...(can(...PERMISSIONS.suppressionStats.read)
      ? [{ id: 'stats' as const, label: t('suppression.tab.stats') }]
      : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'entries',
  );

  return (
    <div>
      <PageHeader title={t('suppression.title')} description={t('suppression.subtitle')} />
      {tabs.length > 0 ? (
        <Tabs items={tabs} value={tab} onChange={setTab} label={t('suppression.title')} />
      ) : null}
      <ResourceGate resource={meta}>
        {(data) => (
          <>
            {tab === 'entries' && canRead ? <EntriesTab meta={data} /> : null}
            {tab === 'import' ? <ImportTab meta={data} /> : null}
            {tab === 'check' && canRead ? <CheckTab meta={data} /> : null}
            {tab === 'stats' ? <StatsTab meta={data} /> : null}
          </>
        )}
      </ResourceGate>
    </div>
  );
}
