import type { SegmentCatalog, SegmentMatch } from '@/api/segments';
import { Button, Checkbox, ChipsInput, Input, Select } from '@/design/components';
import { IconLayers, IconPlus, IconTrash } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import {
  arityOf,
  countRules,
  fieldSpecs,
  newCondition,
  newGroup,
  specFor,
  withField,
  withOperator,
  type DraftCondition,
  type DraftGroup,
  type DraftNode,
  type FieldSpec,
} from './segmentDraft';

export interface ListOption {
  id: string;
  name: string;
}

export interface SegmentEditorProps {
  catalog: SegmentCatalog;
  value: DraftGroup;
  onChange: (next: DraftGroup) => void;
  errors: Record<string, string>;
  /** Listas de la empresa para in_list; null si el rol no puede leerlas (se escribe el id). */
  lists: readonly ListOption[] | null;
  /** Campanas para las reglas de comportamiento; null sin permiso (se escribe el id). */
  campaigns?: readonly ListOption[] | null;
  disabled?: boolean;
}

// Los enumerados del catalogo son valores del dominio de contacts: se muestran con los
// mismos textos que en sus pantallas.
const ENUM_TEXTS: Record<string, string> = {
  status: 'contacts.status',
  source: 'contacts.source',
  consent: 'contacts.consentStatus',
};

function enumLabel(field: string, value: string): string {
  const prefix = ENUM_TEXTS[field];
  return prefix ? tEnum(prefix, value) : value;
}

function fieldLabel(spec: FieldSpec): string {
  return spec.attributeKey
    ? t('segments.editor.attribute', { key: spec.attributeKey })
    : tEnum('segments.field', spec.field);
}

export function SegmentEditor(props: SegmentEditorProps) {
  return (
    <GroupEditor
      {...props}
      group={props.value}
      depth={1}
      total={countRules(props.value)}
      onGroupChange={props.onChange}
    />
  );
}

interface GroupEditorProps extends Omit<SegmentEditorProps, 'value' | 'onChange'> {
  group: DraftGroup;
  depth: number;
  total: number;
  onGroupChange: (next: DraftGroup) => void;
  onRemove?: () => void;
}

function GroupEditor({
  catalog,
  group,
  depth,
  total,
  onGroupChange,
  onRemove,
  errors,
  lists,
  campaigns = null,
  disabled,
}: GroupEditorProps) {
  const { max_depth: maxDepth, max_rules: maxRules } = catalog.limits;
  const replace = (key: string, node: DraftNode) =>
    onGroupChange({ ...group, rules: group.rules.map((r) => (r.key === key ? node : r)) });
  const remove = (key: string) =>
    onGroupChange({ ...group, rules: group.rules.filter((r) => r.key !== key) });
  const matchId = `${group.key}-match`;
  const error = errors[group.key];

  return (
    <fieldset className="cf-segment-group" data-depth={Math.min(depth, 3)} disabled={disabled}>
      <legend className="cf-visually-hidden">
        {depth === 1 ? t('segments.editor.root') : t('segments.editor.group')}
      </legend>
      <div className="cf-segment-group__head">
        <label className="cf-field__label" htmlFor={matchId}>
          {t('segments.editor.match')}
        </label>
        <Select
          id={matchId}
          className="cf-segment-group__match"
          options={catalog.match.map((m) => ({ value: m, label: tEnum('segments.match', m) }))}
          value={group.match}
          onChange={(e) => onGroupChange({ ...group, match: e.target.value as SegmentMatch })}
        />
        {onRemove ? (
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconTrash size={14} />}
            onClick={onRemove}
          >
            {t('segments.editor.removeGroup')}
          </Button>
        ) : null}
      </div>
      {error ? (
        <span className="cf-field__error" role="alert">
          {error}
        </span>
      ) : null}
      <div className="cf-segment-group__rules">
        {group.rules.map((node) =>
          node.kind === 'group' ? (
            <GroupEditor
              key={node.key}
              catalog={catalog}
              group={node}
              depth={depth + 1}
              total={total}
              onGroupChange={(next) => replace(node.key, next)}
              onRemove={() => remove(node.key)}
              errors={errors}
              lists={lists}
              campaigns={campaigns}
              disabled={disabled}
            />
          ) : (
            <ConditionRow
              key={node.key}
              catalog={catalog}
              condition={node}
              error={errors[node.key]}
              lists={lists}
              campaigns={campaigns}
              onChange={(next) => replace(node.key, next)}
              onRemove={() => remove(node.key)}
            />
          ),
        )}
      </div>
      <div className="cf-inline">
        <Button
          size="sm"
          icon={<IconPlus size={14} />}
          disabled={total >= maxRules}
          onClick={() =>
            onGroupChange({ ...group, rules: [...group.rules, newCondition(catalog)] })
          }
        >
          {t('segments.editor.addCondition')}
        </Button>
        <Button
          size="sm"
          icon={<IconLayers size={14} />}
          disabled={depth >= maxDepth || total + 1 >= maxRules}
          onClick={() => onGroupChange({ ...group, rules: [...group.rules, newGroup(catalog)] })}
        >
          {t('segments.editor.addGroup')}
        </Button>
      </div>
    </fieldset>
  );
}

function ConditionRow({
  catalog,
  condition,
  error,
  lists,
  campaigns,
  onChange,
  onRemove,
}: {
  catalog: SegmentCatalog;
  condition: DraftCondition;
  error?: string;
  lists: readonly ListOption[] | null;
  campaigns: readonly ListOption[] | null;
  onChange: (next: DraftCondition) => void;
  onRemove: () => void;
}) {
  const specs = fieldSpecs(catalog);
  const spec = specFor(catalog, condition.field);
  const arity = arityOf(catalog, condition.op);
  const options = specs.map((s) => ({ value: s.field, label: fieldLabel(s) }));
  if (!spec && condition.field) {
    options.push({
      value: condition.field,
      label: t('segments.editor.unknownField', { field: condition.field }),
    });
  }

  return (
    <div className="cf-segment-rule">
      <div className="cf-segment-rule__row">
        <Select
          aria-label={t('segments.editor.field')}
          options={options}
          value={condition.field}
          onChange={(e) => onChange(withField(catalog, condition, e.target.value))}
          invalid={!spec}
        />
        <Select
          aria-label={t('segments.editor.operator')}
          options={(spec?.operators ?? []).map((op) => ({
            value: op,
            label: tEnum('segments.op', op),
          }))}
          value={condition.op}
          onChange={(e) => onChange(withOperator(catalog, condition, e.target.value))}
        />
        <div className="cf-segment-rule__value">
          {spec && arity !== 'none' ? (
            <ValueInput
              spec={spec}
              many={arity === 'many'}
              condition={condition}
              lists={lists}
              campaigns={campaigns}
              onChange={onChange}
              invalid={Boolean(error)}
            />
          ) : null}
        </div>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon={<IconTrash size={14} />}
          onClick={onRemove}
        >
          {t('segments.editor.removeCondition')}
        </Button>
      </div>
      {error ? (
        <span className="cf-field__error" role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}

const NUMBER = /^-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

function ValueInput({
  spec,
  many,
  condition,
  lists,
  campaigns,
  onChange,
  invalid,
}: {
  spec: FieldSpec;
  many: boolean;
  condition: DraftCondition;
  lists: readonly ListOption[] | null;
  campaigns: readonly ListOption[] | null;
  onChange: (next: DraftCondition) => void;
  invalid: boolean;
}) {
  const label = t('segments.editor.value');
  const setValue = (value: string) => onChange({ ...condition, value });
  const setValues = (values: string[]) => onChange({ ...condition, values });

  if (spec.valueKind === 'enum') {
    if (many) {
      return (
        <div className="cf-inline-list" role="group" aria-label={label}>
          {spec.values.map((v) => (
            <Checkbox
              key={v}
              label={enumLabel(spec.field, v)}
              checked={condition.values.includes(v)}
              onChange={(e) =>
                setValues(
                  e.target.checked
                    ? [...condition.values, v]
                    : condition.values.filter((x) => x !== v),
                )
              }
            />
          ))}
        </div>
      );
    }
    return (
      <Select
        aria-label={label}
        placeholder={t('common.select')}
        options={spec.values.map((v) => ({ value: v, label: enumLabel(spec.field, v) }))}
        value={condition.value}
        onChange={(e) => setValue(e.target.value)}
        invalid={invalid}
      />
    );
  }
  if (spec.valueKind === 'list' && lists) {
    return (
      <Select
        aria-label={label}
        placeholder={t('common.select')}
        options={lists.map((l) => ({ value: l.id, label: l.name }))}
        value={condition.value}
        onChange={(e) => setValue(e.target.value)}
        invalid={invalid}
      />
    );
  }
  if (spec.valueKind === 'campaign') {
    if (campaigns) {
      const options = campaigns.map((c) => ({ value: c.id, label: c.name }));
      if (condition.value && !options.some((o) => o.value === condition.value)) {
        options.push({ value: condition.value, label: condition.value });
      }
      return (
        <Select
          aria-label={label}
          placeholder={t('common.select')}
          options={options}
          value={condition.value}
          onChange={(e) => setValue(e.target.value)}
          invalid={invalid}
        />
      );
    }
    return (
      <Input
        aria-label={label}
        className="cf-mono"
        placeholder={t('segments.editor.campaignId')}
        value={condition.value}
        onChange={(e) => setValue(e.target.value)}
        invalid={invalid}
      />
    );
  }
  if (spec.valueKind === 'count') {
    return (
      <Input
        aria-label={tEnum('segments.countLabel', spec.field)}
        type="number"
        min={1}
        max={spec.max ?? undefined}
        step={1}
        value={condition.value}
        onChange={(e) => setValue(e.target.value)}
        invalid={invalid}
      />
    );
  }
  if (spec.valueKind === 'boolean') {
    return (
      <Select
        aria-label={label}
        placeholder={t('common.select')}
        options={[
          { value: 'true', label: t('common.yes') },
          { value: 'false', label: t('common.no') },
        ]}
        value={condition.value}
        onChange={(e) => setValue(e.target.value)}
        invalid={invalid}
      />
    );
  }
  const isDate = spec.valueKind === 'date' || spec.valueKind === 'timestamp';
  const isNumber = spec.valueKind === 'number';
  if (many) {
    const pattern = isDate ? DATE : isNumber ? NUMBER : null;
    return (
      <ChipsInput
        values={condition.values}
        onChange={setValues}
        normalize={(raw) => {
          const v = raw.trim();
          return v && (!pattern || pattern.test(v)) ? v : null;
        }}
        invalid={invalid}
        placeholder={label}
        removeLabel={(value) => t('common.removeValue', { value })}
        rejectedLabel={(list) => t('validation.invalidValues', { list: list.join(', ') })}
      />
    );
  }
  return (
    <Input
      aria-label={label}
      type={isDate ? 'date' : isNumber ? 'number' : 'text'}
      step={isNumber ? 'any' : undefined}
      className={spec.valueKind === 'list' ? 'cf-mono' : undefined}
      placeholder={spec.valueKind === 'list' ? t('segments.editor.listId') : undefined}
      value={condition.value}
      onChange={(e) => setValue(e.target.value)}
      invalid={invalid}
    />
  );
}
