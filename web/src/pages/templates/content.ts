import type { TemplateContent, TemplateVersion, TemplatesMeta } from '@/api/templates';
import { t } from '@/i18n';
import { draftsToVariables, variablesToDrafts, type VariableDraft } from './variables';

/** Contenido editable de una version: asunto, HTML, texto y variables declaradas. */
export interface ContentDraft {
  subject: string;
  html: string;
  text: string;
  variables: VariableDraft[];
}

export interface ContentErrors {
  subject?: string;
  html?: string;
  variables?: Record<string, string>;
  variablesCount?: string;
}

const encoder = new TextEncoder();

/** Los topes de asunto y HTML del servicio son en bytes UTF-8, no en caracteres. */
function byteLength(value: string): number {
  return encoder.encode(value).length;
}

export function emptyContent(): ContentDraft {
  return { subject: '', html: '', text: '', variables: [] };
}

export function contentFromVersion(version: TemplateVersion | null): ContentDraft {
  if (!version) return emptyContent();
  return {
    subject: version.subject,
    html: version.html,
    text: version.text ?? '',
    variables: variablesToDrafts(version.variables),
  };
}

export function hasContentErrors(errors: ContentErrors): boolean {
  return Boolean(errors.subject || errors.html || errors.variables || errors.variablesCount);
}

export function contentFromDraft(
  draft: ContentDraft,
  meta: TemplatesMeta,
): {
  content: TemplateContent | null;
  errors: ContentErrors;
} {
  const { limits } = meta;
  const errors: ContentErrors = {};
  if (!draft.subject.trim()) errors.subject = t('validation.required');
  else if (byteLength(draft.subject) > limits.max_subject_bytes) {
    errors.subject = t('templates.content.tooLarge', { n: limits.max_subject_bytes });
  }
  if (!draft.html.trim()) errors.html = t('validation.required');
  else if (byteLength(draft.html) > limits.max_html_bytes) {
    errors.html = t('templates.content.tooLarge', { n: limits.max_html_bytes });
  }
  if (draft.variables.length > limits.max_variables) {
    errors.variablesCount = t('templates.variables.tooMany', { n: limits.max_variables });
  }
  const parsed = draftsToVariables(
    draft.variables,
    meta.reserved_variables.map((v) => v.name),
  );
  if (Object.keys(parsed.errors).length) errors.variables = parsed.errors;
  if (hasContentErrors(errors)) return { content: null, errors };
  return {
    content: {
      subject: draft.subject,
      html: draft.html,
      text: draft.text.trim() ? draft.text : undefined,
      variables: parsed.variables,
    },
    errors,
  };
}
