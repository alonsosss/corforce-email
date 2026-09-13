import { api } from './client';
import { endpoints } from './endpoints';
import { fetchList, fetchPage } from './paging';
import type { Page, PageQuery } from './types';

// DTOs de services/billing/internal/adapters/http/dto.go. Los importes viajan como texto
// decimal con escala fija ("49.00", "0.001500") y nunca se convierten a numero aqui.

export type BillingResource =
  | 'users'
  | 'domains'
  | 'mailboxes'
  | 'storage_bytes'
  | 'contacts'
  | 'transactional_messages'
  | 'marketing_messages';
export type BillingPeriod = 'monthly' | 'yearly';
export type PlanStatus = 'active' | 'retired';
export type SubscriptionStatus = 'trialing' | 'active' | 'past_due' | 'suspended' | 'cancelled';

// Espejo de domain.Resources(), ParseBillingPeriod, PlanStatus y SubscriptionStatuses()
// (services/billing/internal/domain). billing no publica aun su catalogo: el alta de un
// plan necesita la lista completa de recursos (un limite por recurso). Cuando exista
// GET /billing/meta, estas listas se toman de alli.
export const BILLING_RESOURCES: readonly BillingResource[] = [
  'users',
  'domains',
  'mailboxes',
  'storage_bytes',
  'contacts',
  'transactional_messages',
  'marketing_messages',
];
export const BILLING_PERIODS: readonly BillingPeriod[] = ['monthly', 'yearly'];
export const PLAN_STATUSES: readonly PlanStatus[] = ['active', 'retired'];
export const SUBSCRIPTION_STATUSES: readonly SubscriptionStatus[] = [
  'trialing',
  'active',
  'past_due',
  'suspended',
  'cancelled',
];

/** domain.Unlimited: included = -1 significa que el plan no limita el recurso. */
export const UNLIMITED = -1;

export interface PlanLimit {
  resource: BillingResource;
  included: number;
  hard_limit: boolean;
  overage_unit_price: string | null;
}

export interface Plan {
  id: string;
  code: string;
  name: string;
  description: string;
  currency: string;
  base_price: string;
  billing_period: BillingPeriod;
  status: PlanStatus;
  limits: PlanLimit[];
  created_at: string;
  updated_at: string;
}

export interface Subscription {
  id: string;
  tenant_id: string;
  plan_id: string;
  plan_code: string;
  /** Solo en GET /subscription de la empresa; el listado de plataforma lleva el codigo. */
  plan?: Plan;
  status: SubscriptionStatus;
  /** Dias de calendario UTC, [inicio, fin). */
  current_period_start: string;
  current_period_end: string;
  trial_ends_at: string | null;
  cancel_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface UsageLine {
  resource: BillingResource;
  /** stock: lo que existe; flow: lo consumido en el periodo. */
  kind: 'stock' | 'flow';
  used: number;
  included: number;
  hard_limit: boolean;
  overage: number;
  /** Porcentaje con dos decimales; null si el plan no limita o no incluye nada. */
  percent: string | null;
}

export interface UsageReport {
  tenant_id: string;
  plan_code: string;
  period_start: string;
  period_end: string;
  resources: UsageLine[];
}

export interface LimitInput {
  resource: BillingResource;
  included: number;
  hard_limit: boolean;
  overage_unit_price?: string;
}

export interface CreatePlanRequest {
  code: string;
  name: string;
  description: string;
  currency: string;
  base_price: string;
  billing_period: BillingPeriod;
  limits: LimitInput[];
}

export interface UpdatePlanRequest {
  name?: string;
  description?: string;
  currency?: string;
  base_price?: string;
  billing_period?: BillingPeriod;
  limits?: LimitInput[];
}

/** PUT: una fecha ausente no cambia; null la vacia. */
export interface PutSubscriptionRequest {
  plan_code: string;
  status?: SubscriptionStatus;
  trial_ends_at?: string | null;
  cancel_at?: string | null;
}

export interface SubscriptionQuery extends PageQuery {
  status?: SubscriptionStatus;
}

export const billingApi = {
  subscription: () => api.get<Subscription>(endpoints.billing.subscription),
  usage: () => api.get<UsageReport>(endpoints.billing.usage),

  listPlans: (status?: PlanStatus) =>
    fetchList<Plan>(endpoints.billing.plans.collection, { status }),
  getPlan: (id: string) => api.get<Plan>(endpoints.billing.plans.byId(id)),
  createPlan: (input: CreatePlanRequest) =>
    api.post<Plan>(endpoints.billing.plans.collection, { body: input }),
  updatePlan: (id: string, input: UpdatePlanRequest) =>
    api.patch<Plan>(endpoints.billing.plans.byId(id), { body: input }),
  retirePlan: (id: string) => api.post<Plan>(endpoints.billing.retirePlan(id)),

  listSubscriptions: (query: SubscriptionQuery): Promise<Page<Subscription>> =>
    fetchPage<Subscription>(endpoints.billing.subscriptions.collection, { ...query }),
  putSubscription: (tenantId: string, input: PutSubscriptionRequest) =>
    api.put<Subscription>(endpoints.billing.subscriptions.byId(tenantId), { body: input }),
  tenantUsage: (tenantId: string) => api.get<UsageReport>(endpoints.billing.tenantUsage(tenantId)),
};
