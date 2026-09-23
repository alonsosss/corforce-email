import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import { cachedResource } from './resource';
import type { Page, PageQuery } from './types';

// DTOs de services/campaigns/internal/adapters/http/handler.go (campaignResponse,
// batchResponse), options.go (prueba A/B, reenvio, zona horaria y plan de envio),
// domain/campaign.go, domain/stats.go y ports.BatchResult. Estados, las acciones que admite
// cada uno y los topes llegan en GET /campaigns/meta.

export type CampaignStatus =
  'draft' | 'scheduled' | 'sending' | 'paused' | 'completed' | 'cancelled' | 'failed';

/** Acciones que el dominio admite en un estado (el servicio las vuelve a comprobar). */
export interface CampaignStatusInfo {
  status: CampaignStatus;
  editable: boolean;
  /** Plantilla y audiencia fijas: en pausa ya hay destinatarios. */
  content_locked: boolean;
  can_schedule: boolean;
  can_start: boolean;
  can_pause: boolean;
  can_resume: boolean;
  can_cancel: boolean;
  deletable: boolean;
}

export interface CampaignsMeta {
  statuses: CampaignStatusInfo[];
  batch_statuses: BatchStatus[];
  pause_reason_manual: string;
  max_test_recipients: number;
  /** Tamano efectivo de lote; max_batch_size es el tope del lote de transactional. */
  batch_size: number;
  max_batch_size: number;
  limits: {
    max_name_length: number;
    max_description_length: number;
    max_display_name_length: number;
    max_email_length: number;
    max_audience_ids: number;
    max_search_length: number;
  };
  schedule: { min_lead_seconds: number; max_horizon_seconds: number };
  pagination: { default_page_size: number; max_page_size: number };
  ab_test: {
    criteria: ABCriterion[];
    min_variants: number;
    max_variants: number;
    min_sample_percent: number;
    max_sample_percent: number;
    min_decision_window_minutes: number;
    max_decision_window_minutes: number;
    decision_reasons: DecisionReason[];
  };
  resend: { min_delay_minutes: number; max_delay_minutes: number };
  phases: { kinds: PhaseKind[]; statuses: PhaseStatus[] };
  /** Tope del asunto de una variante y del reenvio. */
  max_subject_length: number;
}

export type ABCriterion = 'opens' | 'clicks';
export type DecisionReason = 'criterion' | 'secondary' | 'delivered' | 'first';
export type PhaseKind = 'main' | 'sample' | 'winner' | 'zone' | 'resend';
export type PhaseStatus = 'pending' | 'done';

/** Variante: sin plantilla propia usa la de la campana; sin asunto, el de la plantilla. */
export interface ABVariantInput {
  subject: string;
  template_id: string | null;
  template_version: number | null;
}

export interface ABVariant extends ABVariantInput {
  label: string;
  /** Version fijada al programar o iniciar. */
  pinned_version: number | null;
}

export interface ABTestInput {
  criterion: ABCriterion;
  sample_percent: number;
  decision_window_minutes: number;
  variants: ABVariantInput[];
}

export interface ABTest extends Omit<ABTestInput, 'variants'> {
  variants: ABVariant[];
}

export interface Resend {
  subject: string;
  delay_minutes: number;
}

/** Hora de pared "AAAA-MM-DDTHH:MM" que cada contacto recibe en su zona. */
export interface TimezoneDelivery {
  local_send_at: string;
  fallback_timezone: string;
}

/** Interaccion unica sobre entregados; tasas decimales con cuatro cifras. */
export interface Engagement {
  accepted: number;
  delivered: number;
  opened: number;
  clicked: number;
  open_rate: string;
  click_rate: string;
}

export interface ABDecisionResult extends Engagement {
  variant: number;
  label: string;
}

export interface ABDecision {
  winner: number;
  criterion: ABCriterion;
  reason: DecisionReason;
  results: ABDecisionResult[];
}

export interface CampaignPhase {
  id: string;
  kind: PhaseKind;
  variant: number | null;
  slot_at: string | null;
  not_before: string | null;
  status: PhaseStatus;
  targeted: number;
  accepted: number;
  suppressed: number;
  started_at: string | null;
  completed_at: string | null;
}

export interface PhaseEngagement extends Engagement {
  kind: PhaseKind;
  variant: number | null;
}

export interface CampaignPlan {
  phases: CampaignPhase[];
  engagement: PhaseEngagement[];
}

/** Acciones del estado segun el catalogo; null si el estado no esta en el catalogo. */
export function campaignStatusInfo(
  meta: CampaignsMeta,
  status: CampaignStatus,
): CampaignStatusInfo | null {
  return meta.statuses.find((s) => s.status === status) ?? null;
}

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
  ab_test: ABTest | null;
  /** Indice de la variante ganadora (0 = A). */
  ab_winner: number | null;
  ab_decided_at: string | null;
  /** Foto de los contadores de la decision; solo con campaigns/stats/read. */
  ab_decision?: ABDecision;
  resend: Resend | null;
  timezone_delivery: TimezoneDelivery | null;
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
  ab_test: ABTestInput | null;
  resend: Resend | null;
}

/** null en ab_test o resend los quita; ausentes no se tocan. */
export type UpdateCampaignRequest = Partial<CreateCampaignRequest>;

/** Programar a un instante o a la hora local de cada contacto. */
export type ScheduleRequest =
  | { scheduled_at: string; template_version?: number }
  | { local_send_at: string; fallback_timezone: string; template_version?: number };

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
  schedule: (id: string, body: ScheduleRequest) =>
    api.post<Campaign>(endpoints.campaigns.schedule(id), { body }),
  start: (id: string, templateVersion?: number) =>
    api.post<Campaign>(endpoints.campaigns.start(id), {
      body: templateVersion ? { template_version: templateVersion } : undefined,
    }),
  pause: (id: string) => api.post<Campaign>(endpoints.campaigns.pause(id)),
  resume: (id: string) => api.post<Campaign>(endpoints.campaigns.resume(id)),
  cancel: (id: string) => api.post<Campaign>(endpoints.campaigns.cancel(id)),

  batches: (id: string, query: PageQuery): Promise<Page<CampaignBatch>> =>
    fetchPage<CampaignBatch>(endpoints.campaigns.batches(id), { ...query }),
  /** variant prueba una variante A/B (0 = A). */
  sendTest: (id: string, emails: string[], templateVersion?: number, variant?: number) =>
    api.post<TestSendResult>(endpoints.campaigns.test(id), {
      body: { emails, template_version: templateVersion, variant },
    }),
  plan: async (id: string): Promise<CampaignPlan> =>
    (await api.get<CampaignPlan>(endpoints.campaigns.phases(id))).data,

  meta: async (): Promise<CampaignsMeta> =>
    (await api.get<CampaignsMeta>(endpoints.campaigns.meta)).data,
};

export const campaignsMeta = cachedResource(campaignsApi.meta);
