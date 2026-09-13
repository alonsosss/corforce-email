import {
  TEMPLATE_MAX_VARIABLES,
  type TemplateContent,
  type TemplateVersion,
} from '@/api/templates';
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

export function contentFromDraft(draft: ContentDraft): {
  content: TemplateContent | null;
  errors: ContentErrors;
} {
  const errors: ContentErrors = {};
  if (!draft.subject.trim()) errors.subject = t('validation.required');
  if (!draft.html.trim()) errors.html = t('validation.required');
  if (draft.variables.length > TEMPLATE_MAX_VARIABLES) {
    errors.variablesCount = t('templates.variables.tooMany', { n: TEMPLATE_MAX_VARIABLES });
  }
  const parsed = draftsToVariables(draft.variables);
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
