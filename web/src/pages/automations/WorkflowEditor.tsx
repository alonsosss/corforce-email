import { useState } from 'react';
import type { AutomationsMeta, StepType } from '@/api/automations';
import { Button, Checkbox, FormField, Input, Select, Textarea } from '@/design/components';
import { IconChevronDown, IconChevronUp, IconPlus, IconTrash } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import {
  emptyStep,
  formatWait,
  moveStep,
  type DraftErrors,
  type StepDraft,
  type WorkflowDraft,
} from './workflowDraft';
import { namedSelectOptions, type NamedOption, type WorkflowOptions } from './workflowOptions';

export interface WorkflowEditorProps {
  meta: AutomationsMeta;
  options: WorkflowOptions;
  draft: WorkflowDraft;
  onChange: (next: WorkflowDraft) => void;
  errors: DraftErrors;
  /** Sin permiso o fuera de borrador y pausa: se muestra la definicion sin editarla. */
  readOnly: boolean;
}

/** Editor de un flujo lineal: datos, disparador, filtro de lista, reentrada y pasos. */
export function WorkflowEditor({
  meta,
  options,
  draft,
  onChange,
  errors,
  readOnly,
}: WorkflowEditorProps) {
  const [newStepType, setNewStepType] = useState<StepType | ''>(meta.step_types[0] ?? '');
  const set = (patch: Partial<WorkflowDraft>) => onChange({ ...draft, ...patch });
  const setStep = (key: string, patch: Partial<StepDraft>) =>
    set({ steps: draft.steps.map((s) => (s.key === key ? { ...s, ...patch } : s)) });
  const trigger = meta.trigger_types.find((tt) => tt.type === draft.triggerType);
  const atMax = draft.steps.length >= meta.limits.max_steps;

  return (
    <div className="cf-form">
      <div className="cf-form__row">
        <FormField label={t('common.name')} htmlFor="workflow-name" required error={errors.name}>
          <Input
            id="workflow-name"
            value={draft.name}
            maxLength={meta.limits.max_name_length}
            onChange={(e) => set({ name: e.target.value })}
            disabled={readOnly}
            invalid={Boolean(errors.name)}
          />
        </FormField>
        <FormField
          label={t('automations.editor.trigger')}
          htmlFor="workflow-trigger"
          required
          error={errors.trigger}
          hint={trigger ? tEnum('automations.triggerHint', trigger.type) : undefined}
        >
          <Select
            id="workflow-trigger"
            placeholder={t('common.select')}
            options={meta.trigger_types.map((tt) => ({
              value: tt.type,
              label: tEnum('automations.trigger', tt.type),
            }))}
            value={draft.triggerType}
            onChange={(e) => set({ triggerType: e.target.value, campaignId: '' })}
            disabled={readOnly}
            invalid={Boolean(errors.trigger)}
          />
        </FormField>
      </div>
      <FormField
        label={t('common.description')}
        htmlFor="workflow-description"
        error={errors.description}
      >
        <Textarea
          id="workflow-description"
          rows={2}
          value={draft.description}
          maxLength={meta.limits.max_description_length}
          onChange={(e) => set({ description: e.target.value })}
          disabled={readOnly}
          invalid={Boolean(errors.description)}
        />
      </FormField>
      <div className="cf-form__row">
        {trigger?.campaign_filter ? (
          <FormField
            label={t('automations.editor.campaign')}
            htmlFor="workflow-campaign"
            error={errors.campaign}
            hint={t('automations.editor.campaignHint')}
          >
            <IdPicker
              id="workflow-campaign"
              options={options.campaigns}
              value={draft.campaignId}
              onChange={(campaignId) => set({ campaignId })}
              emptyLabel={t('automations.editor.anyCampaign')}
              disabled={readOnly}
              invalid={Boolean(errors.campaign)}
            />
          </FormField>
        ) : null}
        <FormField
          label={t('automations.editor.list')}
          htmlFor="workflow-list"
          error={errors.list}
          hint={t('automations.editor.listHint')}
        >
          <IdPicker
            id="workflow-list"
            options={options.lists}
            value={draft.listId}
            onChange={(listId) => set({ listId })}
            emptyLabel={t('automations.editor.noList')}
            disabled={readOnly}
            invalid={Boolean(errors.list)}
          />
        </FormField>
      </div>
      <Checkbox
        label={t('automations.editor.reEntry')}
        checked={draft.reEntry}
        onChange={(e) => set({ reEntry: e.target.checked })}
        disabled={readOnly}
      />
      <span className="cf-text-sm cf-text-secondary">{t('automations.editor.reEntryHint')}</span>

      <div className="cf-form__section">
        {t('automations.editor.steps', {
          n: draft.steps.length,
          max: meta.limits.max_steps,
        })}
      </div>
      {errors.steps ? (
        <div className="cf-form__error" role="alert">
          {errors.steps}
        </div>
      ) : null}
      {draft.steps.length === 0 ? (
        <span className="cf-text-sm cf-text-secondary">{t('automations.editor.noSteps')}</span>
      ) : (
        <ol className="cf-steps" aria-label={t('automations.editor.stepsLabel')}>
          {draft.steps.map((step, index) => (
            <li key={step.key} className="cf-step">
              <div className="cf-step__head">
                <strong>
                  {t('automations.step.title', {
                    n: index + 1,
                    type: tEnum('automations.stepType', step.type),
                  })}
                </strong>
                {readOnly ? null : (
                  <div className="cf-step__actions">
                    <Button
                      size="sm"
                      variant="ghost"
                      iconOnly
                      icon={<IconChevronUp size={14} />}
                      disabled={index === 0}
                      onClick={() => set({ steps: moveStep(draft.steps, index, -1) })}
                    >
                      {t('automations.step.moveUp', { n: index + 1 })}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      iconOnly
                      icon={<IconChevronDown size={14} />}
                      disabled={index === draft.steps.length - 1}
                      onClick={() => set({ steps: moveStep(draft.steps, index, 1) })}
                    >
                      {t('automations.step.moveDown', { n: index + 1 })}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      iconOnly
                      icon={<IconTrash size={14} />}
                      onClick={() => set({ steps: draft.steps.filter((s) => s.key !== step.key) })}
                    >
                      {t('automations.step.remove', { n: index + 1 })}
                    </Button>
                  </div>
                )}
              </div>
              <StepFields
                step={step}
                meta={meta}
                options={options}
                errors={errors}
                readOnly={readOnly}
                onChange={(patch) => setStep(step.key, patch)}
              />
            </li>
          ))}
        </ol>
      )}
      {readOnly ? null : (
        <div className="cf-form__row">
          <FormField
            label={t('automations.editor.newStep')}
            htmlFor="workflow-new-step"
            hint={
              atMax ? t('automations.editor.maxSteps', { max: meta.limits.max_steps }) : undefined
            }
          >
            <Select
              id="workflow-new-step"
              options={meta.step_types.map((type) => ({
                value: type,
                label: tEnum('automations.stepType', type),
              }))}
              value={newStepType}
              onChange={(e) => setNewStepType(e.target.value as StepType)}
              disabled={atMax}
            />
          </FormField>
          <div className="cf-field" style={{ justifyContent: 'flex-end' }}>
            <Button
              icon={<IconPlus size={16} />}
              disabled={atMax || !newStepType}
              onClick={() => {
                if (newStepType) set({ steps: [...draft.steps, emptyStep(newStepType, meta)] });
              }}
            >
              {t('automations.editor.addStep')}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/** Selector por nombre si el rol puede leer la coleccion; si no, el identificador a mano. */
function IdPicker({
  id,
  options,
  value,
  onChange,
  emptyLabel,
  disabled,
  invalid,
  required,
}: {
  id: string;
  options: NamedOption[] | null;
  value: string;
  onChange: (value: string) => void;
  emptyLabel: string;
  disabled: boolean;
  invalid: boolean;
  required?: boolean;
}) {
  if (options) {
    return (
      <Select
        id={id}
        placeholder={emptyLabel}
        options={namedSelectOptions(options, value)}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        invalid={invalid}
        required={required}
      />
    );
  }
  return (
    <Input
      id={id}
      className="cf-mono"
      placeholder={t('automations.editor.idPlaceholder')}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      invalid={invalid}
      autoComplete="off"
      spellCheck={false}
    />
  );
}

function StepFields({
  step,
  meta,
  options,
  errors,
  readOnly,
  onChange,
}: {
  step: StepDraft;
  meta: AutomationsMeta;
  options: WorkflowOptions;
  errors: DraftErrors;
  readOnly: boolean;
  onChange: (patch: Partial<StepDraft>) => void;
}) {
  const id = (field: string) => `step-${step.key}-${field}`;
  const error = (field: string) => errors[`${step.key}.${field}`];

  if (step.type === 'wait') {
    return (
      <div className="cf-form__row">
        <FormField
          label={t('automations.wait.amountLabel')}
          htmlFor={id('amount')}
          required
          error={error('amount')}
          hint={t('automations.wait.hint', {
            min: formatWait(meta.wait.min_seconds, meta),
            max: formatWait(meta.wait.max_seconds, meta),
          })}
        >
          <Input
            id={id('amount')}
            inputMode="numeric"
            value={step.amount}
            onChange={(e) => onChange({ amount: e.target.value })}
            disabled={readOnly}
            invalid={Boolean(error('amount'))}
          />
        </FormField>
        <FormField label={t('automations.wait.unitLabel')} htmlFor={id('unit')} required>
          <Select
            id={id('unit')}
            options={meta.wait.units.map((u) => ({
              value: u.unit,
              label: tEnum('automations.waitUnit', u.unit),
            }))}
            value={step.unit}
            onChange={(e) => onChange({ unit: e.target.value })}
            disabled={readOnly}
          />
        </FormField>
      </div>
    );
  }

  if (step.type === 'send_email') {
    const templates = options.templates;
    const templateOptions = (templates ?? []).map((tpl) => ({
      value: tpl.id,
      label:
        tpl.current_version > 0
          ? t('automations.step.templateOption', { name: tpl.name, n: tpl.current_version })
          : t('automations.step.templateUnpublished', { name: tpl.name }),
    }));
    if (step.templateId && !templateOptions.some((o) => o.value === step.templateId)) {
      templateOptions.push({
        value: step.templateId,
        label: t('automations.step.templateOther', { id: step.templateId }),
      });
    }
    return (
      <>
        <div className="cf-form__row">
          <FormField
            label={t('automations.step.template')}
            htmlFor={id('template')}
            required
            error={error('template')}
            hint={t('automations.step.templateHint')}
          >
            {templates ? (
              <Select
                id={id('template')}
                placeholder={t('common.select')}
                options={templateOptions}
                value={step.templateId}
                onChange={(e) => onChange({ templateId: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(error('template'))}
              />
            ) : (
              <Input
                id={id('template')}
                className="cf-mono"
                placeholder={t('automations.editor.idPlaceholder')}
                value={step.templateId}
                onChange={(e) => onChange({ templateId: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(error('template'))}
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </FormField>
          <FormField
            label={t('automations.step.version')}
            htmlFor={id('version')}
            error={error('version')}
            hint={t('automations.step.versionHint')}
          >
            <Input
              id={id('version')}
              inputMode="numeric"
              value={step.templateVersion}
              onChange={(e) => onChange({ templateVersion: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(error('version'))}
            />
          </FormField>
        </div>
        <div className="cf-form__row">
          <FormField
            label={t('automations.step.fromEmail')}
            htmlFor={id('from-email')}
            required
            error={error('fromEmail')}
            hint={t('automations.step.fromEmailHint')}
          >
            <Input
              id={id('from-email')}
              type="email"
              className="cf-mono"
              value={step.fromEmail}
              onChange={(e) => onChange({ fromEmail: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(error('fromEmail'))}
            />
          </FormField>
          <FormField
            label={t('automations.step.fromName')}
            htmlFor={id('from-name')}
            error={error('fromName')}
          >
            <Input
              id={id('from-name')}
              value={step.fromName}
              maxLength={meta.limits.max_name_length}
              onChange={(e) => onChange({ fromName: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(error('fromName'))}
            />
          </FormField>
          <FormField
            label={t('automations.step.replyTo')}
            htmlFor={id('reply-to')}
            error={error('replyTo')}
          >
            <Input
              id={id('reply-to')}
              type="email"
              className="cf-mono"
              value={step.replyTo}
              onChange={(e) => onChange({ replyTo: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(error('replyTo'))}
            />
          </FormField>
        </div>
      </>
    );
  }

  return (
    <FormField
      label={t('automations.step.list')}
      htmlFor={id('list')}
      required
      error={error('list')}
      hint={tEnum('automations.stepHint', step.type)}
    >
      <IdPicker
        id={id('list')}
        options={options.lists}
        value={step.listId}
        onChange={(listId) => onChange({ listId })}
        emptyLabel={t('common.select')}
        disabled={readOnly}
        invalid={Boolean(error('list'))}
        required
      />
    </FormField>
  );
}
