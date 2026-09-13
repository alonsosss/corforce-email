import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/campaigns/internal/adapters/http/handler.go (campaignResponse,
// batchResponse), domain/campaign.go, domain/stats.go y ports.BatchResult.

export type CampaignStatus =
  'draft' | 'scheduled' | 'sending' | 'paused' | 'completed' | 'cancelled' | 'failed';

// Espejo de domain.Statuses() para el filtro del listado. campaigns no publica aun su
// catalogo; cuando exista GET /campaigns/meta se toma de alli.
export const CAMPAIGN_STATUSES: readonly CampaignStatus[] = [
  'draft',
  'scheduled',
  'sending',
  'paused',
  'completed',
  'cancelled',
  'failed',
];

export interface Audience {
  list_ids: string[];
  segment_ids: string[];
  exclude_segment_ids: string[];
}

export interface CampaignCounters {
  targeted: number;
  accepted: number;
  suppressed: number;
  sent: number;
  delivered: number;
  bounced: number;
  complained: number;
  opened: number;
  clicked: number;
  unsubscribed: number;
  failed: number;
}

/** Fracciones decimales con cuatro cifras ("0.2512"), calculadas por el servicio. */
export interface CampaignRates {
  delivery_rate: string;
  open_rate: string;
  click_rate: string;
  bounce_rate: string;
  complaint_rate: string;
  unsubscribe_rate: string;
}

export interface CampaignStats extends CampaignCounters {
  rates: CampaignRates;
}

export interface Campaign {
  id: string;
  name: string;
  description: string;
  status: CampaignStatus;
  /** manual, o CODIGO: mensaje del vecino que la freno. */
  pause_reason: string;
  /** CODIGO: mensaje del rechazo que la cerro. */
  failure_reason: string;
  template_id: string;
  template_version: number | null;
  from_email: string;
  from_name: string;
  reply_to: string;
  audience: Audience;
  scheduled_at: string | null;
  started_at: string | null;
  completed_at: string | null;
  resume_after: string | null;
  created_by: string;
  created_at: string;
  updated_at: string;
  /** Solo llega a quien tiene campaigns/stats/read. */
  stats?: CampaignStats;
}

export type BatchStatus = 'pending' | 'delivered' | 'failed';

export interface CampaignBatch {
  id: string;
  seq: number;
  status: BatchStatus;
  recipients: number;
  accepted: number;
  suppressed: number;
  attempts: number;
  last_error: string;
  created_at: string;
  updated_at: string;
}

export interface TestSendResult {
  accepted: number;
  suppressed: { email: string; reason: string }[] | null;
  message_ids: string[] | null;
}

export interface CampaignQuery extends PageQuery {
  status?: CampaignStatus;
  search?: string;
}

export interface CreateCampaignRequest {
  name: string;
  description: string;
  template_id: string;
  from_email: string;
  from_name: string;
  reply_to: string;
  audience: Audience;
}

export type UpdateCampaignRequest = Partial<CreateCampaignRequest>;

export const campaignsApi = {
  list: (query: CampaignQuery): Promise<Page<Campaign>> =>
    fetchPage<Campaign>(endpoints.campaigns.collection, { ...query }),
  get: (id: string) => api.get<Campaign>(endpoints.campaigns.byId(id)),
  create: (input: CreateCampaignRequest) =>
    api.post<Campaign>(endpoints.campaigns.collection, { body: input }),
  update: (id: string, input: UpdateCampaignRequest) =>
    api.patch<Campaign>(endpoints.campaigns.byId(id), { body: input }),
  remove: (id: string) => api.delete<null>(endpoints.campaigns.byId(id)),

  /** Sin template_version se fija la version publicada en ese momento. */
  schedule: (id: string, scheduledAt: string, templateVersion?: number) =>
    api.post<Campaign>(endpoints.campaigns.schedule(id), {
      body: { scheduled_at: scheduledAt, template_version: templateVersion },
    }),
  start: (id: string, templateVersion?: number) =>
    api.post<Campaign>(endpoints.campaigns.start(id), {
      body: templateVersion ? { template_version: templateVersion } : undefined,
    }),
  pause: (id: string) => api.post<Campaign>(endpoints.campaigns.pause(id)),
  resume: (id: string) => api.post<Campaign>(endpoints.campaigns.resume(id)),
  cancel: (id: string) => api.post<Campaign>(endpoints.campaigns.cancel(id)),

  batches: (id: string, query: PageQuery): Promise<Page<CampaignBatch>> =>
    fetchPage<CampaignBatch>(endpoints.campaigns.batches(id), { ...query }),
  sendTest: (id: string, emails: string[], templateVersion?: number) =>
    api.post<TestSendResult>(endpoints.campaigns.test(id), {
      body: { emails, template_version: templateVersion },
    }),
};
