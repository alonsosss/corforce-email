// Motivos que los servicios guardan como "CODIGO: detalle" (pausas, fallos y rechazos de un
// vecino). La interfaz traduce el codigo y ensena el detalle tal cual lo escribio el servicio.
const CODED = /^([A-Z][A-Z0-9_]*):\s*([\s\S]*)$/;

export interface CodedReason {
  code: string;
  detail: string;
}

/** Separa codigo y detalle; null si el texto no empieza por un codigo. */
export function splitCodedReason(value: string): CodedReason | null {
  const match = CODED.exec(value.trim());
  if (!match) return null;
  return { code: match[1] ?? '', detail: (match[2] ?? '').trim() };
}
