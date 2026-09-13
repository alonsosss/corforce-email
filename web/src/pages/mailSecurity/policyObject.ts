import { normalizeDomainName } from '@/lib/mailAddress';

// Espejo de ValidateObject y ValidateListPattern de
// services/mail-security/internal/domain/validation.go. Un objeto de politica es un buzon
// (usuario@dominio) o un dominio; un patron de lista, una direccion, @dominio o un comodin.

const LIST_PATTERN = /^[a-z0-9._%+@*-]+$/;
const DECIMAL = /^-?\d+(?:\.\d+)?$/;

export type PolicyObjectKind = 'mailbox' | 'domain';

export function policyObjectKind(object: string): PolicyObjectKind {
  return object.includes('@') ? 'mailbox' : 'domain';
}

export function normalizePolicyObject(raw: string): string | null {
  const value = raw.trim().toLowerCase();
  if (!value.includes('@')) return normalizeDomainName(value);
  const at = value.lastIndexOf('@');
  const local = value.slice(0, at);
  if (at <= 0 || !LIST_PATTERN.test(local) || local.includes('*') || local.includes('@')) {
    return null;
  }
  const domain = normalizeDomainName(value.slice(at + 1));
  return domain ? `${local}@${domain}` : null;
}

export function normalizeListPattern(raw: string): string | null {
  const pattern = raw.trim().toLowerCase();
  if (!pattern || pattern === '*' || pattern === '@' || !LIST_PATTERN.test(pattern)) return null;
  return (pattern.match(/@/g) ?? []).length > 1 ? null : pattern;
}

/** Puntuacion de Rspamd: decimal con signo, que el API recibe como cadena. */
export function isDecimal(value: string): boolean {
  return DECIMAL.test(value.trim());
}
