import type {
  FieldType,
  ListItem,
  ScalarValue,
  TemplateField,
  TemplateVariable,
  VariableType,
  VariableValue,
} from '@/api/templates';
import { t } from '@/i18n';

// Declaracion y valores de las variables de una plantilla, espejo de
// services/templates/internal/domain/variables.go (ValidateDeclarations, coerce y
// checkString). El backend vuelve a validar al guardar y al renderizar.

const NAME = /^[a-z][a-z0-9_]{0,63}$/;
const NUMBER = /^-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?$/;
const EMAIL = /^[^\s@<>()",;:]+@[^\s@<>()",;:]+\.[^\s@<>()",;:]+$/;

/** Campo editable de una variable de tipo lista. */
export interface FieldDraft {
  key: string;
  name: string;
  type: FieldType;
  required: boolean;
}

/**
 * Fila editable de una variable. defaultValue es texto; en boolean '', 'true' o 'false'.
 * fields solo se usa en una lista.
 */
export interface VariableDraft {
  key: string;
  name: string;
  type: VariableType;
  required: boolean;
  defaultValue: string;
  fields: FieldDraft[];
}

let sequence = 0;

/** Nombre utilizable como {{.nombre}}: el mismo patron que valida el servicio. */
export function isVariableName(name: string): boolean {
  return NAME.test(name);
}

export function newFieldDraft(type: FieldType = 'string'): FieldDraft {
  sequence += 1;
  return { key: `field-${sequence}`, name: '', type, required: false };
}

/** Una lista nace con un campo vacio: el servicio exige al menos uno. */
export function newVariableDraft(type: VariableType = 'string'): VariableDraft {
  sequence += 1;
  return {
    key: `variable-${sequence}`,
    name: '',
    type,
    required: false,
    defaultValue: '',
    fields: type === 'list' ? [newFieldDraft()] : [],
  };
}

export function variablesToDrafts(variables: readonly TemplateVariable[] | null): VariableDraft[] {
  return (variables ?? []).map((v) => ({
    ...newVariableDraft(v.type),
    name: v.name,
    required: v.required,
    defaultValue: v.default === undefined ? '' : String(v.default),
    fields: (v.fields ?? []).map((f) => ({ ...newFieldDraft(f.type), ...f })),
  }));
}

export type Coerced = { ok: true; value: ScalarValue } | { ok: false; error: string };

/** Convierte lo escrito al tipo JSON que espera el backend para esa variable. */
export function coerceValue(type: FieldType, raw: string | boolean): Coerced {
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
  if (type === 'image') {
    // Mismo criterio que checkString: https y nunca SVG, que Gmail no muestra.
    try {
      const url = new URL(text.trim());
      if (url.protocol === 'https:' && url.host && !/\.svg$/i.test(url.pathname)) {
        return { ok: true, value: text.trim() };
      }
    } catch {
      // Se informa abajo.
    }
    return { ok: false, error: t('templates.variables.expectImage') };
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

/** reserved: nombres que inyecta quien envia (GET /templates/meta); no se declaran. */
export function draftsToVariables(
  drafts: readonly VariableDraft[],
  reserved: readonly string[] = [],
): DraftsResult {
  const variables: TemplateVariable[] = [];
  const errors: Record<string, string> = {};
  const seen = new Set<string>();
  for (const draft of drafts) {
    const name = draft.name.trim();
    if (!NAME.test(name)) {
      errors[draft.key] = t('templates.variables.invalidName');
      continue;
    }
    if (reserved.includes(name)) {
      errors[draft.key] = t('templates.variables.reserved', { name });
      continue;
    }
    if (seen.has(name)) {
      errors[draft.key] = t('templates.variables.duplicate', { name });
      continue;
    }
    seen.add(name);
    if (draft.type === 'list') {
      const fields = draftsToFields(draft.fields);
      if (typeof fields === 'string') {
        errors[draft.key] = fields;
        continue;
      }
      variables.push({ name, type: 'list', required: draft.required, fields });
      continue;
    }
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

/** Campos de una lista, o el primer error encontrado. */
function draftsToFields(drafts: readonly FieldDraft[]): TemplateField[] | string {
  if (drafts.length === 0) return t('templates.variables.listNeedsFields');
  const fields: TemplateField[] = [];
  const seen = new Set<string>();
  for (const draft of drafts) {
    const name = draft.name.trim();
    if (!NAME.test(name)) return t('templates.variables.invalidFieldName');
    if (seen.has(name)) return t('templates.variables.duplicateField', { name });
    seen.add(name);
    fields.push({ name, type: draft.type, required: draft.required });
  }
  return fields;
}

/**
 * Valores del formulario de previsualizacion: texto, o booleano en las casillas. El de una
 * lista es su JSON escrito a mano.
 */
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
    if (v.type === 'list') {
      const parsed = parseListValue(text);
      if (Array.isArray(parsed)) variables[v.name] = parsed;
      else errors[v.name] = parsed;
      continue;
    }
    const coerced = coerceValue(v.type, text);
    if (coerced.ok) variables[v.name] = coerced.value;
    else errors[v.name] = coerced.error;
  }
  return { variables, errors };
}

/**
 * Lista escrita como JSON en la previsualizacion: un arreglo de objetos con valores simples.
 * Los tipos de cada campo los valida el servicio al renderizar, que devuelve el campo exacto.
 */
export function parseListValue(text: string): ListItem[] | string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return t('templates.variables.expectList');
  }
  if (!Array.isArray(parsed)) return t('templates.variables.expectList');
  const items: ListItem[] = [];
  for (const item of parsed) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      return t('templates.variables.expectList');
    }
    const row: ListItem = {};
    for (const [key, value] of Object.entries(item as Record<string, unknown>)) {
      if (typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') {
        return t('templates.variables.expectList');
      }
      row[key] = value;
    }
    items.push(row);
  }
  return items;
}

/** Ejemplo de valor de una lista para guiar a quien escribe su JSON. */
export function listExample(fields: readonly TemplateField[]): string {
  const item: Record<string, ScalarValue> = {};
  for (const f of fields) {
    item[f.name] =
      f.type === 'number'
        ? 1
        : f.type === 'boolean'
          ? true
          : f.type === 'url' || f.type === 'image'
            ? 'https://'
            : f.type === 'email'
              ? 'nombre@dominio.com'
              : '';
  }
  return JSON.stringify([item]);
}
