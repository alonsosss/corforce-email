import type { TemplateVariable } from '@/api/templates';
import type { SendingDomain } from '@/api/transactional';
import { buildRenderVariables, type RenderValuesResult, type ValueDraft } from './variables';

// Reglas del formulario de envio de prueba que no dependen de la pantalla.

/** Dominios desde los que hoy se puede enviar (can_send lo decide transactional), ordenados. */
export function sendableDomains(domains: readonly SendingDomain[]): string[] {
  return domains
    .filter((d) => d.can_send)
    .map((d) => d.domain)
    .sort((a, b) => a.localeCompare(b));
}

/** Remitente a partir de la parte local escrita y el dominio elegido; null si falta alguno. */
export function senderAddress(localPart: string, domain: string): string | null {
  const local = localPart.trim();
  if (!local || !domain || local.includes('@')) return null;
  return `${local}@${domain}`;
}

/**
 * Valores de las variables para la prueba. Una variable vacia, tambien una requerida, no
 * viaja: el render de prueba la completa con su valor por defecto o uno de ejemplo. Lo escrito
 * se valida contra su tipo como en la previsualizacion.
 */
export function testVariables(
  declared: readonly TemplateVariable[],
  values: ValueDraft,
): RenderValuesResult {
  return buildRenderVariables(
    declared.map((v) => ({ ...v, required: false })),
    values,
  );
}
