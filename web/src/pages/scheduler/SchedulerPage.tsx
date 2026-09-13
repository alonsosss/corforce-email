import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useTabParam } from '@/hooks/useTabParam';
import { PageHeader, Tabs } from '@/design/components';
import { t } from '@/i18n';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { JobsTab } from './JobsTab';
import { TasksTab } from './TasksTab';

type TabId = 'jobs' | 'tasks';

export default function SchedulerPage() {
  const { can } = useAccess();
  const tabs: { id: TabId; label: string }[] = [
    ...(can(...PERMISSIONS.schedulerJobs.read)
      ? [{ id: 'jobs' as const, label: t('scheduler.tab.jobs') }]
      : []),
    ...(can(...PERMISSIONS.schedulerTasks.read)
      ? [{ id: 'tasks' as const, label: t('scheduler.tab.tasks') }]
      : []),
  ];
  const [tab, setTab] = useTabParam(
    tabs.map((item) => item.id),
    'jobs',
  );

  if (tabs.length === 0) {
    return <MissingPermission title={t('scheduler.title')} description={t('scheduler.subtitle')} />;
  }
  const visible = tabs.some((item) => item.id === tab) ? tab : null;

  return (
    <div>
      <PageHeader title={t('scheduler.title')} description={t('scheduler.subtitle')} />
      {tabs.length > 1 ? (
        <Tabs items={tabs} value={tab} onChange={setTab} label={t('scheduler.title')} />
      ) : null}
      {visible === 'jobs' ? <JobsTab /> : null}
      {visible === 'tasks' ? <TasksTab /> : null}
    </div>
  );
}
