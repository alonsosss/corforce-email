import { useNavigate } from 'react-router-dom';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useTabParam } from '@/hooks/useTabParam';
import { Button, PageHeader, Tabs } from '@/design/components';
import { IconPlus } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { DoubleOptInTab } from './DoubleOptInTab';
import { WorkflowsTab } from './WorkflowsTab';

type TabId = 'workflows' | 'doubleOptIn';

/** Flujos de automatizacion y correo del doble opt-in, cada pestana con su permiso. */
export default function AutomationsPage() {
  const navigate = useNavigate();
  const { can } = useAccess();
  const tabs: { id: TabId; label: string }[] = [
    ...(can(...PERMISSIONS.automationWorkflows.read)
      ? [{ id: 'workflows' as const, label: t('automations.tab.workflows') }]
      : []),
    ...(can(...PERMISSIONS.automationSettings.read)
      ? [{ id: 'doubleOptIn' as const, label: t('automations.tab.doubleOptIn') }]
      : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'workflows',
  );
  if (tabs.length === 0) {
    return (
      <MissingPermission title={t('automations.title')} description={t('automations.subtitle')} />
    );
  }
  return (
    <div>
      <PageHeader
        title={t('automations.title')}
        description={t('automations.subtitle')}
        actions={
          tab === 'workflows' && can(...PERMISSIONS.automationWorkflows.create) ? (
            <Button
              variant="primary"
              icon={<IconPlus size={16} />}
              onClick={() => navigate(paths.automationNew)}
            >
              {t('automations.new')}
            </Button>
          ) : null
        }
      />
      {tabs.length > 1 ? (
        <Tabs items={tabs} value={tab} onChange={setTab} label={t('automations.title')} />
      ) : null}
      {tab === 'workflows' ? <WorkflowsTab /> : <DoubleOptInTab />}
    </div>
  );
}
