// Lector de CSV (RFC 4180) para las importaciones: comillas, comillas dobles escapadas,
// separadores dentro de comillas y saltos CRLF. Admite coma o punto y coma como
// separador, porque las hojas de calculo en espanol exportan con punto y coma.

export type CsvDelimiter = ',' | ';';

/** Elige el separador por la cabecera: el que aparece fuera de comillas. */
export function detectDelimiter(text: string): CsvDelimiter {
  const header = text.split(/\r?\n/, 1)[0] ?? '';
  let inQuotes = false;
  let commas = 0;
  let semicolons = 0;
  for (const ch of header) {
    if (ch === '"') inQuotes = !inQuotes;
    else if (!inQuotes && ch === ',') commas += 1;
    else if (!inQuotes && ch === ';') semicolons += 1;
  }
  return semicolons > commas ? ';' : ',';
}

/** Filas de celdas. Las lineas vacias se omiten. */
export function parseCsv(
  text: string,
  delimiter: CsvDelimiter = detectDelimiter(text),
): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let inQuotes = false;
  const source = text.replace(/^\uFEFF/, '');

  const endRow = () => {
    row.push(cell);
    if (row.some((c) => c.trim() !== '')) rows.push(row);
    row = [];
    cell = '';
  };

  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i];
    if (inQuotes) {
      if (ch === '"') {
        if (source[i + 1] === '"') {
          cell += '"';
          i += 1;
        } else {
          inQuotes = false;
        }
      } else {
        cell += ch;
      }
      continue;
    }
    if (ch === '"') {
      inQuotes = true;
    } else if (ch === delimiter) {
      row.push(cell);
      cell = '';
    } else if (ch === '\n') {
      endRow();
    } else if (ch !== '\r') {
      cell += ch;
    }
  }
  if (cell !== '' || row.length > 0) endRow();
  return rows;
}
