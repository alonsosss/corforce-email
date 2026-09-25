import { ERROR_CODES, errorCode, errorDetail, errorDetailList } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import type {
  FilterLimits,
  Forwarding,
  MailFiltersInput,
  MailRule,
  RuleAction,
  RuleCondition,
} from '@/api/webmail';
import { t } from '@/i18n';
import { normalizeRecipient } from '../compose';
import { utf8Length } from '../format';

/** Errores por campo, con la misma ruta que details.field del servicio sin el prefijo. */
export type FieldErrors = Record<string, string>;

export function emptyCondition(): RuleCondition {
  return { field: 'from', op: 'contains', value: '' };
}

export function actionOfType(type: RuleAction['type'], folder = ''): RuleAction {
  switch (type) {
    case 'move':
      return { type, folder };
    case 'forward':
      return { type, address: '', keep_copy: true };
    default:
      return { type };
  }
}

export function emptyRule(folder = ''): MailRule {
  return {
    id: '',
    name: '',
    enabled: true,
    match: 'all',
    conditions: [emptyCondition()],
    actions: [actionOfType('move', folder)],
    stop: false,
  };
}

/** Cuerpo del PUT: solo los campos del contrato; una regla con id vacio es nueva. */
export function toFiltersInput(
  rules: readonly MailRule[],
  forwarding: Forwarding,
): MailFiltersInput {
  return { rules: [...rules], forwarding };
}

const chars = (value: string) => Array.from(value).length;

// Acciones que no tienen sentido junto a descartar: el directorio las rechaza.
const EXCLUDED_BY_DISCARD = new Set<RuleAction['type']>(['move', 'mark_read', 'flag']);

/**
 * Lo que el directorio rechazaria, con sus topes (limits de GET /filters). Las claves siguen
 * la forma de details.field: "conditions[0].value", "actions[1].address". ownAddress es el
 * buzon de la sesion: reenviarse a si mismo crearia un bucle y el servicio lo rechaza.
 */
export function ruleProblems(rule: MailRule, limits: FilterLimits, ownAddress = ''): FieldErrors {
  const problems: FieldErrors = {};
  if (!rule.name.trim()) problems.name = t('webmail.rules.nameRequired');
  else if (chars(rule.name.trim()) > limits.max_name_length) {
    problems.name = t('webmail.rules.tooLong', { max: limits.max_name_length });
  }

  if (rule.conditions.length === 0) problems.conditions = t('webmail.rules.conditionsRequired');
  else if (rule.conditions.length > limits.max_conditions) {
    problems.conditions = t('webmail.rules.tooManyConditions', { max: limits.max_conditions });
  }
  rule.conditions.forEach((condition, i) => {
    const key = `conditions[${i}].value`;
    if (!condition.value.trim()) problems[key] = t('webmail.rules.valueRequired');
    else if (chars(condition.value.trim()) > limits.max_value_length) {
      problems[key] = t('webmail.rules.tooLong', { max: limits.max_value_length });
    }
  });

  if (rule.actions.length === 0) problems.actions = t('webmail.rules.actionsRequired');
  else if (rule.actions.length > limits.max_actions) {
    problems.actions = t('webmail.rules.tooManyActions', { max: limits.max_actions });
  }
  const discards = rule.actions.some((a) => a.type === 'discard');
  const seen = new Set<string>();
  rule.actions.forEach((action, i) => {
    const address = action.type === 'forward' ? normalizeRecipient(action.address) : null;
    // Solo el reenvio se repite, y a direcciones distintas.
    const identity = action.type === 'forward' ? `forward:${address ?? i}` : action.type;
    if (seen.has(identity)) {
      problems[`actions[${i}].type`] = t('webmail.rules.actionRepeated');
    } else if (discards && EXCLUDED_BY_DISCARD.has(action.type)) {
      problems[`actions[${i}].type`] = t('webmail.rules.discardConflict');
    }
    seen.add(identity);
    if (action.type === 'move') {
      if (!action.folder) problems[`actions[${i}].folder`] = t('webmail.rules.folderRequired');
      else if (utf8Length(action.folder) > limits.max_folder_bytes) {
        problems[`actions[${i}].folder`] = t('webmail.rules.folderTooLong');
      }
    }
    if (action.type === 'forward') {
      if (!address) problems[`actions[${i}].address`] = t('webmail.rules.addressInvalid');
      else if (ownAddress && address.toLowerCase() === ownAddress.toLowerCase()) {
        problems[`actions[${i}].address`] = t('webmail.rules.forwardToSelf');
      }
    }
  });
  return problems;
}

export function forwardingProblems(forwarding: Forwarding, limits: FilterLimits): FieldErrors {
  const problems: FieldErrors = {};
  if (forwarding.enabled && forwarding.addresses.length === 0) {
    problems.addresses = t('webmail.forwarding.addressesRequired');
  } else if (forwarding.addresses.length > limits.max_forward_addresses) {
    problems.addresses = t('webmail.forwarding.tooMany', { max: limits.max_forward_addresses });
  }
  return problems;
}

/**
 * Un 422 del servicio con details.field dentro de `prefix` ("rules[2].", "forwarding.") se
 * pinta junto a su campo; cualquier otro error queda como error general.
 */
export function apiFieldError(err: unknown, prefix: string): FieldErrors | null {
  if (errorCode(err) !== ERROR_CODES.VALIDATION_ERROR) return null;
  const field = errorDetail(err, 'field');
  if (!field || !field.startsWith(prefix)) return null;
  const key = field.slice(prefix.length);
  // Una direccion concreta del reenvio se senala en la lista entera.
  const normalized = key.replace(/^addresses\[\d+\]$/, 'addresses');
  return { [normalized || 'general']: errorMessage(err) };
}

/**
 * Texto de un error al guardar reglas o reenvio. Si la empresa no permite reenviar fuera, se
 * nombran las direcciones rechazadas.
 */
export function filtersErrorMessage(err: unknown): string {
  if (errorCode(err) === ERROR_CODES.EXTERNAL_FORWARDING_DISABLED) {
    const addresses = errorDetailList(err, 'addresses');
    if (addresses.length) {
      return t('webmail.forwarding.externalDisabled', { list: addresses.join(', ') });
    }
  }
  return errorMessage(err);
}
