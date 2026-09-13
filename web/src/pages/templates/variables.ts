import type { TemplateVariable, VariableType, VariableValue } from '@/api/templates';
import { t } from '@/i18n';

// Declaracion y valores de las variables de una plantilla, espejo de
// services/templates/internal/domain/variables.go (ValidateDeclarations, coerce y
// checkString). El backend vuelve a validar al guardar y al renderizar.

const NAME = /^[a-z][a-z0-9_]{0,63}$/;
const NUMBER = /^-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?$/;
const EMAIL = /^[^\s@<>()",;:]+@[^\s@<>()",;:]+\.[^\s@<>()",;:]+$/;

/** Fila editable de una variable. defaultValue es texto; en boolean '', 'true' o 'false'. */
export interface VariableDraft {
  key: string;
  name: string;
  type: VariableType;
  required: boolean;
  defaultValue: string;
}

let sequence = 0;

export function newVariableDraft(type: VariableType = 'string'): VariableDraft {
  sequence += 1;
  return { key: `variable-${sequence}`, name: '', type, required: false, defaultValue: '' };
}

export function variablesToDrafts(variables: readonly TemplateVariable[] | null): VariableDraft[] {
  return (variables ?? []).map((v) => ({
    ...newVariableDraft(v.type),
    name: v.name,
    required: v.required,
    defaultValue: v.default === undefined ? '' : String(v.default),
  }));
}

export type Coerced = { ok: true; value: VariableValue } | { ok: false; error: string };

/** Convierte lo escrito al tipo JSON que espera el backend para esa variable. */
export function coerceValue(type: VariableType, raw: string | boolean): Coerced {
  if (type === 'boolean') {
    if (raw === true || raw === 'true') return { ok: true, value: true };
    if (raw === false || raw === 'false') return { ok: true, value: false };
    return { ok: false, error: t('templates.variables.expectBoolean') };
  }
  const text = typeof raw === 'string' ? raw : String(raw);
  if (type === 'number') {
    const trimmed = text.trim();
    const value = Number(trimmed);
    return NUMBER.test(trimmed) && Number.isFinite(value)
      ? { ok: true, value }
      : { ok: false, error: t('templates.variables.expectNumber') };
  }
  if (type === 'url') {
    try {
      const url = new URL(text.trim());
      if ((url.protocol === 'http:' || url.protocol === 'https:') && url.host) {
        return { ok: true, value: text.trim() };
      }
    } catch {
      // Se informa abajo con el mismo mensaje que cualquier otra URL invalida.
    }
    return { ok: false, error: t('templates.variables.expectUrl') };
  }
  if (type === 'email') {
    return EMAIL.test(text.trim())
      ? { ok: true, value: text.trim() }
      : { ok: false, error: t('templates.variables.expectEmail') };
  }
  return { ok: true, value: text };
}

export interface DraftsResult {
  variables: TemplateVariable[];
  /** Error por clave de fila. */
  errors: Record<string, string>;
}

export function draftsToVariables(drafts: readonly VariableDraft[]): DraftsResult {
  const variables: TemplateVariable[] = [];
  const errors: Record<string, string> = {};
  const seen = new Set<string>();
  for (const draft of drafts) {
    const name = draft.name.trim();
    if (!NAME.test(name)) {
      errors[draft.key] = t('templates.variables.invalidName');
      continue;
    }
    if (seen.has(name)) {
      errors[draft.key] = t('templates.variables.duplicate', { name });
      continue;
    }
    seen.add(name);
    const hasDefault = draft.defaultValue.trim() !== '';
    if (draft.required && hasDefault) {
      errors[draft.key] = t('templates.variables.requiredNoDefault');
      continue;
    }
    const variable: TemplateVariable = { name, type: draft.type, required: draft.required };
    if (hasDefault) {
      const coerced = coerceValue(draft.type, draft.defaultValue);
      if (!coerced.ok) {
        errors[draft.key] = coerced.error;
        continue;
      }
      variable.default = coerced.value;
    }
    variables.push(variable);
  }
  return { variables, errors };
}

/** Valores del formulario de previsualizacion: texto, o booleano en las casillas. */
export type ValueDraft = Record<string, string | boolean>;

export function initialValues(variables: readonly TemplateVariable[]): ValueDraft {
  const out: ValueDraft = {};
  for (const v of variables) {
    if (v.type === 'boolean') out[v.name] = v.default === true || v.default === 'true';
    else out[v.name] = v.default === undefined ? '' : String(v.default);
  }
  return out;
}

export interface RenderValuesResult {
  variables: Record<string, VariableValue>;
  errors: Record<string, string>;
}

/**
 * Cuerpo de variables para /preview. Un campo opcional vacio no viaja: el backend aplica
 * su valor por defecto o el cero de su tipo. Uno requerido vacio es un error.
 */
export function buildRenderVariables(
  declared: readonly TemplateVariable[],
  values: ValueDraft,
): RenderValuesResult {
  const variables: Record<string, VariableValue> = {};
  const errors: Record<string, string> = {};
  for (const v of declared) {
    const raw = values[v.name];
    if (v.type === 'boolean') {
      variables[v.name] = raw === true;
      continue;
    }
    const text = typeof raw === 'string' ? raw : '';
    if (!text.trim()) {
      if (v.required) errors[v.name] = t('validation.required');
      continue;
    }
    const coerced = coerceValue(v.type, text);
    if (coerced.ok) variables[v.name] = coerced.value;
    else errors[v.name] = coerced.error;
  }
  return { variables, errors };
}
