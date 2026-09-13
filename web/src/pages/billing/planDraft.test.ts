import { describe, expect, it } from 'vitest';
import { BILLING_RESOURCES, UNLIMITED, type Plan } from '@/api/billing';
import { t } from '@/i18n';
import { emptyPlanDraft, limitsFromDraft, planToDraft, sameLimits } from './planDraft';

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
    expect(emptyPlanDraft().limits.map((l) => l.resource)).toEqual([...BILLING_RESOURCES]);
  });

  it('los importes viajan como el texto escrito, sin pasar por float', () => {
    const draft = planToDraft(plan);
    const { limits, errors } = limitsFromDraft(draft.limits);
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
    const draft = emptyPlanDraft();
    const [first, second, third] = draft.limits;
    if (!first || !second || !third) throw new Error('faltan recursos');
    first.included = '1.5';
    second.hardLimit = false;
    second.overage = '1,5';
    third.overage = '0.10';
    const { limits, errors } = limitsFromDraft(draft.limits);
    expect(errors[first.resource]).toBe(t('billing.form.includedInvalid'));
    expect(errors[second.resource]).toBe(t('billing.form.amountInvalid'));
    expect(limits.find((l) => l.resource === third.resource)?.overage_unit_price).toBeUndefined();
  });

  it('detecta si los limites no cambiaron para no tocar las condiciones del plan', () => {
    const { limits } = limitsFromDraft(planToDraft(plan).limits);
    const withAll = limits.filter((l) => plan.limits.some((p) => p.resource === l.resource));
    expect(sameLimits(withAll, plan.limits)).toBe(true);
    expect(sameLimits(limits, plan.limits)).toBe(false);
  });
});
