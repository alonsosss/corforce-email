import {
  BILLING_RESOURCES,
  UNLIMITED,
  type BillingPeriod,
  type BillingResource,
  type LimitInput,
  type Plan,
} from '@/api/billing';
import { t } from '@/i18n';

// Formulario de un plan. Los importes se escriben y viajan como texto decimal: nunca pasan
// por float. El servicio vuelve a validar escala, topes y coherencia de cada limite.

/** Forma de un importe que acepta domain.parseAmount: digitos y un punto decimal. */
const AMOUNT = /^\d+(?:\.\d+)?$/;
const INTEGER = /^\d+$/;

export interface LimitDraft {
  resource: BillingResource;
  included: string;
  unlimited: boolean;
  hardLimit: boolean;
  overage: string;
}

export interface PlanDraft {
  code: string;
  name: string;
  description: string;
  currency: string;
  basePrice: string;
  billingPeriod: BillingPeriod | '';
  limits: LimitDraft[];
}

function defaultLimit(resource: BillingResource): LimitDraft {
  return { resource, included: '0', unlimited: false, hardLimit: true, overage: '' };
}

export function emptyPlanDraft(): PlanDraft {
  return {
    code: '',
    name: '',
    description: '',
    currency: '',
    basePrice: '',
    billingPeriod: '',
    limits: BILLING_RESOURCES.map(defaultLimit),
  };
}

export function planToDraft(plan: Plan): PlanDraft {
  return {
    code: plan.code,
    name: plan.name,
    description: plan.description,
    currency: plan.currency,
    basePrice: plan.base_price,
    billingPeriod: plan.billing_period,
    limits: BILLING_RESOURCES.map((resource) => {
      const limit = plan.limits.find((l) => l.resource === resource);
      if (!limit) return defaultLimit(resource);
      return {
        resource,
        included: limit.included === UNLIMITED ? '' : String(limit.included),
        unlimited: limit.included === UNLIMITED,
        hardLimit: limit.hard_limit,
        overage: limit.overage_unit_price ?? '',
      };
    }),
  };
}

export function isAmount(value: string): boolean {
  return AMOUNT.test(value.trim());
}

export interface LimitsResult {
  limits: LimitInput[];
  errors: Partial<Record<BillingResource, string>>;
}

export function limitsFromDraft(drafts: readonly LimitDraft[]): LimitsResult {
  const limits: LimitInput[] = [];
  const errors: Partial<Record<BillingResource, string>> = {};
  for (const d of drafts) {
    if (!d.unlimited && !INTEGER.test(d.included.trim())) {
      errors[d.resource] = t('billing.form.includedInvalid');
      continue;
    }
    const limit: LimitInput = {
      resource: d.resource,
      included: d.unlimited ? UNLIMITED : Number(d.included.trim()),
      hard_limit: d.hardLimit,
    };
    const overage = d.overage.trim();
    if (overage && !d.hardLimit && !d.unlimited) {
      if (!isAmount(overage)) {
        errors[d.resource] = t('billing.form.amountInvalid');
        continue;
      }
      limit.overage_unit_price = overage;
    }
    limits.push(limit);
  }
  return { limits, errors };
}

/** Limites en forma comparable, para enviar solo si cambiaron. */
export function sameLimits(
  a: readonly LimitInput[],
  b: readonly Plan['limits'][number][],
): boolean {
  const key = (l: {
    resource: string;
    included: number;
    hard_limit: boolean;
    overage_unit_price?: string | null;
  }) => `${l.resource}:${l.included}:${l.hard_limit}:${l.overage_unit_price ?? ''}`;
  const left = a.map(key).sort();
  const right = b.map(key).sort();
  return left.length === right.length && left.every((v, i) => v === right[i]);
}
