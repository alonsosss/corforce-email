import type {
  AutomationsMeta,
  StepType,
  WaitUnit,
  Workflow,
  WorkflowRequest,
  WorkflowStep,
} from '@/api/automations';
import { t, tEnum } from '@/i18n';
import { rules, validateField } from '@/lib/validate';

// Borrador del editor de flujos. Tipos de paso, unidades de espera, sus topes y los del
// flujo salen de GET /automations/meta; aqui no se copia ninguno. El servicio vuelve a
// validarlo todo y comprueba las plantillas al activar.

export interface StepDraft {
  /** Clave estable para React; no viaja al API. */
  key: string;
  type: StepType;
  amount: string;
  unit: string;
  templateId: string;
  templateVersion: string;
  fromEmail: string;
  fromName: string;
  replyTo: string;
  listId: string;
}

export interface WorkflowDraft {
  name: string;
  description: string;
  triggerType: string;
  campaignId: string;
  listId: string;
  reEntry: boolean;
  steps: StepDraft[];
}

/** Errores por campo: name, description, trigger, campaign, list, steps y `${clave}.campo`. */
export type DraftErrors = Record<string, string>;

const POSITIVE_INTEGER = /^[1-9]\d*$/;
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

export function emptyStep(type: StepType, meta: AutomationsMeta): StepDraft {
  return {
    key: nextStepKey(),
    type,
    amount: '',
    unit: defaultUnit(meta),
    templateId: '',
    templateVersion: '',
    fromEmail: '',
    fromName: '',
    replyTo: '',
    listId: '',
  };
}

export function emptyDraft(meta: AutomationsMeta): WorkflowDraft {
  return {
    name: '',
    description: '',
    triggerType: meta.trigger_types[0]?.type ?? '',
    campaignId: '',
    listId: '',
    reEntry: false,
    steps: [],
  };
}

function stepFromApi(step: WorkflowStep, meta: AutomationsMeta): StepDraft {
  const base = emptyStep(step.type, meta);
  const wait = DURATION.exec(step.duration ?? '');
  const waitUnit = wait ? unitOf(meta, wait[2] ?? '') : undefined;
  return {
    ...base,
    amount: waitUnit ? (wait?.[1] ?? '') : '',
    unit: waitUnit ? waitUnit.unit : base.unit,
    templateId: step.template_id ?? '',
    templateVersion: step.template_version ? String(step.template_version) : '',
    fromEmail: step.from_email ?? '',
    fromName: step.from_name ?? '',
    replyTo: step.reply_to ?? '',
    listId: step.list_id ?? '',
  };
}

export function draftFromWorkflow(workflow: Workflow, meta: AutomationsMeta): WorkflowDraft {
  return {
    name: workflow.name,
    description: workflow.description,
    triggerType: workflow.trigger.type,
    campaignId: workflow.trigger.campaign_id ?? '',
    listId: workflow.list_id ?? '',
    reEntry: workflow.re_entry,
    steps: (workflow.steps ?? []).map((s) => stepFromApi(s, meta)),
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

export function moveStep(steps: readonly StepDraft[], index: number, delta: -1 | 1): StepDraft[] {
  const target = index + delta;
  const out = [...steps];
  const current = out[index];
  const other = out[target];
  if (current === undefined || other === undefined) return out;
  out[index] = other;
  out[target] = current;
  return out;
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
      out = { type: step.type, duration: `${amount}${unit.unit}` };
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
      out = { type: step.type, list_id: step.listId.trim() };
      break;
  }
  Object.assign(errors, found);
  return Object.keys(found).length ? null : out;
}

export interface BuildResult {
  request: WorkflowRequest | null;
  errors: DraftErrors;
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
  const trigger = meta.trigger_types.find((tt) => tt.type === draft.triggerType);
  if (!trigger) errors.trigger = t('validation.required');
  if (trigger?.campaign_filter) flag('campaign', checkId(draft.campaignId, false));
  flag('list', checkId(draft.listId, false));
  const count = draft.steps.length;
  if (count < meta.limits.min_steps || count > meta.limits.max_steps) {
    errors.steps = t('automations.steps.count', {
      min: meta.limits.min_steps,
      max: meta.limits.max_steps,
    });
  }
  const steps: WorkflowStep[] = [];
  for (const step of draft.steps) {
    const built = buildStep(step, meta, errors);
    if (built) steps.push(built);
  }
  if (!trigger || Object.keys(errors).length) return { request: null, errors };
  const campaignId = trigger.campaign_filter ? draft.campaignId.trim() : '';
  return {
    request: {
      name: draft.name.trim(),
      description: draft.description.trim(),
      trigger: campaignId
        ? { type: trigger.type, campaign_id: campaignId }
        : { type: trigger.type },
      list_id: draft.listId.trim() || null,
      re_entry: draft.reEntry,
      steps,
    },
    errors,
  };
}
