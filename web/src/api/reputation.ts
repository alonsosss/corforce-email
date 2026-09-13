import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import type { SendClass } from './sendClass';
import type { Page, PageQuery } from './types';

// DTOs de services/reputation/internal/adapters/http/handler.go. Las tasas y los umbrales
// viajan como fracciones en texto decimal y se muestran sin recalcular.

export type ReputationState = 'ok' | 'warning' | 'restricted' | 'suspended';

// Espejo de domain.States() (de menos a mas grave) para el filtro de plataforma.
// reputation no publica aun su catalogo; cuando exista se toma de alli.
export const REPUTATION_STATES: readonly ReputationState[] = [
  'ok',
  'warning',
  'restricted',
  'suspended',
];

export interface RateUsage {
  limit: number;
  /** null cuando el contador de tasa no respondio. */
  used: number | null;
}

export interface ReputationWindow {
  sent: number;
  bounced: number;
  complained: number;
  bounce_rate: string;
  complaint_rate: string;
}

export interface ClassSummary {
  class: SendClass;
  state: ReputationState;
  reason: string;
  /** Lo fijo el superadmin: la evaluacion automatica no lo cambia hasta liberarlo. */
  manual: boolean;
  changed_at: string | null;
  changed_by: string | null;
  window: ReputationWindow;
}

export interface Thresholds {
  bounce_warn: string;
  bounce_block: string;
  complaint_warn: string;
  complaint_block: string;
}

export interface ClassStatus extends ClassSummary {
  thresholds: Thresholds;
  hourly: RateUsage;
  daily: RateUsage;
}

export interface ReputationStatus {
  window_days: number;
  window_start: string;
  min_volume: number;
  classes: ClassStatus[];
}

export interface StateChange {
  id: string;
  class: SendClass;
  from: ReputationState;
  to: ReputationState;
  reason: string;
  bounce_rate: string;
  complaint_rate: string;
  manual: boolean;
  changed_by: string | null;
  created_at: string;
}

export interface TenantReputation {
  tenant_id: string;
  /** La base de la empresa no respondio: se lista para no aparentar un listado completo. */
  unavailable: boolean;
  classes: ClassSummary[];
}

export interface LimitsResult {
  tenant_id: string;
  class: SendClass;
  hourly: number;
  daily: number;
  override: {
    hourly: number | null;
    daily: number | null;
    updated_by: string;
    updated_at: string;
  } | null;
}

export interface StateRecord {
  tenant_id: string;
  class: SendClass;
  state: ReputationState;
  reason: string;
  manual: boolean;
  bounce_rate: string;
  complaint_rate: string;
  changed_at: string | null;
  changed_by: string | null;
}

export interface HistoryQuery extends PageQuery {
  class?: SendClass;
}

/** Un limite ausente o null vuelve al de la plataforma; los dos a la vez retiran los propios. */
export interface LimitsInput {
  hourly: number | null;
  daily: number | null;
}

export const reputationApi = {
  status: () => api.get<ReputationStatus>(endpoints.reputation.status),
  history: (query: HistoryQuery): Promise<Page<StateChange>> =>
    fetchPage<StateChange>(endpoints.reputation.history, { ...query }),

  tenants: (state?: ReputationState) =>
    fetchList<TenantReputation>(endpoints.reputation.tenants, { state }),
  setLimits: (tenantId: string, sendClass: SendClass, input: LimitsInput) =>
    api.put<LimitsResult>(endpoints.reputation.limits(tenantId, sendClass), { body: input }),
  suspend: (tenantId: string, sendClass: SendClass, reason: string) =>
    api.post<StateRecord>(endpoints.reputation.suspend(tenantId, sendClass), {
      body: { reason },
    }),
  release: (tenantId: string, sendClass: SendClass) =>
    api.post<StateRecord>(endpoints.reputation.release(tenantId, sendClass)),
};
