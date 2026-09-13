import type { DoiStatus, RunStatus, WorkflowStatus } from '@/api/automations';
import { Badge, type BadgeTone } from '@/design/components';
import { tEnum } from '@/i18n';

export function workflowStatusTone(status: WorkflowStatus): BadgeTone {
  if (status === 'active') return 'success';
  if (status === 'paused') return 'warning';
  if (status === 'draft') return 'info';
  return 'neutral';
}

export function WorkflowStatusBadge({ status }: { status: WorkflowStatus }) {
  return <Badge tone={workflowStatusTone(status)}>{tEnum('automations.status', status)}</Badge>;
}

export function runStatusTone(status: RunStatus): BadgeTone {
  if (status === 'running') return 'accent';
  if (status === 'waiting') return 'info';
  if (status === 'completed') return 'success';
  if (status === 'failed') return 'danger';
  if (status === 'skipped') return 'warning';
  return 'neutral';
}

export function RunStatusBadge({ status }: { status: RunStatus }) {
  return <Badge tone={runStatusTone(status)}>{tEnum('automations.runStatus', status)}</Badge>;
}

export function doiStatusTone(status: DoiStatus): BadgeTone {
  if (status === 'sent') return 'success';
  if (status === 'failed') return 'danger';
  if (status === 'pending') return 'info';
  return 'neutral';
}

export function DoiStatusBadge({ status }: { status: DoiStatus }) {
  return <Badge tone={doiStatusTone(status)}>{tEnum('automations.doiStatus', status)}</Badge>;
}
