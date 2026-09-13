import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import type { SendClass } from './sendClass';
import type { Page, PageQuery } from './types';

// DTOs de services/analytics/internal/adapters/http/handler.go y domain/counters.go. Los
// dias son dias de la zona que devuelve el servicio en `timezone`.

export interface Counters {
  sent: number;
  delivered: number;
  bounced_hard: number;
  bounced_soft: number;
  complained: number;
  opened_unique: number;
  clicked_unique: number;
  unsubscribed: number;
  failed: number;
}

/** Fracciones decimales con cuatro cifras ("0.2512"), calculadas por el servicio. */
export interface Rates {
  delivery: string;
  bounce: string;
  complaint: string;
  open: string;
  click: string;
  unsubscribe: string;
}

interface RangeEcho {
  from: string;
  to: string;
  timezone: string;
}

export interface Overview extends RangeEcho {
  class: SendClass | null;
  totals: Counters;
  rates: Rates;
}

export interface DayPoint extends Counters {
  day: string;
}

export interface Timeseries extends RangeEcho {
  class: SendClass | null;
  points: DayPoint[];
}

export interface CampaignReport {
  campaign_id: string;
  status: string | null;
  started_at: string | null;
  completed_at: string | null;
  first_day: string | null;
  last_day: string | null;
  totals: Counters;
  rates: Rates;
}

export interface DomainReport {
  recipient_domain: string;
  totals: Counters;
  rates: Rates;
}

export interface DomainsReport extends RangeEcho {
  class: SendClass | null;
  limit: number;
  domains: DomainReport[];
}

export interface RangeQuery {
  /** AAAA-MM-DD; sin fechas el servicio usa su rango por defecto. */
  from?: string;
  to?: string;
  class?: SendClass;
}

export const analyticsApi = {
  overview: async (query: RangeQuery): Promise<Overview> =>
    (await api.get<Overview>(endpoints.analytics.overview, { params: { ...query } })).data,
  timeseries: async (query: RangeQuery): Promise<Timeseries> =>
    (await api.get<Timeseries>(endpoints.analytics.timeseries, { params: { ...query } })).data,
  campaigns: (query: PageQuery): Promise<Page<CampaignReport>> =>
    fetchPage<CampaignReport>(endpoints.analytics.campaigns.collection, { ...query }),
  domains: async (query: RangeQuery & { limit?: number }): Promise<DomainsReport> =>
    (await api.get<DomainsReport>(endpoints.analytics.domains, { params: { ...query } })).data,
};
