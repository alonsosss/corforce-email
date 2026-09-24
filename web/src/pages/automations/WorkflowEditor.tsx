import { useMemo, useState } from 'react';
import type { AutomationsMeta, ConditionKind, StepType } from '@/api/automations';
import { Alert, Button, Checkbox, FormField, Input, Select, Textarea } from '@/design/components';
import { IconTrash } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { browserTimezones } from '@/lib/datetime';
import { FlowCanvas } from './FlowCanvas';
import { ConditionFields } from './ConditionFields';
import {
  conditionUsesStep,
  emptyStep,
  formatWait,
  graphErrors,
  withFreshId,
  type DraftErrors,
  type StepDraft,
  type WorkflowDraft,
} from './workflowDraft';
import { insertAt, portsOf, relink, removeNode, type Anchor, type Port } from './workflowGraph';
import { IdPicker } from './IdPicker';
import { namedSelectOptions, optionName, type WorkflowOptions } from './workflowOptions';

export interface WorkflowEditorProps {
  meta: AutomationsMeta;
  options: WorkflowOptions;
  draft: WorkflowDraft;
  onChange: (next: WorkflowDraft) => void;
  errors: DraftErrors;
  /** Sin permiso o fuera de borrador y pausa: se muestra la definicion sin editarla. */
  readOnly: boolean;
}

const TIMEZONES_LIST_ID = 'workflow-timezones';

/**
 * Editor de un flujo: datos, disparador (por evento o por fecha), filtro de lista, reentrada y
 * el lienzo con los pasos y sus ramas. El panel de abajo edita el paso elegido en el lienzo.
 */
export function WorkflowEditor({
  meta,
  options,
  draft,
  onChange,
  errors,
  readOnly,
}: WorkflowEditorProps) {
  const [selected, setSelected] = useState<string | null>(null);
  const timezones = useMemo(browserTimezones, []);
  const set = (patch: Partial<WorkflowDraft>) => onChange({ ...draft, ...patch });
  const setSteps = (steps: StepDraft[]) => set({ steps });
  const setStep = (key: string, patch: Partial<StepDraft>) =>
    setSteps(draft.steps.map((s) => (s.key === key ? { ...s, ...patch } : s)));
  const trigger = meta.trigger_types.find((tt) => tt.type === draft.triggerType);

  // Validacion en vivo del grafo; las de los campos llegan al guardar (errors).
  const live = graphErrors(draft, meta);
  const allErrors: DraftErrors = { ...errors, ...live };
  const invalid = new Set(
    draft.steps
      .filter((s) => Object.keys(allErrors).some((k) => k.startsWith(`${s.key}.`)))
      .map((s) => s.key),
  );
  const current = draft.steps.find((s) => s.key === selected) ?? null;
  const dateAttributes = options.catalog?.attributes.filter((a) => a.type === 'date') ?? null;

  const insert = (anchor: Anchor, type: StepType) => {
    const step = withFreshId(emptyStep(type, meta), draft.steps);
    setSteps(insertAt(draft.steps, anchor, step));
    setSelected(step.key);
  };

  const summary = (step: StepDraft): string => {
    switch (step.type) {
      case 'wait': {
        const unit = meta.wait.units.find((u) => u.unit === step.unit);
        const n = Number(step.amount);
        return unit && Number.isSafeInteger(n) && n > 0
          ? formatWait(n * unit.seconds, meta)
          : t('automations.canvas.incomplete');
      }
      case 'send_email':
        return step.templateId
          ? (options.templates?.find((tpl) => tpl.id === step.templateId)?.name ?? step.templateId)
          : t('automations.canvas.incomplete');
      case 'add_to_list':
      case 'remove_from_list':
        return step.listId
          ? optionName(options.lists, step.listId)
          : t('automations.canvas.incomplete');
      case 'branch':
        return step.conditionKind
          ? tEnum('automations.condition.kind', step.conditionKind)
          : t('automations.canvas.incomplete');
    }
    return '';
  };

  const triggerLabel = trigger
    ? trigger.date && draft.dateAttribute
      ? t('automations.canvas.dateTrigger', {
          attribute: draft.dateAttribute,
          hour: draft.hour,
        })
      : tEnum('automations.trigger', trigger.type)
    : t('automations.canvas.incomplete');

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
            onChange={(e) => {
              const next = meta.trigger_types.find((tt) => tt.type === e.target.value);
              // Un aniversario se repite cada ano: la reentrada es lo que se espera.
              set({
                triggerType: e.target.value,
                campaignId: '',
                reEntry: next?.date ? true : draft.reEntry,
              });
            }}
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
      {trigger?.date ? (
        <div className="cf-form__row">
          <FormField
            label={t('automations.editor.dateAttribute')}
            htmlFor="workflow-date-attribute"
            required
            error={errors.dateAttribute}
            hint={t('automations.editor.dateAttributeHint')}
          >
            {dateAttributes ? (
              <Select
                id="workflow-date-attribute"
                placeholder={t('common.select')}
                options={namedSelectOptions(
                  dateAttributes.map((a) => ({ id: a.key, name: a.key })),
                  draft.dateAttribute,
                )}
                value={draft.dateAttribute}
                onChange={(e) => set({ dateAttribute: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(errors.dateAttribute)}
              />
            ) : (
              <Input
                id="workflow-date-attribute"
                className="cf-mono"
                value={draft.dateAttribute}
                onChange={(e) => set({ dateAttribute: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(errors.dateAttribute)}
              />
            )}
          </FormField>
          <FormField
            label={t('automations.editor.hour')}
            htmlFor="workflow-hour"
            required
            error={errors.hour}
          >
            <Select
              id="workflow-hour"
              options={Array.from({ length: meta.limits.max_trigger_hour + 1 }, (_, h) => ({
                value: String(h),
                label: t('automations.editor.hourOption', { h: String(h).padStart(2, '0') }),
              }))}
              value={draft.hour}
              onChange={(e) => set({ hour: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(errors.hour)}
            />
          </FormField>
          <FormField
            label={t('automations.editor.timezone')}
            htmlFor="workflow-timezone"
            required
            error={errors.timezone}
            hint={t('automations.editor.timezoneHint')}
          >
            <Input
              id="workflow-timezone"
              value={draft.timezone}
              list={timezones.length ? TIMEZONES_LIST_ID : undefined}
              placeholder="America/Lima"
              onChange={(e) => set({ timezone: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(errors.timezone)}
              autoComplete="off"
            />
          </FormField>
          {timezones.length ? (
            <datalist id={TIMEZONES_LIST_ID}>
              {timezones.map((zone) => (
                <option key={zone} value={zone} />
              ))}
            </datalist>
          ) : null}
        </div>
      ) : null}
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
      <span className="cf-text-sm cf-text-secondary">
        {trigger?.date
          ? t('automations.editor.reEntryDateHint')
          : t('automations.editor.reEntryHint')}
      </span>

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
      {Object.keys(live).length ? (
        <Alert tone="warning" title={t('automations.canvas.issues')}>
          <ul className="cf-flow__issues">
            {draft.steps
              .filter((s) => live[`${s.key}.graph`])
              .map((s) => (
                <li key={s.key}>
                  <span className="cf-mono">{s.id}</span>: {live[`${s.key}.graph`]}
                </li>
              ))}
          </ul>
        </Alert>
      ) : null}
      <FlowCanvas
        steps={draft.steps}
        stepTypes={meta.step_types}
        triggerLabel={triggerLabel}
        selected={selected}
        onSelect={setSelected}
        onInsert={readOnly ? null : insert}
        summary={summary}
        invalid={invalid}
        maxSteps={meta.limits.max_steps}
      />
      {draft.steps.length === 0 ? (
        <span className="cf-text-sm cf-text-secondary">{t('automations.editor.noSteps')}</span>
      ) : null}
      {current ? (
        <section className="cf-step" aria-label={t('automations.canvas.selected')}>
          <div className="cf-step__head">
            <strong>
              {t('automations.step.title', {
                n: current.id,
                type: tEnum('automations.stepType', current.type),
              })}
            </strong>
            {readOnly ? null : (
              <div className="cf-step__actions">
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<IconTrash size={14} />}
                  onClick={() => {
                    setSteps(removeNode(draft.steps, current.key));
                    setSelected(null);
                  }}
                >
                  {current.type === 'branch'
                    ? t('automations.canvas.removeBranch')
                    : t('automations.canvas.remove')}
                </Button>
              </div>
            )}
          </div>
          {allErrors[`${current.key}.graph`] ? (
            <span className="cf-field__error" role="alert">
              {allErrors[`${current.key}.graph`]}
            </span>
          ) : null}
          <StepFields
            step={current}
            steps={draft.steps}
            meta={meta}
            options={options}
            errors={allErrors}
            readOnly={readOnly}
            onChange={(patch) => setStep(current.key, patch)}
          />
          <LinkFields
            step={current}
            steps={draft.steps}
            readOnly={readOnly}
            onRelink={(port, target) => setSteps(relink(draft.steps, current.key, port, target))}
          />
        </section>
      ) : draft.steps.length ? (
        <span className="cf-text-sm cf-text-secondary">{t('automations.canvas.selectHint')}</span>
      ) : null}
    </div>
  );
}

/** Destinos del paso: el siguiente, o en una rama el de si y el de no. Permite juntar ramas. */
function LinkFields({
  step,
  steps,
  readOnly,
  onRelink,
}: {
  step: StepDraft;
  steps: StepDraft[];
  readOnly: boolean;
  onRelink: (port: Port, target: string) => void;
}) {
  const targets = [
    { value: '', label: t('automations.canvas.end') },
    ...steps
      .filter((s) => s.key !== step.key)
      .map((s) => ({
        value: s.id,
        label: t('automations.canvas.stepOption', {
          id: s.id,
          type: tEnum('automations.stepType', s.type),
        }),
      })),
  ];
  return (
    <div className="cf-form__row">
      {portsOf(step).map((port) => (
        <FormField
          key={port}
          label={tEnum('automations.canvas.port', port)}
          htmlFor={`step-${step.key}-${port}`}
        >
          <Select
            id={`step-${step.key}-${port}`}
            options={targets}
            value={step[port]}
            onChange={(e) => onRelink(port, e.target.value)}
            disabled={readOnly}
          />
        </FormField>
      ))}
    </div>
  );
}

function StepFields({
  step,
  steps,
  meta,
  options,
  errors,
  readOnly,
  onChange,
}: {
  step: StepDraft;
  steps: StepDraft[];
  meta: AutomationsMeta;
  options: WorkflowOptions;
  errors: DraftErrors;
  readOnly: boolean;
  onChange: (patch: Partial<StepDraft>) => void;
}) {
  const id = (field: string) => `step-${step.key}-${field}`;
  const error = (field: string) => errors[`${step.key}.${field}`];

  if (step.type === 'branch') {
    return (
      <ConditionFields
        step={step}
        steps={steps}
        meta={meta}
        options={options}
        errors={errors}
        readOnly={readOnly}
        onChange={onChange}
        onKind={(kind: ConditionKind) =>
          onChange({
            conditionKind: kind,
            conditionStep: conditionUsesStep(meta, kind) ? '' : undefined,
            segmentId: '',
            attribute: '',
            op: '',
            value: '',
          })
        }
      />
    );
  }

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
