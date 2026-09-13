import { useState } from 'react';
import {
  BILLING_PERIODS,
  billingApi,
  type BillingPeriod,
  type Plan,
  type UpdatePlanRequest,
} from '@/api/billing';
import { useAction } from '@/hooks/useAction';
import { Checkbox, FormField, Input, Select, Textarea } from '@/design/components';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import {
  emptyPlanDraft,
  isAmount,
  limitsFromDraft,
  planToDraft,
  sameLimits,
  type LimitDraft,
  type PlanDraft,
} from './planDraft';

export function PlanForm({
  plan,
  onClose,
  onSaved,
}: {
  /** null en el alta. */
  plan: Plan | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [draft, setDraft] = useState<PlanDraft>(() =>
    plan ? planToDraft(plan) : emptyPlanDraft(),
  );
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const set = (patch: Partial<PlanDraft>) => setDraft((d) => ({ ...d, ...patch }));
  const setLimit = (index: number, patch: Partial<LimitDraft>) =>
    setDraft((d) => ({
      ...d,
      limits: d.limits.map((l, i) => (i === index ? { ...l, ...patch } : l)),
    }));

  const action = useAction(async (body: UpdatePlanRequest | null, create: boolean) => {
    if (create) {
      await billingApi.createPlan({
        code: draft.code.trim(),
        name: draft.name.trim(),
        description: draft.description.trim(),
        currency: draft.currency.trim(),
        base_price: draft.basePrice.trim(),
        billing_period: draft.billingPeriod as BillingPeriod,
        limits: limitsFromDraft(draft.limits).limits,
      });
    } else if (plan && body) {
      await billingApi.updatePlan(plan.id, body);
    }
    onSaved();
  });

  const submit = async () => {
    const limits = limitsFromDraft(draft.limits);
    const next: Record<string, string | undefined> = {
      ...limits.errors,
      code: plan ? undefined : (validateField(draft.code, rules.required) ?? undefined),
      name: validateField(draft.name, rules.required) ?? undefined,
      currency: validateField(draft.currency, rules.required) ?? undefined,
      basePrice: !isAmount(draft.basePrice) ? t('billing.form.amountInvalid') : undefined,
      billingPeriod: draft.billingPeriod ? undefined : t('validation.required'),
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    if (!plan) {
      await action.run(null, true);
      return;
    }
    const body: UpdatePlanRequest = {
      name: changed(draft.name.trim(), plan.name),
      description: changed(draft.description.trim(), plan.description),
      currency: changed(draft.currency.trim(), plan.currency),
      base_price: changed(draft.basePrice.trim(), plan.base_price),
      billing_period: changed(draft.billingPeriod as BillingPeriod, plan.billing_period),
      limits: sameLimits(limits.limits, plan.limits) ? undefined : limits.limits,
    };
    if (isEmptyPatch(body)) {
      onClose();
      return;
    }
    await action.run(body, false);
  };

  return (
    <FormModal
      id="plan-form"
      title={plan ? t('billing.plans.editTitle') : t('billing.plans.createTitle')}
      submitLabel={plan ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      {plan ? (
        <span className="cf-text-sm cf-text-secondary">{t('billing.plans.termsHint')}</span>
      ) : null}
      <div className="cf-form__row">
        <FormField
          label={t('billing.planCode')}
          htmlFor="plan-code"
          required={!plan}
          error={errors.code}
          hint={plan ? t('billing.plans.codeImmutable') : t('billing.plans.codeHint')}
        >
          <Input
            id="plan-code"
            className="cf-mono"
            value={draft.code}
            onChange={(e) => set({ code: e.target.value })}
            readOnly={Boolean(plan)}
            invalid={Boolean(errors.code)}
          />
        </FormField>
        <FormField label={t('common.name')} htmlFor="plan-name" required error={errors.name}>
          <Input
            id="plan-name"
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            invalid={Boolean(errors.name)}
          />
        </FormField>
      </div>
      <FormField label={t('common.description')} htmlFor="plan-description">
        <Textarea
          id="plan-description"
          rows={2}
          value={draft.description}
          onChange={(e) => set({ description: e.target.value })}
        />
      </FormField>
      <div className="cf-form__row">
        <FormField
          label={t('billing.form.currency')}
          htmlFor="plan-currency"
          required
          error={errors.currency}
          hint={t('billing.form.currencyHint')}
        >
          <Input
            id="plan-currency"
            className="cf-mono"
            value={draft.currency}
            onChange={(e) => set({ currency: e.target.value.toUpperCase() })}
            invalid={Boolean(errors.currency)}
          />
        </FormField>
        <FormField
          label={t('billing.price')}
          htmlFor="plan-price"
          required
          error={errors.basePrice}
          hint={t('billing.form.amountHint')}
        >
          <Input
            id="plan-price"
            inputMode="decimal"
            className="cf-mono"
            value={draft.basePrice}
            onChange={(e) => set({ basePrice: e.target.value })}
            invalid={Boolean(errors.basePrice)}
          />
        </FormField>
        <FormField
          label={t('billing.form.period')}
          htmlFor="plan-period"
          required
          error={errors.billingPeriod}
        >
          <Select
            id="plan-period"
            placeholder={t('common.select')}
            options={BILLING_PERIODS.map((p) => ({
              value: p,
              label: tEnum('billing.billingPeriod', p),
            }))}
            value={draft.billingPeriod}
            onChange={(e) => set({ billingPeriod: e.target.value as BillingPeriod | '' })}
            invalid={Boolean(errors.billingPeriod)}
          />
        </FormField>
      </div>
      <div className="cf-form__section">{t('billing.limits.title')}</div>
      <span className="cf-text-sm cf-text-secondary">{t('billing.form.limitsHint')}</span>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        {draft.limits.map((limit, index) => {
          const id = `plan-limit-${limit.resource}`;
          const error = errors[limit.resource];
          return (
            <div
              key={limit.resource}
              className="cf-limit-row"
              role="group"
              aria-label={tEnum('billing.resource', limit.resource)}
            >
              <strong className="cf-limit-row__name">
                {tEnum('billing.resource', limit.resource)}
              </strong>
              <Input
                id={`${id}-included`}
                aria-label={t('billing.column.included')}
                inputMode="numeric"
                value={limit.unlimited ? '' : limit.included}
                placeholder={limit.unlimited ? t('billing.unlimited') : undefined}
                disabled={limit.unlimited}
                onChange={(e) => setLimit(index, { included: e.target.value })}
                invalid={Boolean(error)}
              />
              <Checkbox
                label={t('billing.unlimited')}
                checked={limit.unlimited}
                onChange={(e) => setLimit(index, { unlimited: e.target.checked })}
              />
              <Checkbox
                label={t('billing.hardLimit')}
                checked={limit.hardLimit}
                onChange={(e) => setLimit(index, { hardLimit: e.target.checked })}
              />
              <Input
                id={`${id}-overage`}
                aria-label={t('billing.column.overagePrice')}
                inputMode="decimal"
                className="cf-mono"
                placeholder={t('billing.column.overagePrice')}
                value={limit.overage}
                disabled={limit.hardLimit || limit.unlimited}
                onChange={(e) => setLimit(index, { overage: e.target.value })}
              />
              {error ? (
                <span className="cf-field__error cf-limit-row__error" role="alert">
                  {error}
                </span>
              ) : null}
            </div>
          );
        })}
      </div>
    </FormModal>
  );
}
