import { useState } from 'react';
import {
  RATE_LIMIT_UNITS,
  mailSecurityApi,
  type RateLimit,
  type RateLimitUnit,
} from '@/api/mailSecurity';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Badge, FormField, Input, Select, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ListTab, type ResourceFormProps } from '@/pages/shared/ResourceTab';
import { ObjectField } from './ObjectField';
import { normalizePolicyObject, policyObjectKind } from './policyObject';
import { formatRateLimit, parseRateLimit } from './rateLimit';

const loadLimits = () => mailSecurityApi.listRateLimits();

function RateLimitValue({ value }: { value: string }) {
  const parts = parseRateLimit(value);
  if (!parts) return <span className="cf-mono">{value}</span>;
  return (
    <>
      {t('security.rateLimits.per', {
        n: parts.amount,
        unit: tEnum('security.rateLimits.unit', parts.unit),
      })}
    </>
  );
}

const columns: Column<RateLimit>[] = [
  {
    key: 'object',
    header: t('security.object'),
    render: (r) => (
      <span className="cf-inline">
        <strong className="cf-mono">{r.object}</strong>
        <Badge>{tEnum('security.objectKind', policyObjectKind(r.object))}</Badge>
      </span>
    ),
  },
  {
    key: 'value',
    header: t('security.rateLimits.value'),
    render: (r) => <RateLimitValue value={r.value} />,
  },
  { key: 'updated', header: t('common.updatedAt'), render: (r) => formatDateTime(r.updated_at) },
];

export function RateLimitsTab() {
  const { can } = useAccess();
  const canUpdate = can(...PERMISSIONS.rateLimits.update);
  return (
    <ListTab
      load={loadLimits}
      rowKey={(r) => r.object}
      columns={columns}
      Form={RateLimitForm}
      canCreate={canUpdate}
      canUpdate={canUpdate}
      canDelete={can(...PERMISSIONS.rateLimits.delete)}
      remove={(r) => mailSecurityApi.deleteRateLimit(r.object)}
      texts={{
        title: t('security.rateLimits.title'),
        description: t('security.rateLimits.description'),
        create: t('security.rateLimits.new'),
        empty: t('security.rateLimits.empty'),
        created: t('security.saved'),
        updated: t('security.saved'),
        deleted: t('security.deleted'),
        deleteTitle: t('security.rateLimits.delete'),
        deleteConfirm: (r) => t('security.rateLimits.deleteConfirm', { object: r.object }),
      }}
    />
  );
}

function RateLimitForm({ item, onClose, onSaved }: ResourceFormProps<RateLimit>) {
  const current = item ? parseRateLimit(item.value) : null;
  const [object, setObject] = useState(item?.object ?? '');
  const [amount, setAmount] = useState(current?.amount ?? '');
  const [unit, setUnit] = useState<RateLimitUnit | ''>(current?.unit ?? '');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});

  const action = useAction(async (target: string, value: string) => {
    await mailSecurityApi.putRateLimit(target, value);
    onSaved();
  });

  const submit = async () => {
    const target = item ? item.object : normalizePolicyObject(object);
    const value = unit ? formatRateLimit(amount, unit) : null;
    const next = {
      object: target ? undefined : t('security.objectInvalid'),
      amount: unit && !value ? t('validation.nonNegativeInteger') : undefined,
      unit: unit ? undefined : t('validation.required'),
    };
    setErrors(next);
    if (!target || !value) return;
    await action.run(target, value);
  };

  return (
    <FormModal
      id="rate-limit-form"
      title={item ? t('security.rateLimits.editTitle') : t('security.rateLimits.createTitle')}
      submitLabel={t('common.save')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
    >
      <ObjectField
        id="rate-limit-object"
        value={object}
        onChange={setObject}
        error={errors.object}
        disabled={Boolean(item)}
      />
      <div className="cf-form__row">
        <FormField
          label={t('security.rateLimits.amount')}
          htmlFor="rate-limit-amount"
          required
          error={errors.amount}
        >
          <Input
            id="rate-limit-amount"
            type="number"
            min={0}
            step={1}
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            invalid={Boolean(errors.amount)}
          />
        </FormField>
        <FormField
          label={t('security.rateLimits.window')}
          htmlFor="rate-limit-unit"
          required
          error={errors.unit}
        >
          <Select
            id="rate-limit-unit"
            placeholder={t('common.select')}
            options={RATE_LIMIT_UNITS.map((u) => ({
              value: u,
              label: tEnum('security.rateLimits.window', u),
            }))}
            value={unit}
            onChange={(e) => setUnit(e.target.value as RateLimitUnit | '')}
            invalid={Boolean(errors.unit)}
          />
        </FormField>
      </div>
      <span className="cf-text-sm cf-text-secondary">{t('security.rateLimits.formatHint')}</span>
    </FormModal>
  );
}
