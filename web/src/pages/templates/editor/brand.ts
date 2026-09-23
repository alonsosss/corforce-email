import type { BrandFont, BrandKit, BrandKitFooter } from '@/api/templates';

/**
 * Lo que los bloques y la galeria toman del kit de marca. Los colores y la tipografia de
 * reserva son el acabado neutro de las plantillas cuando la empresa aun no tiene kit; los
 * datos de la empresa (nombre, direccion, web, logo) solo salen del kit, nunca se inventan.
 */
export interface BrandTokens {
  primary: string;
  text: string;
  muted: string;
  background: string;
  surface: string;
  border: string;
  /** Pila CSS: la tipografia del kit y alternativas seguras. */
  fontFamily: string;
  logoUrl: string | null;
  footer: BrandKitFooter;
}

const NEUTRAL = {
  primary: '#1F4ED8',
  text: '#1F2937',
  muted: '#6B7280',
  background: '#F3F4F6',
  surface: '#FFFFFF',
  border: '#E5E7EB',
  fontFamily: 'Helvetica, Arial, sans-serif',
} as const;

const FALLBACK_STACK = 'Helvetica, Arial, sans-serif';

export function fontStack(font: string | undefined): string {
  const name = font?.trim();
  if (!name) return NEUTRAL.fontFamily;
  if (name.includes(',')) return name;
  const quoted = /\s/.test(name) ? `'${name}'` : name;
  return `${quoted}, ${FALLBACK_STACK}`;
}

/**
 * Tokens del kit. La pila de la tipografia sale del catalogo de GET /templates/meta
 * (brand_fonts); si no esta en el, se arma con alternativas seguras.
 */
export function brandTokens(
  kit: BrandKit | null,
  logoUrl: string | null,
  fonts: readonly BrandFont[] = [],
): BrandTokens {
  const colors = kit?.colors ?? [];
  const font = kit?.fonts[0];
  const known = fonts.find((f) => f.name === font);
  return {
    primary: colors[0] ?? NEUTRAL.primary,
    text: colors[1] ?? NEUTRAL.text,
    muted: NEUTRAL.muted,
    background: NEUTRAL.background,
    surface: NEUTRAL.surface,
    border: NEUTRAL.border,
    fontFamily: known?.stack ?? fontStack(font),
    logoUrl,
    footer: kit?.footer ?? { company: '', address: '', website: '', support_email: '' },
  };
}

/** URL http(s) valida o null: un dato del kit mal escrito no llega a un href. */
export function safeHttpUrl(value: string): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  try {
    const url = new URL(trimmed);
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.toString() : null;
  } catch {
    return null;
  }
}

const EMAIL = /^[^\s@<>()",;:]+@[^\s@<>()",;:]+\.[^\s@<>()",;:]+$/;

export function safeEmail(value: string): string | null {
  const trimmed = value.trim();
  return EMAIL.test(trimmed) ? trimmed : null;
}
