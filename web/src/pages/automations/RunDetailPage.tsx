import { Link, useParams } from 'react-router-dom';
import { automationsApi } from '@/api/automations';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  DescriptionList,
  ErrorState,
  PageHeader,
  Skeleton,
} from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { describeRunError } from './automationReason';
import { RunStatusBadge } from './automationStatus';
import { stepLabel } from './RunsCard';

/** Detalle de una ejecucion: en que paso esta, cuando vuelve a correr y por que termino. */
export default function RunDetailPage() {
  const { id = '' } = useParams();
  const { can } = useAccess();
  const canRuns = can(...PERMISSIONS.automationRuns.read);
  const canWorkflows = can(...PERMISSIONS.automationWorkflows.read);
  const canContacts = can(...PERMISSIONS.contacts.read);
  const run = useQuery(
    async () => (canRuns ? (await automationsApi.getRun(id)).data : null),
    [id, canRuns],
  );
  const workflowId = run.data?.workflow_id ?? '';
  const workflow = useQuery(
    async () =>
      canWorkflows && workflowId ? (await automationsApi.getWorkflow(workflowId)).data : null,
    [canWorkflows, workflowId],
  );

  if (!canRuns) return <MissingPermission title={t('automations.run.title')} />;
  const back = workflow.data
    ? { to: `${paths.automation(workflow.data.id)}?tab=runs`, label: workflow.data.name }
    : { to: paths.automations, label: t('nav.automations') };

  if (run.error) {
    return (
      <div>
        <PageHeader title={t('automations.run.title')} back={back} />
        <Card>
          <ErrorState
            error={run.error}
            title={t('automations.run.notFound')}
            onRetry={run.reload}
          />
        </Card>
      </div>
    );
  }
  if (!run.data) {
    return (
      <Card>
        <Skeleton lines={8} />
      </Card>
    );
  }

  const r = run.data;
  const reason = describeRunError(r.error_code, r.last_error);
  const format = new Intl.NumberFormat(getLocale());

  return (
    <div>
      <PageHeader
        title={t('automations.run.title')}
        description={<RunStatusBadge status={r.status} />}
        back={back}
        actions={
          <Button variant="ghost" icon={<IconRefresh size={16} />} onClick={run.reload}>
            {t('common.refresh')}
          </Button>
        }
      />
      <div className="cf-stack">
        {reason ? (
          <Alert
            tone={r.status === 'failed' ? 'danger' : 'info'}
            title={t('automations.run.reasonTitle')}
          >
            <span>{reason.text}</span>
            {reason.detail ? <span className="cf-text-sm cf-break">{reason.detail}</span> : null}
          </Alert>
        ) : null}
        <Card>
          <DescriptionList
            items={[
              {
                label: t('automations.run.workflow'),
                value: canWorkflows ? (
                  <Link to={paths.automation(r.workflow_id)}>
                    {workflow.data?.name ?? r.workflow_id}
                  </Link>
                ) : (
                  <span className="cf-mono">{r.workflow_id}</span>
                ),
              },
              {
                label: t('automations.runs.contact'),
                value: canContacts ? (
                  <Link to={paths.contact(r.contact_id)} className="cf-mono">
                    {r.contact_id}
                  </Link>
                ) : (
                  <span className="cf-mono">{r.contact_id}</span>
                ),
              },
              { label: t('common.status'), value: <RunStatusBadge status={r.status} /> },
              { label: t('automations.runs.step'), value: stepLabel(r, workflow.data) },
              {
                label: t('automations.runs.nextRun'),
                value: r.finished_at ? t('common.dash') : formatDateTime(r.next_run_at),
              },
              { label: t('automations.runs.attempts'), value: format.format(r.attempts) },
              {
                label: t('automations.run.finishedAt'),
                value: r.finished_at ? formatDateTime(r.finished_at) : t('common.dash'),
              },
              {
                label: t('automations.run.triggerEvent'),
                value: <span className="cf-mono cf-break">{r.trigger_event_id}</span>,
              },
              { label: t('common.createdAt'), value: formatDateTime(r.created_at) },
              { label: t('common.updatedAt'), value: formatDateTime(r.updated_at) },
              { label: t('common.id'), value: <span className="cf-mono">{r.id}</span> },
            ]}
          />
        </Card>
      </div>
    </div>
  );
}
