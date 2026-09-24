import type { PagesMeta } from '@/api/pages';
import { rules } from '@/lib/validate';
import { t } from '@/i18n';

/**
 * Direccion de la pagina a partir de su nombre: minusculas sin tildes, palabras separadas por
 * guiones y sin guiones en los extremos, recortada al tope del servicio.
 */
export function slugFromName(name: string, maxLength: number): string {
  const base = name
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return base.slice(0, Math.max(0, maxLength)).replace(/-+$/g, '');
}

function slugPattern(pattern: string): RegExp | null {
  try {
    return new RegExp(pattern);
  } catch {
    return null;
  }
}

/** Error del slug con las reglas de GET /templates/pages/meta, o null si es valido. */
export function slugError(
  slug: string,
  meta: Pick<PagesMeta, 'max_slug_length' | 'slug_pattern'>,
): string | null {
  if (!slug) return t('validation.required');
  if (slug.length > meta.max_slug_length) {
    return t('validation.maxLength', { n: meta.max_slug_length });
  }
  const pattern = slugPattern(meta.slug_pattern);
  if (pattern) return pattern.test(slug) ? null : t('validation.slug');
  return rules.slug(slug);
}

/** URL publica que tendra la pagina con ese slug. */
export function publicUrlFor(prefix: string, slug: string): string {
  return `${prefix.endsWith('/') ? prefix : `${prefix}/`}${slug}`;
}
