import type { FlagChange } from '@/api/webmail';

/** Flags tras aplicar un cambio aceptado por el servicio, sin repetir ninguno. */
export function applyFlagChange(flags: readonly string[], change: FlagChange): string[] {
  const removed = new Set<string>(change.remove ?? []);
  const next = flags.filter((flag) => !removed.has(flag));
  for (const flag of change.add ?? []) {
    if (!next.includes(flag)) next.push(flag);
  }
  return next;
}
