import type { AutomationsMeta, ConditionKind } from '@/api/automations';
import type { SegmentCatalog } from '@/api/segments';
import { Alert, FormField, Input, Select } from '@/design/components';
import { t, tEnum } from '@/i18n';
import { arityOf, convertValue, type FieldSpec } from '@/pages/segments/segmentDraft';
import { conditionUsesStep, type DraftErrors, type StepDraft } from './workflowDraft';
import { dominatingSends } from './workflowGraph';
import type { WorkflowOptions } from './workflowOptions';
import { IdPicker } from './IdPicker';

// Condicion de una rama. Las de atributo usan el catalogo del DSL de segmentos de contacts
// (atributos declarados y operadores por tipo): contacts la valida al activar y la evalua en
// cada ejecucion, asi que aqui no se copia ninguna regla. Sin permiso para leer el catalogo
// se escriben la clave, el operador y el valor JSON a mano.

export interface ConditionFieldsProps {
  step: StepDraft;
  steps: StepDraft[];
  meta: AutomationsMeta;
  options: WorkflowOptions;
  errors: DraftErrors;
  readOnly: boolean;
  onChange: (patch: Partial<StepDraft>) => void;
  onKind: (kind: ConditionKind) => void;
}

/** El valor JSON de la condicion desde el texto del editor, con el tipo del atributo. */
export function attributeValueJSON(
  catalog: SegmentCatalog,
  attribute: string,
  op: string,
  raw: string,
): { json: string } | { error: string } {
  const decl = catalog.attributes.find((a) => a.key === attribute);
  if (!decl) return { error: t('automations.condition.attributeUnknown') };
  const arity = arityOf(catalog, op);
  if (arity === 'none') return { json: '' };
  const spec: FieldSpec = {
    field: `${catalog.attribute_prefix}${attribute}`,
    attributeKey: attribute,
    valueKind: decl.type,
    operators: [],
    values: [],
    max: null,
  };
  const parts =
    arity === 'many'
      ? raw
          .split(',')
          .map((p) => p.trim())
          .filter(Boolean)
      : [raw];
  const values: unknown[] = [];
  for (const part of parts) {
    const converted = convertValue(catalog, spec, part);
    if (!converted.ok) return { error: converted.error };
    values.push(converted.value);
  }
  if (arity === 'many' && !values.length) return { error: t('segments.error.valuesRequired') };
  return { json: JSON.stringify(arity === 'many' ? values : values[0]) };
}

/** Texto editable de un valor JSON guardado. */
function valueText(json: string): string {
  if (!json) return '';
  try {
    const v: unknown = JSON.parse(json);
    return Array.isArray(v) ? v.map(String).join(', ') : String(v);
  } catch {
    return json;
  }
}

export function ConditionFields({
  step,
  steps,
  meta,
  options,
  errors,
  readOnly,
  onChange,
  onKind,
}: ConditionFieldsProps) {
  const id = (field: string) => `step-${step.key}-${field}`;
  const error = (field: string) => errors[`${step.key}.${field}`];
  const kind = step.conditionKind;
  const index = steps.findIndex((s) => s.key === step.key);
  const sends = dominatingSends(steps, index);
  const catalog = options.catalog;

  return (
    <div className="cf-stack">
      <FormField
        label={t('automations.condition.kindLabel')}
        htmlFor={id('kind')}
        required
        error={error('condition')}
        hint={kind ? tEnum('automations.condition.hint', kind) : undefined}
      >
        <Select
          id={id('kind')}
          placeholder={t('common.select')}
          options={meta.condition_kinds.map((c) => ({
            value: c.kind,
            label: tEnum('automations.condition.kind', c.kind),
          }))}
          value={kind}
          onChange={(e) => onKind(e.target.value as ConditionKind)}
          disabled={readOnly}
        />
      </FormField>
      {kind && conditionUsesStep(meta, kind) ? (
        <>
          <FormField
            label={t('automations.condition.step')}
            htmlFor={id('condition-step')}
            required
            error={error('conditionStep')}
            hint={t('automations.condition.stepHint')}
          >
            <Select
              id={id('condition-step')}
              placeholder={sends.length ? t('common.select') : t('automations.condition.noSends')}
              options={[
                ...sends.map((s) => ({
                  value: s.id,
                  label: t('automations.canvas.stepOption', {
                    id: s.id,
                    type: tEnum('automations.stepType', s.type),
                  }),
                })),
                ...(step.conditionStep && !sends.some((s) => s.id === step.conditionStep)
                  ? [{ value: step.conditionStep, label: step.conditionStep }]
                  : []),
              ]}
              value={step.conditionStep ?? ''}
              onChange={(e) => onChange({ conditionStep: e.target.value })}
              disabled={readOnly}
              invalid={Boolean(error('conditionStep'))}
            />
          </FormField>
          <Alert tone="info">{t('automations.condition.appleMail')}</Alert>
        </>
      ) : null}
      {kind === 'segment' ? (
        <FormField
          label={t('automations.condition.segment')}
          htmlFor={id('segment')}
          required
          error={error('segment')}
        >
          <IdPicker
            id={id('segment')}
            options={options.segments}
            value={step.segmentId}
            onChange={(segmentId) => onChange({ segmentId })}
            emptyLabel={t('common.select')}
            disabled={readOnly}
            invalid={Boolean(error('segment'))}
            required
          />
        </FormField>
      ) : null}
      {kind === 'attribute' ? (
        catalog ? (
          <AttributeCondition
            step={step}
            catalog={catalog}
            error={error}
            id={id}
            readOnly={readOnly}
            onChange={onChange}
          />
        ) : (
          <div className="cf-form__row">
            <FormField
              label={t('automations.condition.attribute')}
              htmlFor={id('attribute')}
              required
              error={error('attribute')}
            >
              <Input
                id={id('attribute')}
                className="cf-mono"
                value={step.attribute}
                onChange={(e) => onChange({ attribute: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(error('attribute'))}
              />
            </FormField>
            <FormField
              label={t('automations.condition.op')}
              htmlFor={id('op')}
              required
              error={error('op')}
            >
              <Input
                id={id('op')}
                className="cf-mono"
                value={step.op}
                onChange={(e) => onChange({ op: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(error('op'))}
              />
            </FormField>
            <FormField
              label={t('automations.condition.valueJson')}
              htmlFor={id('value')}
              error={error('value')}
            >
              <Input
                id={id('value')}
                className="cf-mono"
                value={step.value}
                onChange={(e) => onChange({ value: e.target.value })}
                disabled={readOnly}
                invalid={Boolean(error('value'))}
              />
            </FormField>
          </div>
        )
      ) : null}
    </div>
  );
}

function AttributeCondition({
  step,
  catalog,
  error,
  id,
  readOnly,
  onChange,
}: {
  step: StepDraft;
  catalog: SegmentCatalog;
  error: (field: string) => string | undefined;
  id: (field: string) => string;
  readOnly: boolean;
  onChange: (patch: Partial<StepDraft>) => void;
}) {
  const decl = catalog.attributes.find((a) => a.key === step.attribute);
  const ops = decl
    ? (catalog.attribute_types.find((x) => x.type === decl.type)?.operators ?? [])
    : [];
  const arity = step.op ? arityOf(catalog, step.op) : 'one';
  const setText = (raw: string) => {
    const built = attributeValueJSON(catalog, step.attribute, step.op, raw);
    onChange({ value: 'json' in built ? built.json : JSON.stringify(raw) });
  };
  return (
    <div className="cf-form__row">
      <FormField
        label={t('automations.condition.attribute')}
        htmlFor={id('attribute')}
        required
        error={error('attribute')}
      >
        <Select
          id={id('attribute')}
          placeholder={
            catalog.attributes.length ? t('common.select') : t('automations.condition.noAttributes')
          }
          options={catalog.attributes.map((a) => ({
            value: a.key,
            label: t('automations.condition.attributeOption', {
              key: a.key,
              type: tEnum('contacts.attributeType', a.type),
            }),
          }))}
          value={step.attribute}
          onChange={(e) => onChange({ attribute: e.target.value, op: '', value: '' })}
          disabled={readOnly}
          invalid={Boolean(error('attribute'))}
        />
      </FormField>
      <FormField
        label={t('automations.condition.op')}
        htmlFor={id('op')}
        required
        error={error('op')}
      >
        <Select
          id={id('op')}
          placeholder={t('common.select')}
          options={ops.map((op) => ({ value: op, label: tEnum('segments.op', op) }))}
          value={step.op}
          onChange={(e) => onChange({ op: e.target.value, value: '' })}
          disabled={readOnly || !decl}
          invalid={Boolean(error('op'))}
        />
      </FormField>
      {arity !== 'none' ? (
        <FormField
          label={t('automations.condition.value')}
          htmlFor={id('value')}
          error={error('value')}
          hint={arity === 'many' ? t('automations.condition.valuesHint') : undefined}
        >
          <Input
            id={id('value')}
            type={decl?.type === 'date' ? 'date' : 'text'}
            value={valueText(step.value)}
            onChange={(e) => setText(e.target.value)}
            disabled={readOnly || !step.op}
            invalid={Boolean(error('value'))}
          />
        </FormField>
      ) : null}
    </div>
  );
}
