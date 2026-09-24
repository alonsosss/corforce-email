import type { MessageFilters } from '@/api/webmail';
import type { WebmailView } from '@/paths';
import { t } from '@/i18n';
import { utf8Length } from './format';

/** Busqueda de la carpeta: el texto libre y los filtros avanzados, tal como viajan en la URL. */
export interface SearchCriteria {
  q: string;
  from: string;
  to: string;
  subject: string;
  since: string;
  before: string;
  unread: boolean;
  flagged: boolean;
  attachments: boolean;
}

export const EMPTY_CRITERIA: SearchCriteria = {
  q: '',
  from: '',
  to: '',
  subject: '',
  since: '',
  before: '',
  unread: false,
  flagged: false,
  attachments: false,
};

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const ON = '1';

function isDate(value: string): boolean {
  if (!DATE.test(value)) return false;
  const parsed = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(parsed.getTime()) && parsed.toISOString().startsWith(value);
}

/** Lee la busqueda de la URL; una fecha mal formada se ignora. */
export function readCriteria(params: URLSearchParams): SearchCriteria {
  const text = (key: string) => params.get(key)?.trim() ?? '';
  const date = (key: string) => {
    const value = text(key);
    return isDate(value) ? value : '';
  };
  return {
    q: text('q'),
    from: text('from'),
    to: text('to'),
    subject: text('subject'),
    since: date('since'),
    before: date('before'),
    unread: params.get('unread') === ON,
    flagged: params.get('flagged') === ON,
    attachments: params.get('attachments') === ON,
  };
}

/** Parametros de la URL de la vista; lo vacio no se escribe. */
export function criteriaToView(criteria: SearchCriteria): Partial<WebmailView> {
  const text = (value: string) => value.trim() || undefined;
  const mark = (value: boolean) => (value ? ON : undefined);
  return {
    q: text(criteria.q),
    from: text(criteria.from),
    to: text(criteria.to),
    subject: text(criteria.subject),
    since: text(criteria.since),
    before: text(criteria.before),
    unread: mark(criteria.unread),
    flagged: mark(criteria.flagged),
    attachments: mark(criteria.attachments),
  };
}

/** Filtros del API (GET /folders/{folder}/messages). */
export function criteriaToFilters(criteria: SearchCriteria): MessageFilters {
  const text = (value: string) => value.trim() || undefined;
  return {
    from: text(criteria.from),
    to: text(criteria.to),
    subject: text(criteria.subject),
    since: text(criteria.since),
    before: text(criteria.before),
    unread: criteria.unread || undefined,
    flagged: criteria.flagged || undefined,
    hasAttachments: criteria.attachments || undefined,
  };
}

/** Algun filtro avanzado activo (sin contar el texto libre). */
export function hasAdvanced(criteria: SearchCriteria): boolean {
  return Boolean(
    criteria.from.trim() ||
    criteria.to.trim() ||
    criteria.subject.trim() ||
    criteria.since ||
    criteria.before ||
    criteria.unread ||
    criteria.flagged ||
    criteria.attachments,
  );
}

export function isSearching(criteria: SearchCriteria): boolean {
  return Boolean(criteria.q.trim()) || hasAdvanced(criteria);
}

export type CriteriaField = 'q' | 'from' | 'to' | 'subject' | 'before';

/**
 * Problemas antes de buscar: cada texto con el tope del servicio (bytes UTF-8) y un rango
 * de fechas que no se invierte. "before" es exclusivo, como IMAP SEARCH BEFORE.
 */
export function criteriaProblems(
  criteria: SearchCriteria,
  maxBytes: number | null,
): Partial<Record<CriteriaField, string>> {
  const problems: Partial<Record<CriteriaField, string>> = {};
  if (maxBytes !== null) {
    for (const field of ['q', 'from', 'to', 'subject'] as const) {
      if (utf8Length(criteria[field].trim()) > maxBytes) {
        problems[field] = t('webmail.list.searchTooLong');
      }
    }
  }
  if (criteria.since && criteria.before && criteria.before <= criteria.since) {
    problems.before = t('webmail.search.badRange');
  }
  return problems;
}

/** Resumen legible de los filtros activos, para la linea de resultados. */
export function criteriaSummary(criteria: SearchCriteria): string {
  const parts: string[] = [];
  if (criteria.q.trim()) parts.push(`"${criteria.q.trim()}"`);
  if (criteria.from.trim()) parts.push(`${t('webmail.search.from')}: ${criteria.from.trim()}`);
  if (criteria.to.trim()) parts.push(`${t('webmail.search.to')}: ${criteria.to.trim()}`);
  if (criteria.subject.trim())
    parts.push(`${t('webmail.search.subject')}: ${criteria.subject.trim()}`);
  if (criteria.since) parts.push(`${t('webmail.search.since')}: ${criteria.since}`);
  if (criteria.before) parts.push(`${t('webmail.search.before')}: ${criteria.before}`);
  if (criteria.unread) parts.push(t('webmail.search.unread'));
  if (criteria.flagged) parts.push(t('webmail.search.flagged'));
  if (criteria.attachments) parts.push(t('webmail.search.attachments'));
  return parts.join(', ');
}
