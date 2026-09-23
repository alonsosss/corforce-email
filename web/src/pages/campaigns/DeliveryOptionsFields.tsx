import type { CampaignsMeta } from '@/api/campaigns';
import type { Template } from '@/api/templates';
import { Button, Checkbox, FormField, Input, Select } from '@/design/components';
import { IconPlus, IconTrash } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import {
  variantLabel,
  type ABDraft,
  type DraftErrors,
  type ResendDraft,
  type VariantDraft,
} from './campaignDelivery';

interface ABTestFieldsProps {
  draft: ABDraft;
  onChange: (next: ABDraft) => void;
  meta: CampaignsMeta;
  /** Plantillas de marketing; null si el rol no puede leerlas (se escribe el id). */
  templates: Template[] | null;
  errors: DraftErrors;
  disabled: boolean;
  /** La campana se envia por zona horaria: la prueba no se combina con ella. */
  timezoneLocked: boolean;
}

export function ABTestFields({
  draft,
  onChange,
  meta,
  templates,
  errors,
  disabled,
  timezoneLocked,
}: ABTestFieldsProps) {
  const setVariant = (i: number, patch: Partial<VariantDraft>) =>
    onChange({
      ...draft,
      variants: draft.variants.map((v, j) => (j === i ? { ...v, ...patch } : v)),
    });
  const templateOptions = [
    { value: '', label: t('campaigns.ab.campaignTemplate') },
    ...(templates ?? []).map((tpl) => ({ value: tpl.id, label: tpl.name })),
  ];
  const locked = disabled || timezoneLocked;
  return (
    <>
      <div className="cf-form__section">{t('campaigns.ab.title')}</div>
      <Checkbox
        label={t('campaigns.ab.enable')}
        checked={draft.enabled}
        disabled={locked}
        onChange={(e) => onChange({ ...draft, enabled: e.target.checked })}
      />
      <span className="cf-text-sm cf-text-secondary">
        {timezoneLocked ? t('campaigns.ab.timezoneLocked') : t('campaigns.ab.hint')}
      </span>
      {draft.enabled ? (
        <>
          <div className="cf-form__row">
            <FormField label={t('campaigns.ab.criterion')} htmlFor="campaign-ab-criterion">
              <Select
                id="campaign-ab-criterion"
                options={meta.ab_test.criteria.map((c) => ({
                  value: c,
                  label: tEnum('campaigns.ab.criterionOption', c),
                }))}
                value={draft.criterion}
                disabled={locked}
                onChange={(e) =>
                  onChange({ ...draft, criterion: e.target.value as ABDraft['criterion'] })
                }
              />
            </FormField>
            <FormField
              label={t('campaigns.ab.samplePercent')}
              htmlFor="campaign-ab-sample"
              error={errors.samplePercent}
              hint={t('campaigns.ab.sampleHint')}
            >
              <Input
                id="campaign-ab-sample"
                type="number"
                min={meta.ab_test.min_sample_percent}
                max={meta.ab_test.max_sample_percent}
                step={1}
                value={draft.samplePercent}
                disabled={locked}
                invalid={Boolean(errors.samplePercent)}
                onChange={(e) => onChange({ ...draft, samplePercent: e.target.value })}
              />
            </FormField>
            <FormField
              label={t('campaigns.ab.windowHours')}
              htmlFor="campaign-ab-window"
              error={errors.windowHours}
              hint={t('campaigns.ab.windowHint')}
            >
              <Input
                id="campaign-ab-window"
                type="number"
                step={1}
                value={draft.windowHours}
                disabled={locked}
                invalid={Boolean(errors.windowHours)}
                onChange={(e) => onChange({ ...draft, windowHours: e.target.value })}
              />
            </FormField>
          </div>
          {errors.variants ? (
            <span className="cf-field__error" role="alert">
              {errors.variants}
            </span>
          ) : null}
          {draft.variants.map((v, i) => {
            const label = variantLabel(i);
            return (
              <div key={label} className="cf-form__row">
                <FormField
                  label={t('campaigns.ab.variantSubject', { label })}
                  htmlFor={`campaign-ab-subject-${i}`}
                  error={errors[`variant.${i}.subject`]}
                  hint={t('campaigns.ab.subjectHint')}
                >
                  <Input
                    id={`campaign-ab-subject-${i}`}
                    maxLength={meta.max_subject_length}
                    value={v.subject}
                    disabled={locked}
                    invalid={Boolean(errors[`variant.${i}.subject`])}
                    onChange={(e) => setVariant(i, { subject: e.target.value })}
                  />
                </FormField>
                <FormField
                  label={t('campaigns.ab.variantTemplate', { label })}
                  htmlFor={`campaign-ab-template-${i}`}
                >
                  {templates ? (
                    <Select
                      id={`campaign-ab-template-${i}`}
                      options={templateOptions}
                      value={v.templateId}
                      disabled={locked}
                      onChange={(e) => setVariant(i, { templateId: e.target.value })}
                    />
                  ) : (
                    <Input
                      id={`campaign-ab-template-${i}`}
                      className="cf-mono"
                      placeholder={t('campaigns.form.templateId')}
                      value={v.templateId}
                      disabled={locked}
                      onChange={(e) => setVariant(i, { templateId: e.target.value })}
                    />
                  )}
                </FormField>
                <FormField
                  label={t('campaigns.form.version')}
                  htmlFor={`campaign-ab-version-${i}`}
                  error={errors[`variant.${i}.version`]}
                >
                  <Input
                    id={`campaign-ab-version-${i}`}
                    type="number"
                    min={1}
                    step={1}
                    value={v.version}
                    disabled={locked}
                    invalid={Boolean(errors[`variant.${i}.version`])}
                    onChange={(e) => setVariant(i, { version: e.target.value })}
                  />
                </FormField>
                {draft.variants.length > meta.ab_test.min_variants ? (
                  <Button
                    variant="ghost"
                    iconOnly
                    icon={<IconTrash size={16} />}
                    disabled={locked}
                    aria-label={t('campaigns.ab.removeVariant', { label })}
                    onClick={() =>
                      onChange({ ...draft, variants: draft.variants.filter((_, j) => j !== i) })
                    }
                  />
                ) : null}
              </div>
            );
          })}
          {draft.variants.length < meta.ab_test.max_variants ? (
            <div>
              <Button
                variant="ghost"
                icon={<IconPlus size={16} />}
                disabled={locked}
                onClick={() =>
                  onChange({
                    ...draft,
                    variants: [...draft.variants, { subject: '', templateId: '', version: '' }],
                  })
                }
              >
                {t('campaigns.ab.addVariant')}
              </Button>
            </div>
          ) : null}
        </>
      ) : null}
    </>
  );
}

interface ResendFieldsProps {
  draft: ResendDraft;
  onChange: (next: ResendDraft) => void;
  meta: CampaignsMeta;
  errors: DraftErrors;
  disabled: boolean;
}

export function ResendFields({ draft, onChange, meta, errors, disabled }: ResendFieldsProps) {
  return (
    <>
      <div className="cf-form__section">{t('campaigns.resend.title')}</div>
      <Checkbox
        label={t('campaigns.resend.enable')}
        checked={draft.enabled}
        disabled={disabled}
        onChange={(e) => onChange({ ...draft, enabled: e.target.checked })}
      />
      <span className="cf-text-sm cf-text-secondary">{t('campaigns.resend.hint')}</span>
      {draft.enabled ? (
        <div className="cf-form__row">
          <FormField
            label={t('campaigns.resend.subject')}
            htmlFor="campaign-resend-subject"
            required
            error={errors.resendSubject}
          >
            <Input
              id="campaign-resend-subject"
              maxLength={meta.max_subject_length}
              value={draft.subject}
              disabled={disabled}
              invalid={Boolean(errors.resendSubject)}
              onChange={(e) => onChange({ ...draft, subject: e.target.value })}
            />
          </FormField>
          <FormField
            label={t('campaigns.resend.delayHours')}
            htmlFor="campaign-resend-delay"
            error={errors.resendDelay}
            hint={t('campaigns.resend.delayHint')}
          >
            <Input
              id="campaign-resend-delay"
              type="number"
              step={1}
              value={draft.delayHours}
              disabled={disabled}
              invalid={Boolean(errors.resendDelay)}
              onChange={(e) => onChange({ ...draft, delayHours: e.target.value })}
            />
          </FormField>
        </div>
      ) : null}
    </>
  );
}
