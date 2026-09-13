import type { SuppressionCause, SuppressionEntry } from '@/api/suppression';

export interface CauseView {
  cause: SuppressionCause;
  /** La causa principal de la direccion: la vigente mas grave (su id es el de la fila). */
  primary: boolean;
  /** Vigente: sin caducidad o con caducidad futura. Solo una manual puede caducar. */
  active: boolean;
}

function isActive(cause: SuppressionCause, now: Date): boolean {
  if (!cause.expires_at) return true;
  const expires = Date.parse(cause.expires_at);
  return Number.isNaN(expires) || expires > now.getTime();
}

/**
 * Causas de una direccion para mostrarlas: la principal primero, despues las demas
 * vigentes y al final las caducadas. Dentro de cada grupo se respeta el orden del servicio,
 * que ya las da de mas a menos grave.
 */
export function causeViews(entry: SuppressionEntry, now: Date = new Date()): CauseView[] {
  const causes = entry.causes?.length ? entry.causes : [entry];
  const views = causes.map((cause) => ({
    cause,
    primary: cause.id === entry.id,
    active: isActive(cause, now),
  }));
  const rank = (v: CauseView) => (v.primary ? 0 : v.active ? 1 : 2);
  return views
    .map((view, index) => ({ view, index }))
    .sort((a, b) => rank(a.view) - rank(b.view) || a.index - b.index)
    .map(({ view }) => view);
}

/** Cuantas causas vigentes quedarian si se retirara esta. */
export function activeCausesAfterRemoving(entry: SuppressionEntry, causeId: string): number {
  return causeViews(entry).filter((v) => v.active && v.cause.id !== causeId).length;
}
