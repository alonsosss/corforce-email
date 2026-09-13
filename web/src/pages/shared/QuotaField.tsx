import { FormField, Input, Select } from '@/design/components';
import { QUOTA_UNITS, quotaToBytes, type QuotaAmount, type QuotaUnit } from '@/lib/quota';
import { t, tEnum } from '@/i18n';

export interface QuotaFieldProps {
  id: string;
  label: string;
  value: QuotaAmount;
  onChange: (value: QuotaAmount) => void;
  hint?: string;
  error?: string | null;
  disabled?: boolean;
  required?: boolean;
}

/** Cantidad y unidad (MiB o GiB); el formulario la convierte a bytes con readQuota. */
export function QuotaField({
  id,
  label,
  value,
  onChange,
  hint,
  error,
  disabled,
  required,
}: QuotaFieldProps) {
  return (
    <FormField label={label} htmlFor={id} hint={hint} error={error} required={required}>
      <div className="cf-inline-field">
        <Input
          id={id}
          inputMode="decimal"
          value={value.amount}
          onChange={(e) => onChange({ ...value, amount: e.target.value })}
          invalid={Boolean(error)}
          disabled={disabled}
          autoComplete="off"
        />
        <Select
          aria-label={t('quota.unitLabel')}
          options={QUOTA_UNITS.map((unit) => ({ value: unit, label: tEnum('quota.unit', unit) }))}
          value={value.unit}
          onChange={(e) => onChange({ ...value, unit: e.target.value as QuotaUnit })}
          disabled={disabled}
        />
      </div>
    </FormField>
  );
}

export interface QuotaReading {
  /** Bytes enteros; ausente si el campo esta vacio y es opcional. */
  bytes?: number;
  error: string | null;
}

export function readQuota(value: QuotaAmount, optional = false): QuotaReading {
  if (!value.amount.trim()) {
    return optional ? { error: null } : { error: t('validation.required') };
  }
  const bytes = quotaToBytes(value.amount, value.unit);
  return bytes === null ? { error: t('validation.quota') } : { bytes, error: null };
}
