import { describe, expect, it } from 'vitest';
import type { BillingMeta, Plan } from '@/api/billing';
import { t } from '@/i18n';
import { emptyPlanDraft, limitsFromDraft, planToDraft, sameLimits } from './planDraft';

// Catalogo como lo sirve GET /billing/meta.
const meta: BillingMeta = {
  resources: [
    { resource: 'users', kind: 'stock' },
    { resource: 'domains', kind: 'stock' },
    { resource: 'mailboxes', kind: 'stock' },
    { resource: 'storage_bytes', kind: 'stock' },
    { resource: 'contacts', kind: 'stock' },
    { resource: 'transactional_messages', kind: 'flow' },
    { resource: 'marketing_messages', kind: 'flow' },
  ],
  resource_kinds: ['stock', 'flow'],
  billing_periods: [
    { period: 'monthly', months: 1 },
    { period: 'yearly', months: 12 },
  ],
  plan_statuses: ['active', 'retired'],
  subscription_statuses: [{ status: 'active', allows_usage: true }],
  unlimited: -1,
  limits: {
    max_plan_name_length: 120,
    max_plan_description_length: 1000,
    price_scale: 2,
    unit_price_scale: 6,
  },
  pagination: { default_page_size: 20, max_page_size: 100 },
};
const UNLIMITED = meta.unlimited;

const plan: Plan = {
  id: 'p',
  code: 'pro',
  name: 'Pro',
  description: '',
  currency: 'USD',
  base_price: '49.00',
  billing_period: 'monthly',
  status: 'active',
  limits: [
    { resource: 'users', included: 10, hard_limit: true, overage_unit_price: null },
    {
      resource: 'marketing_messages',
      included: 5000,
      hard_limit: false,
      overage_unit_price: '0.001500',
    },
    { resource: 'contacts', included: UNLIMITED, hard_limit: false, overage_unit_price: null },
  ],
  created_at: '',
  updated_at: '',
};

describe('limites de un plan', () => {
  it('un plan nuevo declara un limite por cada recurso', () => {
    expect(emptyPlanDraft(meta).limits.map((l) => l.resource)).toEqual(
      meta.resources.map((r) => r.resource),
    );
  });

  it('los importes viajan como el texto escrito, sin pasar por float', () => {
    const draft = planToDraft(plan, meta);
    const { limits, errors } = limitsFromDraft(draft.limits, UNLIMITED);
    expect(errors).toEqual({});
    expect(limits.find((l) => l.resource === 'marketing_messages')).toEqual({
      resource: 'marketing_messages',
      included: 5000,
      hard_limit: false,
      overage_unit_price: '0.001500',
    });
    expect(limits.find((l) => l.resource === 'contacts')?.included).toBe(UNLIMITED);
    expect(draft.basePrice).toBe('49.00');
  });

  it('rechaza cantidades e importes mal escritos y no manda excedente en un limite duro', () => {
    const draft = emptyPlanDraft(meta);
    const [first, second, third] = draft.limits;
    if (!first || !second || !third) throw new Error('faltan recursos');
    first.included = '1.5';
    second.hardLimit = false;
    second.overage = '1,5';
    third.overage = '0.10';
    const { limits, errors } = limitsFromDraft(draft.limits, UNLIMITED);
    expect(errors[first.resource]).toBe(t('billing.form.includedInvalid'));
    expect(errors[second.resource]).toBe(t('billing.form.amountInvalid'));
    expect(limits.find((l) => l.resource === third.resource)?.overage_unit_price).toBeUndefined();
  });

  it('detecta si los limites no cambiaron para no tocar las condiciones del plan', () => {
    const { limits } = limitsFromDraft(planToDraft(plan, meta).limits, UNLIMITED);
    const withAll = limits.filter((l) => plan.limits.some((p) => p.resource === l.resource));
    expect(sameLimits(withAll, plan.limits)).toBe(true);
    expect(sameLimits(limits, plan.limits)).toBe(false);
  });
});
