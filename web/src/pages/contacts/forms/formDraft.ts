import type { AttributeDefinition, AttributeType } from '@/api/contacts';
import type {
  CreateFormRequest,
  FormField,
  FormLimits,
  FormsMeta,
  FormStatus,
  FormTexts,
  SubscriptionForm,
} from '@/api/forms';
import { t } from '@/i18n';

// Reglas del editor de formularios que no dependen de la pantalla. El servicio las vuelve a
// comprobar; aqui se avisan antes de enviar.

/** Clave del campo que todo formulario lleva, siempre obligatorio. */
export const EMAIL_FIELD_KEY = 'email';

export interface FieldOption {
  key: string;
  type: AttributeType | 'email';
  /** Etiqueta con la que entra el campo al anadirlo. */
  label: string;
  /** Atributo declarado obligatorio: tiene que estar y ser obligatorio. */
  mandatory: boolean;
  builtin: boolean;
}

export interface FormDraft {
  name: string;
  status: FormStatus;
  listId: string;
  fields: FormField[];
  texts: FormTexts;
  redirectUrl: string;
  allowedOrigins: string[];
}

export interface FormDraftErrors {
  name?: string;
  listId?: string;
  fields?: string;
  /** Por clave del campo. */
  fieldLabels?: Record<string, string>;
  fieldPlaceholders?: Record<string, string>;
  title?: string;
  description?: string;
  submitLabel?: string;
  consentText?: string;
  successMessage?: string;
  redirectUrl?: string;
  allowedOrigins?: string;
}

function builtinLabel(key: string): string {
  if (key === EMAIL_FIELD_KEY) return t('common.email');
  if (key === 'first_name') return t('contacts.form.firstName');
  if (key === 'last_name') return t('contacts.form.lastName');
  return key;
}

/** Campos que admite el formulario: los fijos del servicio y los atributos declarados. */
export function fieldCatalog(
  meta: Pick<FormsMeta, 'builtin_fields'>,
  attributes: readonly AttributeDefinition[],
): FieldOption[] {
  const builtin: FieldOption[] = meta.builtin_fields.map((f) => ({
    key: f.key,
    type: f.type,
    label: builtinLabel(f.key),
    mandatory: f.key === EMAIL_FIELD_KEY,
    builtin: true,
  }));
  const known = new Set(builtin.map((f) => f.key));
  const declared: FieldOption[] = attributes
    .filter((a) => !known.has(a.key))
    .map((a) => ({
      key: a.key,
      type: a.type,
      label: a.label.trim() || a.key,
      mandatory: a.required,
      builtin: false,
    }));
  return [...builtin, ...declared];
}

/**
 * Sin permiso para leer los atributos, los campos ya guardados siguen siendo validos: se
 * conservan tal cual aunque no se puedan anadir otros.
 */
export function withSavedFields(
  catalog: readonly FieldOption[],
  fields: readonly FormField[],
): FieldOption[] {
  const known = new Set(catalog.map((o) => o.key));
  const saved: FieldOption[] = fields
    .filter((f) => !known.has(f.key))
    .map((f) => ({ key: f.key, type: 'string', label: f.label, mandatory: false, builtin: false }));
  return [...catalog, ...saved];
}

export function fieldFromOption(option: FieldOption): FormField {
  return { key: option.key, label: option.label, required: option.mandatory, placeholder: '' };
}

/** Borrador de un formulario nuevo: el correo y los atributos obligatorios ya puestos. */
export function emptyFormDraft(catalog: readonly FieldOption[]): FormDraft {
  const email = catalog.find((o) => o.key === EMAIL_FIELD_KEY);
  const fields = [
    email ? fieldFromOption(email) : emailField(),
    ...catalog.filter((o) => o.mandatory && o.key !== EMAIL_FIELD_KEY).map(fieldFromOption),
  ];
  return {
    name: '',
    status: 'active',
    listId: '',
    fields,
    texts: { title: '', description: '', submit_label: '', consent_text: '', success_message: '' },
    redirectUrl: '',
    allowedOrigins: [],
  };
}

function emailField(): FormField {
  return { key: EMAIL_FIELD_KEY, label: t('common.email'), required: true, placeholder: '' };
}

/** Borrador de un formulario guardado; si le faltara el correo, se anade al principio. */
export function draftFromForm(form: SubscriptionForm): FormDraft {
  const fields = form.fields.map((f) =>
    f.key === EMAIL_FIELD_KEY ? { ...f, required: true } : { ...f },
  );
  return {
    name: form.name,
    status: form.status,
    listId: form.list_id,
    fields: fields.some((f) => f.key === EMAIL_FIELD_KEY) ? fields : [emailField(), ...fields],
    texts: { ...form.texts },
    redirectUrl: form.redirect_url ?? '',
    allowedOrigins: [...form.allowed_origins],
  };
}

/** Campos que aun se pueden anadir. */
export function availableOptions(
  catalog: readonly FieldOption[],
  fields: readonly FormField[],
): FieldOption[] {
  const used = new Set(fields.map((f) => f.key));
  return catalog.filter((o) => !used.has(o.key));
}

/** Mueve un campo una posicion arriba (-1) o abajo (1). */
export function moveField(fields: readonly FormField[], index: number, delta: -1 | 1): FormField[] {
  const target = index + delta;
  if (index < 0 || index >= fields.length || target < 0 || target >= fields.length) {
    return [...fields];
  }
  const next = [...fields];
  const [moved] = next.splice(index, 1);
  if (moved) next.splice(target, 0, moved);
  return next;
}

/** El correo no se quita: es el unico dato imprescindible del contacto. */
export function removeField(fields: readonly FormField[], key: string): FormField[] {
  if (key === EMAIL_FIELD_KEY) return [...fields];
  return fields.filter((f) => f.key !== key);
}

/** Un campo obligatorio por el catalogo no se puede marcar como opcional. */
export function isLockedRequired(catalog: readonly FieldOption[], key: string): boolean {
  return catalog.some((o) => o.key === key && o.mandatory);
}

/**
 * Origen https://host[:puerto] normalizado (sin barra final) o null. Es lo que el servicio
 * pone en frame-ancestors y compara en CORS: nada de ruta, consulta ni credenciales.
 */
export function normalizeOrigin(raw: string): string | null {
  const value = raw.trim();
  if (!value) return null;
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return null;
  }
  if (url.protocol !== 'https:' || !url.hostname || url.username || url.password) return null;
  if (url.search || url.hash || value.includes('?') || value.includes('#')) return null;
  const rest = value.slice(value.indexOf('//') + 2);
  const slash = rest.indexOf('/');
  if (slash >= 0 && rest.slice(slash) !== '/') return null;
  return url.origin;
}

/** URL https absoluta con host. */
export function isHttpsUrl(raw: string): boolean {
  try {
    const url = new URL(raw.trim());
    return url.protocol === 'https:' && Boolean(url.hostname) && !url.username && !url.password;
  } catch {
    return false;
  }
}

/** Atributos obligatorios que faltan en el formulario o no estan marcados obligatorios. */
export function missingMandatory(
  catalog: readonly FieldOption[],
  fields: readonly FormField[],
): string[] {
  return catalog
    .filter((o) => o.mandatory && o.key !== EMAIL_FIELD_KEY)
    .filter((o) => !fields.some((f) => f.key === o.key && f.required))
    .map((o) => o.key);
}

function lengthError(value: string, max: number, required = false): string | undefined {
  const trimmed = value.trim();
  if (required && !trimmed) return t('validation.required');
  if (trimmed.length > max) return t('validation.maxLength', { n: max });
  return undefined;
}

function hasAny(errors: FormDraftErrors): boolean {
  return Object.values(errors).some((v) =>
    typeof v === 'object' && v !== null ? Object.keys(v).length > 0 : Boolean(v),
  );
}

export interface FormValidation {
  errors: FormDraftErrors;
  request: CreateFormRequest | null;
}

/** Valida el borrador y, si no hay errores, arma el cuerpo de la peticion. */
export function validateFormDraft(
  draft: FormDraft,
  limits: FormLimits,
  catalog: readonly FieldOption[],
): FormValidation {
  const errors: FormDraftErrors = {};
  const name = lengthError(draft.name, limits.max_name_length, true);
  if (name) errors.name = name;
  if (!draft.listId) errors.listId = t('validation.required');

  const email = draft.fields.find((f) => f.key === EMAIL_FIELD_KEY);
  const keys = draft.fields.map((f) => f.key);
  const known = new Set(catalog.map((o) => o.key));
  const missing = missingMandatory(catalog, draft.fields);
  if (!email || !email.required) {
    errors.fields = t('contacts.forms.error.emailRequired');
  } else if (new Set(keys).size !== keys.length) {
    errors.fields = t('contacts.forms.error.duplicateField');
  } else if (keys.some((k) => !known.has(k))) {
    errors.fields = t('contacts.forms.error.unknownField', {
      keys: keys.filter((k) => !known.has(k)).join(', '),
    });
  } else if (draft.fields.length > limits.max_fields) {
    errors.fields = t('contacts.forms.error.tooManyFields', { n: limits.max_fields });
  } else if (missing.length > 0) {
    errors.fields = t('contacts.forms.error.mandatoryMissing', { keys: missing.join(', ') });
  }

  const labels: Record<string, string> = {};
  const placeholders: Record<string, string> = {};
  for (const field of draft.fields) {
    const label = lengthError(field.label, limits.max_label_length, true);
    if (label) labels[field.key] = label;
    const placeholder = lengthError(field.placeholder, limits.max_placeholder_length);
    if (placeholder) placeholders[field.key] = placeholder;
  }
  if (Object.keys(labels).length) errors.fieldLabels = labels;
  if (Object.keys(placeholders).length) errors.fieldPlaceholders = placeholders;

  const redirect = draft.redirectUrl.trim();
  const { texts } = draft;
  const title = lengthError(texts.title, limits.max_title_length);
  if (title) errors.title = title;
  const description = lengthError(texts.description, limits.max_description_length);
  if (description) errors.description = description;
  const submitLabel = lengthError(texts.submit_label, limits.max_submit_label_length);
  if (submitLabel) errors.submitLabel = submitLabel;
  const consent = lengthError(texts.consent_text, limits.max_consent_text_length, true);
  if (consent) errors.consentText = consent;
  const success = lengthError(texts.success_message, limits.max_success_message_length, true);
  if (success) errors.successMessage = success;

  if (redirect) {
    if (!isHttpsUrl(redirect)) errors.redirectUrl = t('contacts.forms.error.redirectHttps');
    else if (redirect.length > limits.max_redirect_url_length) {
      errors.redirectUrl = t('validation.maxLength', { n: limits.max_redirect_url_length });
    }
  }

  const invalidOrigins = draft.allowedOrigins.filter((o) => normalizeOrigin(o) !== o);
  if (invalidOrigins.length) {
    errors.allowedOrigins = t('contacts.forms.error.origin', {
      origins: invalidOrigins.join(', '),
    });
  } else if (draft.allowedOrigins.length > limits.max_allowed_origins) {
    errors.allowedOrigins = t('contacts.forms.error.tooManyOrigins', {
      n: limits.max_allowed_origins,
    });
  }

  if (hasAny(errors)) return { errors, request: null };
  return {
    errors,
    request: {
      name: draft.name.trim(),
      status: draft.status,
      list_id: draft.listId,
      fields: draft.fields.map((f) => ({
        key: f.key,
        label: f.label.trim(),
        required: f.key === EMAIL_FIELD_KEY ? true : f.required,
        placeholder: f.placeholder.trim(),
      })),
      texts: {
        title: texts.title.trim(),
        description: texts.description.trim(),
        submit_label: texts.submit_label.trim(),
        consent_text: texts.consent_text.trim(),
        success_message: texts.success_message.trim(),
      },
      redirect_url: redirect || null,
      allowed_origins: [...draft.allowedOrigins],
    },
  };
}
