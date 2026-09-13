import type { BatchStatus, CampaignStatus } from '@/api/campaigns';
import { Badge, type BadgeTone } from '@/design/components';
import { tEnum } from '@/i18n';

export function campaignStatusTone(status: CampaignStatus | string): BadgeTone {
  switch (status) {
    case 'scheduled':
      return 'info';
    case 'sending':
      return 'accent';
    case 'paused':
      return 'warning';
    case 'completed':
      return 'success';
    case 'failed':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function CampaignStatusBadge({ status }: { status: CampaignStatus | string }) {
  return <Badge tone={campaignStatusTone(status)}>{tEnum('campaigns.status', status)}</Badge>;
}

export function BatchStatusBadge({ status }: { status: BatchStatus }) {
  const tone: BadgeTone =
    status === 'delivered' ? 'success' : status === 'failed' ? 'danger' : 'info';
  return <Badge tone={tone}>{tEnum('campaigns.batchStatus', status)}</Badge>;
}
