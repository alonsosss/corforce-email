import { useState } from 'react';
import {
  billingApi,
  billingMeta,
  type Plan,
  type PutSubscriptionRequest,
  type Subscription,
  type SubscriptionStatus,
} from '@/api/billing';
import type { Tenant } from '@/api/organization';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { FormField, Input, Select } from '@/design/components';
import { isPast, toDatetimeLocal } from '@/lib/datetime';
import { localToRfc3339 } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';

/**
 * Asigna o cambia el plan de una empresa (PUT idempotente). Las fechas se envian solo si
 * cambian; vaciar una que existia la retira (null).
 */
export function SubscriptionForm({
  subscription,
  tenantId,
  tenants,
  plans,
  onClose,
  onSaved,
}: {
  subscription: Subscription | null;
  tenantId: string | null;
  tenants: readonly Tenant[];
  plans: readonly Plan[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [tenant, setTenant] = useState(tenantId ?? '');
  const [planCode, setPlanCode] = useState(subscription?.plan_code ?? '');
  const [status, setStatus] = useState<SubscriptionStatus | ''>('');
  const meta = useResource(billingMeta);
  const initialTrial = toDatetimeLocal(subscription?.trial_ends_at);
  const initialCancel = toDatetimeLocal(subscription?.cancel_at);
  const [trial, setTrial] = useState(initialTrial);
  const [cancelAt, setCancelAt] = useState(initialCancel);
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (target: string, body: PutSubscriptionRequest) => {
    await billingApi.putSubscription(target, body);
    onSaved();
  });

  const dateField = (value: string, initial: string): string | null | undefined => {
    if (value === initial) return undefined;
    return value ? (localToRfc3339(value) ?? undefined) : null;
  };

  const submit = async () => {
    const trialValue = dateField(trial, initialTrial);
    const cancelValue = dateField(cancelAt, initialCancel);
    const next = {
      tenant: tenant ? undefined : t('validation.required'),
      planCode: planCode ? undefined : t('validation.required'),
      trial:
        typeof trialValue === 'string' && isPast(trialValue)
          ? t('billing.subscriptions.futureDate')
          : undefined,
      cancelAt:
        typeof cancelValue === 'string' && isPast(cancelValue)
          ? t('billing.subscriptions.futureDate')
          : undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;
    await action.run(tenant, {
      plan_code: planCode,
      status: status || undefined,
      trial_ends_at: trialValue,
      cancel_at: cancelValue,
    });
  };

  const activePlans = plans.filter(
    (p) => p.status === 'active' || p.code === subscription?.plan_code,
  );

  return (
    <FormModal
      id="subscription-form"
      title={
        subscription
          ? t('billing.subscriptions.changeTitle')
          : t('billing.subscriptions.assignTitle')
      }
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <FormField
        label={t('billing.subscriptions.tenant')}
        htmlFor="subscription-tenant"
        required
        error={errors.tenant}
      >
        <Select
          id="subscription-tenant"
          placeholder={t('common.select')}
          options={tenants.map((tn) => ({ value: tn.id, label: `${tn.name} (${tn.slug})` }))}
          value={tenant}
          onChange={(e) => setTenant(e.target.value)}
          disabled={Boolean(tenantId)}
          invalid={Boolean(errors.tenant)}
        />
      </FormField>
      <FormField
        label={t('billing.plan')}
        htmlFor="subscription-plan"
        required
        error={errors.planCode}
      >
        <Select
          id="subscription-plan"
          placeholder={t('common.select')}
          options={activePlans.map((p) => ({ value: p.code, label: `${p.name} (${p.code})` }))}
          value={planCode}
          onChange={(e) => setPlanCode(e.target.value)}
          invalid={Boolean(errors.planCode)}
        />
      </FormField>
      <FormField
        label={t('common.status')}
        htmlFor="subscription-status"
        hint={
          subscription
            ? t('billing.subscriptions.statusKeep')
            : t('billing.subscriptions.statusDefault')
        }
      >
        <Select
          id="subscription-status"
          placeholder={t('billing.subscriptions.noStatusChange')}
          options={(meta.data?.subscription_statuses ?? []).map(({ status: s }) => ({
            value: s,
            label: tEnum('billing.subscriptionStatus', s),
          }))}
          value={status}
          onChange={(e) => setStatus(e.target.value as SubscriptionStatus | '')}
        />
      </FormField>
      <div className="cf-form__row">
        <FormField
          label={t('billing.trialEnds')}
          htmlFor="subscription-trial"
          error={errors.trial}
          hint={t('billing.subscriptions.clearHint')}
        >
          <Input
            id="subscription-trial"
            type="datetime-local"
            value={trial}
            onChange={(e) => setTrial(e.target.value)}
            invalid={Boolean(errors.trial)}
          />
        </FormField>
        <FormField
          label={t('billing.cancelAt')}
          htmlFor="subscription-cancel"
          error={errors.cancelAt}
          hint={t('billing.subscriptions.clearHint')}
        >
          <Input
            id="subscription-cancel"
            type="datetime-local"
            value={cancelAt}
            onChange={(e) => setCancelAt(e.target.value)}
            invalid={Boolean(errors.cancelAt)}
          />
        </FormField>
      </div>
    </FormModal>
  );
}
