import type { AttributeDefinition, AttributeType, AttributeValue } from '@/api/contacts';
import { t } from '@/i18n';

// Valores de los atributos declarados. El tipo lo decide la definicion del API, no lo que
// se escribe; el servicio vuelve a validar cada valor (domain.ParseAttributeValue).

const NUMBER = /^-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Texto editable: '' sin valor; los booleanos como 'true' o 'false'. */
export function attributeToDraft(value: AttributeValue | null | undefined): string {
  return value === undefined || value === null ? '' : String(value);
}

export type ParsedAttribute =
  { ok: true; value: AttributeValue | null } | { ok: false; error: string };

export function parseAttribute(type: AttributeType, raw: string): ParsedAttribute {
  const text = raw.trim();
  if (text === '') return { ok: true, value: null };
  if (type === 'number') {
    const n = Number(text);
    return NUMBER.test(text) && Number.isFinite(n)
      ? { ok: true, value: n }
      : { ok: false, error: t('contacts.attributes.expectNumber') };
  }
  if (type === 'boolean') {
    if (text === 'true') return { ok: true, value: true };
    if (text === 'false') return { ok: true, value: false };
    return { ok: false, error: t('contacts.attributes.expectBoolean') };
  }
  if (type === 'date') {
    return DATE.test(text) && !Number.isNaN(Date.parse(`${text}T00:00:00Z`))
      ? { ok: true, value: text }
      : { ok: false, error: t('contacts.attributes.expectDate') };
  }
  return { ok: true, value: raw };
}

export interface AttributesResult {
  attributes: Record<string, AttributeValue | null>;
  errors: Record<string, string>;
}

/**
 * Del formulario al cuerpo. En el alta (current null) solo viajan los atributos con valor
 * y se exigen los obligatorios. En la edicion viajan solo los cambiados, y un valor
 * borrado viaja como null para quitarlo.
 */
export function buildAttributes(
  definitions: readonly AttributeDefinition[],
  drafts: Record<string, string>,
  current: Record<string, AttributeValue> | null,
): AttributesResult {
  const attributes: Record<string, AttributeValue | null> = {};
  const errors: Record<string, string> = {};
  for (const def of definitions) {
    const parsed = parseAttribute(def.type, drafts[def.key] ?? '');
    if (!parsed.ok) {
      errors[def.key] = parsed.error;
      continue;
    }
    const before = current?.[def.key];
    if (parsed.value === null) {
      const had = before !== undefined && before !== null;
      if (def.required && (current === null || had)) {
        errors[def.key] = t('validation.required');
      } else if (current !== null && had) {
        attributes[def.key] = null;
      }
      continue;
    }
    if (current === null || before === undefined || String(before) !== String(parsed.value)) {
      attributes[def.key] = parsed.value;
    }
  }
  return { attributes, errors };
}
