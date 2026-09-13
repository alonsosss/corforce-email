// Espejo de services/mail-directory/internal/domain/validation.go (NormalizeDomain,
// NormalizeLocalPart, NormalizeEmail, NormalizeAddress y NormalizeGoto). El backend vuelve
// a validar; aqui solo se evita enviar lo que va a rechazar y se normaliza igual que el.

const MAX_DOMAIN_LENGTH = 253;
const MAX_LABEL_LENGTH = 63;
const MAX_LOCAL_PART_LENGTH = 64;
const DNS_LABEL = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/;
const TLD = /^[a-z]{2,}$/;
const OWN_LOCAL_PART = /^[a-z0-9._+-]+$/;
const EXTERNAL_LOCAL_PART = /^[a-z0-9!#$%&'*+/=?^_`{|}~.-]+$/;

function hasBadDots(local: string): boolean {
  return local.startsWith('.') || local.endsWith('.') || local.includes('..');
}

/** Nombre DNS en minusculas, sin punto final, con al menos dos etiquetas y TLD alfabetico. */
export function normalizeDomainName(raw: string): string | null {
  const name = raw.trim().toLowerCase().replace(/\.$/, '');
  if (!name || name.length > MAX_DOMAIN_LENGTH) return null;
  const labels = name.split('.');
  if (labels.length < 2) return null;
  if (labels.some((label) => label.length > MAX_LABEL_LENGTH || !DNS_LABEL.test(label))) {
    return null;
  }
  return TLD.test(labels[labels.length - 1] ?? '') ? name : null;
}

/** Parte local de un buzon propio: minusculas, digitos y . _ + -, sin puntos sueltos. */
export function normalizeLocalPart(raw: string): string | null {
  const local = raw.trim().toLowerCase();
  if (!local || local.length > MAX_LOCAL_PART_LENGTH || !OWN_LOCAL_PART.test(local)) return null;
  return hasBadDots(local) ? null : local;
}

/** Direccion completa, propia o externa. */
export function normalizeEmail(raw: string): string | null {
  const address = raw.trim().toLowerCase();
  const at = address.lastIndexOf('@');
  if (at <= 0 || at === address.length - 1) return null;
  const local = address.slice(0, at);
  if (local.length > MAX_LOCAL_PART_LENGTH || !EXTERNAL_LOCAL_PART.test(local)) return null;
  if (hasBadDots(local)) return null;
  const domain = normalizeDomainName(address.slice(at + 1));
  return domain ? `${local}@${domain}` : null;
}

/** Direccion de un alias: correo completo o comodin de dominio (@dominio). */
export function normalizeAliasAddress(raw: string): string | null {
  const address = raw.trim().toLowerCase();
  if (address.startsWith('@')) {
    const domain = normalizeDomainName(address.slice(1));
    return domain ? `@${domain}` : null;
  }
  return normalizeEmail(address);
}

/** El campo goto guarda los destinos separados por comas. */
export function gotoToList(goto: string): string[] {
  return goto
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean);
}

export function listToGoto(addresses: readonly string[]): string {
  return addresses.join(',');
}
