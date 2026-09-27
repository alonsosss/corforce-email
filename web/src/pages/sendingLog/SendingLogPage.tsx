import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useTabParam } from '@/hooks/useTabParam';
import { EmptyState, PageHeader, Tabs } from '@/design/components';
import { t } from '@/i18n';
import { CorporateTab } from './CorporateTab';
import { TransactionalTab } from './TransactionalTab';

type TabId = 'transactional' | 'corporate';

/**
 * Envios de la empresa y su resultado (docs/Plan_Registro_de_Envios.md): el correo transaccional
 * (API y relay, con SES) y el corporativo (buzones, con Postfix), cada uno en su pestana y con su
 * permiso.
 */
export default function SendingLogPage() {
  const { can } = useAccess();
  const tabs: { id: TabId; label: string }[] = [
    ...(can(...PERMISSIONS.transactionalMessages.read)
      ? [{ id: 'transactional' as const, label: t('sendingLog.tab.transactional') }]
      : []),
    ...(can(...PERMISSIONS.deliveryLog.read)
      ? [{ id: 'corporate' as const, label: t('sendingLog.tab.corporate') }]
      : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    tabs[0]?.id ?? 'transactional',
  );

  return (
    <div>
      <PageHeader title={t('sendingLog.title')} description={t('sendingLog.subtitle')} />
      {tabs.length === 0 ? (
        <EmptyState title={t('sendingLog.noAccess')} />
      ) : (
        <>
          <Tabs items={tabs} value={tab} onChange={setTab} label={t('sendingLog.title')} />
          {tab === 'transactional' ? <TransactionalTab /> : null}
          {tab === 'corporate' ? <CorporateTab /> : null}
        </>
      )}
    </div>
  );
}
