import type {
  AutomationsMeta,
  ConditionKind,
  StepType,
  WaitUnit,
  Workflow,
  WorkflowCondition,
  WorkflowRequest,
  WorkflowStep,
  WorkflowTrigger,
} from '@/api/automations';
import { t, tEnum } from '@/i18n';
import { rules, validateField } from '@/lib/validate';
import {
  freshId,
  linkLegacy,
  validateGraph,
  type GraphIssue,
  type GraphNode,
} from './workflowGraph';

// Borrador del editor de flujos. Tipos de paso, condiciones, unidades de espera, sus topes y
// los del flujo salen de GET /automations/meta; aqui no se copia ninguno. El grafo se valida
// en vivo con workflowGraph (la misma regla que el dominio). El servicio vuelve a validarlo
// todo y comprueba plantillas, segmentos y atributos al activar.

export interface StepDraft extends GraphNode {
  type: StepType;
  amount: string;
  unit: string;
  templateId: string;
  templateVersion: string;
  fromEmail: string;
  fromName: string;
  replyTo: string;
  listId: string;
  /** Rama: tipo de condicion y sus campos. */
  conditionKind: ConditionKind | '';
  segmentId: string;
  attribute: string;
  op: string;
  /** Valor de la condicion de atributo, en JSON (lo produce el editor desde el catalogo). */
  value: string;
}

export interface WorkflowDraft {
  name: string;
  description: string;
  triggerType: string;
  campaignId: string;
  /** Disparador por fecha: atributo, hora local y zona de respaldo. */
  dateAttribute: string;
  hour: string;
  timezone: string;
  listId: string;
  reEntry: boolean;
  steps: StepDraft[];
}

/** Errores por campo: name, trigger, campaign, list, steps, `${clave}.campo` y `${clave}.graph`. */
export type DraftErrors = Record<string, string>;

const POSITIVE_INTEGER = /^[1-9]\d*$/;
const HOUR = /^\d{1,2}$/;
const DURATION = /^([1-9]\d*)([a-z]+)$/;

let stepSeq = 0;
function nextStepKey(): string {
  stepSeq += 1;
  return `step-${stepSeq}`;
}

function unitOf(meta: AutomationsMeta, code: string): WaitUnit | undefined {
  return meta.wait.units.find((u) => u.unit === code);
}

/** Las esperas de marketing se piensan en la unidad mayor: es la que se propone. */
function defaultUnit(meta: AutomationsMeta): string {
  return meta.wait.units[meta.wait.units.length - 1]?.unit ?? '';
}

/** Un paso nuevo sin enlazar; id vacio hasta que se inserta (withFreshId). */
export function emptyStep(type: StepType, meta: AutomationsMeta): StepDraft {
  return {
    key: nextStepKey(),
    id: '',
    type,
    next: '',
    then: '',
    else: '',
    amount: '',
    unit: defaultUnit(meta),
    templateId: '',
    templateVersion: '',
    fromEmail: '',
    fromName: '',
    replyTo: '',
    listId: '',
    conditionKind: type === 'branch' ? (meta.condition_kinds[0]?.kind ?? '') : '',
    conditionStep: type === 'branch' && meta.condition_kinds[0]?.step ? '' : undefined,
    segmentId: '',
    attribute: '',
    op: '',
    value: '',
  };
}

/** El paso con un id libre en el flujo, listo para insertarlo. */
export function withFreshId(step: StepDraft, steps: readonly StepDraft[]): StepDraft {
  return { ...step, id: freshId(steps) };
}

export function emptyDraft(meta: AutomationsMeta): WorkflowDraft {
  return {
    name: '',
    description: '',
    triggerType: meta.trigger_types[0]?.type ?? '',
    campaignId: '',
    dateAttribute: '',
    hour: '9',
    timezone: '',
    listId: '',
    reEntry: false,
    steps: [],
  };
}

/** La condicion de una rama que mira un correo guarda el paso en conditionStep. */
export function conditionUsesStep(meta: AutomationsMeta, kind: string): boolean {
  return meta.condition_kinds.some((c) => c.kind === kind && c.step);
}

function stepFromApi(step: WorkflowStep, meta: AutomationsMeta): StepDraft {
  const base = emptyStep(step.type, meta);
  const wait = DURATION.exec(step.duration ?? '');
  const waitUnit = wait ? unitOf(meta, wait[2] ?? '') : undefined;
  const c = step.condition;
  return {
    ...base,
    id: step.id ?? '',
    next: step.next ?? '',
    then: step.then ?? '',
    else: step.else ?? '',
    amount: waitUnit ? (wait?.[1] ?? '') : '',
    unit: waitUnit ? waitUnit.unit : base.unit,
    templateId: step.template_id ?? '',
    templateVersion: step.template_version ? String(step.template_version) : '',
    fromEmail: step.from_email ?? '',
    fromName: step.from_name ?? '',
    replyTo: step.reply_to ?? '',
    listId: step.list_id ?? '',
    conditionKind: c?.kind ?? base.conditionKind,
    conditionStep: c && conditionUsesStep(meta, c.kind) ? (c.step ?? '') : undefined,
    segmentId: c?.segment_id ?? '',
    attribute: c?.attribute ?? '',
    op: c?.op ?? '',
    value: c?.value === undefined ? '' : JSON.stringify(c.value),
  };
}

export function draftFromWorkflow(workflow: Workflow, meta: AutomationsMeta): WorkflowDraft {
  const tr = workflow.trigger;
  return {
    name: workflow.name,
    description: workflow.description,
    triggerType: tr.type,
    campaignId: tr.campaign_id ?? '',
    dateAttribute: tr.attribute ?? '',
    hour: tr.hour === undefined ? '9' : String(tr.hour),
    timezone: tr.timezone ?? '',
    listId: workflow.list_id ?? '',
    reEntry: workflow.re_entry,
    steps: linkLegacy((workflow.steps ?? []).map((s) => stepFromApi(s, meta))),
  };
}

/** Espera legible en la mayor unidad del catalogo que la divide (90 d, 12 h, 1 min). */
export function formatWait(seconds: number, meta: AutomationsMeta): string {
  const units = [...meta.wait.units].sort((a, b) => b.seconds - a.seconds);
  const unit = units.find((u) => u.seconds > 0 && seconds % u.seconds === 0);
  if (!unit) return String(seconds);
  return t('automations.wait.amount', {
    n: seconds / unit.seconds,
    unit: tEnum('automations.waitUnitShort', unit.unit),
  });
}

/** Texto del problema de grafo de un paso. */
export function graphIssueText(issue: GraphIssue): string {
  switch (issue.kind) {
    case 'missingTarget':
      return t('automations.graph.missingTarget', { id: issue.target });
    case 'tooDeep':
      return t('automations.graph.tooDeep', { max: issue.max });
    default:
      return tEnum('automations.graph', issue.kind);
  }
}

function checkId(value: string, required: boolean): string | null {
  const id = value.trim();
  if (!id) return required ? t('validation.required') : null;
  return rules.uuid(id);
}

function positiveInteger(value: string): number | null {
  const text = value.trim();
  if (!POSITIVE_INTEGER.test(text)) return null;
  const n = Number(text);
  return Number.isSafeInteger(n) ? n : null;
}

function links(step: StepDraft): Pick<WorkflowStep, 'id' | 'next' | 'then' | 'else'> {
  const out: Pick<WorkflowStep, 'id' | 'next' | 'then' | 'else'> = { id: step.id };
  if (step.type === 'branch') {
    if (step.then) out.then = step.then;
    if (step.else) out.else = step.else;
  } else if (step.next) {
    out.next = step.next;
  }
  return out;
}

function buildCondition(
  step: StepDraft,
  meta: AutomationsMeta,
  flag: (field: string, message: string | null) => void,
): WorkflowCondition | null {
  const kind = step.conditionKind;
  if (!kind || !meta.condition_kinds.some((c) => c.kind === kind)) {
    flag('condition', t('validation.required'));
    return null;
  }
  if (conditionUsesStep(meta, kind)) {
    if (!step.conditionStep) {
      flag('conditionStep', t('validation.required'));
      return null;
    }
    return { kind, step: step.conditionStep };
  }
  if (kind === 'segment') {
    const err = checkId(step.segmentId, true);
    flag('segment', err);
    return err ? null : { kind, segment_id: step.segmentId.trim() };
  }
  const attribute = step.attribute.trim();
  flag('attribute', attribute ? null : t('validation.required'));
  flag('op', step.op.trim() ? null : t('validation.required'));
  let value: unknown;
  if (step.value.trim()) {
    try {
      value = JSON.parse(step.value);
    } catch {
      flag('value', t('automations.condition.valueInvalid'));
      return null;
    }
    if (new TextEncoder().encode(step.value).length > meta.limits.max_condition_value_bytes) {
      flag('value', t('automations.condition.valueTooLong'));
      return null;
    }
  }
  if (!attribute || !step.op.trim()) return null;
  return { kind, attribute, op: step.op.trim(), ...(value === undefined ? {} : { value }) };
}

function buildStep(
  step: StepDraft,
  meta: AutomationsMeta,
  errors: DraftErrors,
): WorkflowStep | null {
  const found: DraftErrors = {};
  const flag = (field: string, message: string | null) => {
    if (message) found[`${step.key}.${field}`] = message;
  };
  let out: WorkflowStep | null = null;
  switch (step.type) {
    case 'wait': {
      const amount = positiveInteger(step.amount);
      const unit = unitOf(meta, step.unit);
      if (amount === null || !unit) {
        flag('amount', t('automations.wait.invalid'));
        break;
      }
      const seconds = amount * unit.seconds;
      if (
        !Number.isSafeInteger(seconds) ||
        seconds < meta.wait.min_seconds ||
        seconds > meta.wait.max_seconds
      ) {
        flag(
          'amount',
          t('automations.wait.range', {
            min: formatWait(meta.wait.min_seconds, meta),
            max: formatWait(meta.wait.max_seconds, meta),
          }),
        );
        break;
      }
      out = { ...links(step), type: step.type, duration: `${amount}${unit.unit}` };
      break;
    }
    case 'send_email': {
      flag('template', checkId(step.templateId, true));
      const version = step.templateVersion.trim() ? positiveInteger(step.templateVersion) : null;
      if (step.templateVersion.trim() && version === null) {
        flag('version', t('automations.step.versionInvalid'));
      }
      flag('fromEmail', validateField(step.fromEmail, rules.required, rules.email));
      flag('fromName', validateField(step.fromName, rules.maxLength(meta.limits.max_name_length)));
      flag('replyTo', step.replyTo.trim() ? validateField(step.replyTo, rules.email) : null);
      const fromName = step.fromName.trim();
      const replyTo = step.replyTo.trim();
      out = {
        ...links(step),
        type: step.type,
        template_id: step.templateId.trim(),
        ...(version !== null ? { template_version: version } : {}),
        from_email: step.fromEmail.trim(),
        ...(fromName ? { from_name: fromName } : {}),
        ...(replyTo ? { reply_to: replyTo } : {}),
      };
      break;
    }
    case 'add_to_list':
    case 'remove_from_list':
      flag('list', checkId(step.listId, true));
      out = { ...links(step), type: step.type, list_id: step.listId.trim() };
      break;
    case 'branch': {
      const condition = buildCondition(step, meta, flag);
      if (condition) out = { ...links(step), type: step.type, condition };
      break;
    }
  }
  Object.assign(errors, found);
  return Object.keys(found).length ? null : out;
}

function buildTrigger(
  draft: WorkflowDraft,
  meta: AutomationsMeta,
  errors: DraftErrors,
): WorkflowTrigger | null {
  const trigger = meta.trigger_types.find((tt) => tt.type === draft.triggerType);
  if (!trigger) {
    errors.trigger = t('validation.required');
    return null;
  }
  if (trigger.campaign_filter) {
    const err = checkId(draft.campaignId, false);
    if (err) errors.campaign = err;
    const campaignId = draft.campaignId.trim();
    return campaignId ? { type: trigger.type, campaign_id: campaignId } : { type: trigger.type };
  }
  if (!trigger.date) return { type: trigger.type };
  const attribute = draft.dateAttribute.trim();
  if (!attribute) errors.dateAttribute = t('validation.required');
  const hourText = draft.hour.trim();
  const hour = Number(hourText);
  if (!HOUR.test(hourText) || hour > meta.limits.max_trigger_hour) {
    errors.hour = t('automations.editor.hourInvalid', { max: meta.limits.max_trigger_hour });
  }
  const timezone = draft.timezone.trim();
  if (!timezone) errors.timezone = t('validation.required');
  return { type: trigger.type, attribute, hour, timezone };
}

export interface BuildResult {
  request: WorkflowRequest | null;
  errors: DraftErrors;
}

/** Problemas del grafo por clave de paso, en texto (validacion en vivo del lienzo). */
export function graphErrors(draft: WorkflowDraft, meta: AutomationsMeta): DraftErrors {
  const out: DraftErrors = {};
  for (const [key, issue] of validateGraph(linkLegacy(draft.steps), meta.limits.max_depth)) {
    out[`${key}.graph`] = graphIssueText(issue);
  }
  return out;
}

/** Convierte el borrador en la peticion del API; con errores, request es null. */
export function buildRequest(draft: WorkflowDraft, meta: AutomationsMeta): BuildResult {
  const errors: DraftErrors = {};
  const flag = (field: string, message: string | null) => {
    if (message) errors[field] = message;
  };
  flag(
    'name',
    validateField(draft.name.trim(), rules.required, rules.maxLength(meta.limits.max_name_length)),
  );
  flag(
    'description',
    validateField(draft.description.trim(), rules.maxLength(meta.limits.max_description_length)),
  );
  const trigger = buildTrigger(draft, meta, errors);
  flag('list', checkId(draft.listId, false));
  const count = draft.steps.length;
  if (count < meta.limits.min_steps || count > meta.limits.max_steps) {
    errors.steps = t('automations.steps.count', {
      min: meta.limits.min_steps,
      max: meta.limits.max_steps,
    });
  }
  const linked = linkLegacy(draft.steps);
  Object.assign(errors, graphErrors({ ...draft, steps: linked }, meta));
  const steps: WorkflowStep[] = [];
  for (const step of linked) {
    const built = buildStep(step, meta, errors);
    if (built) steps.push(built);
  }
  if (!trigger || Object.keys(errors).length) return { request: null, errors };
  return {
    request: {
      name: draft.name.trim(),
      description: draft.description.trim(),
      trigger,
      list_id: draft.listId.trim() || null,
      re_entry: draft.reEntry,
      steps,
    },
    errors,
  };
}
