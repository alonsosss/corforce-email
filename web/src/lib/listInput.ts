// Listas que el usuario escribe o pega: destinos de un alias, exclusiones, direcciones a
// importar. Lo que no pasa la normalizacion se devuelve aparte para que lo corrija.

const SEPARATORS = /[\s,;]+/;

/** Separa por comas, punto y coma, espacios y saltos de linea. */
export function splitTokens(text: string): string[] {
  return text
    .split(SEPARATORS)
    .map((token) => token.trim())
    .filter(Boolean);
}

/** Una entrada por linea, sin lineas vacias. */
export function splitLines(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}

export interface ChipsMerge {
  values: string[];
  rejected: string[];
}

/** Anade a los valores actuales lo escrito, normalizado y sin duplicados. */
export function mergeChips(
  current: readonly string[],
  text: string,
  normalize: (raw: string) => string | null,
): ChipsMerge {
  const values = [...current];
  const rejected: string[] = [];
  for (const token of splitTokens(text)) {
    const value = normalize(token);
    if (value === null) rejected.push(token);
    else if (!values.includes(value)) values.push(value);
  }
  return { values, rejected };
}
