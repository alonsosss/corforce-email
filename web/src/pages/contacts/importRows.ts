import type { AttributeDefinition, AttributeValue, ImportRow } from '@/api/contacts';
import { parseCsv } from '@/lib/csv';
import { parseAttribute } from './attributeValue';

// Del CSV pegado o subido a las filas del API. La cabecera nombra los campos fijos o los
// atributos declarados (con o sin el prefijo attributes.). El servicio valida cada fila y
// devuelve por linea las que rechaza; aqui solo se detecta lo que impide mandar la carga.

const FIXED = ['email', 'first_name', 'last_name', 'locale', 'timezone', 'tags'] as const;
type FixedColumn = (typeof FIXED)[number];
const ATTRIBUTE_PREFIX = 'attributes.';
/** Separador de etiquetas dentro de su celda: una etiqueta puede llevar espacios. */
export const TAG_SEPARATOR = '|';

export interface ParsedImport {
  rows: ImportRow[];
  columns: string[];
  missingEmail: boolean;
  unknownColumns: string[];
}

type Column =
  | { kind: 'fixed'; name: FixedColumn }
  | { kind: 'attribute'; def: AttributeDefinition }
  | { kind: 'unknown'; name: string };

function classify(header: string, byKey: Map<string, AttributeDefinition>): Column {
  const name = header.trim().toLowerCase();
  const fixed = FIXED.find((f) => f === name);
  if (fixed) return { kind: 'fixed', name: fixed };
  const key = name.startsWith(ATTRIBUTE_PREFIX) ? name.slice(ATTRIBUTE_PREFIX.length) : name;
  const def = byKey.get(key);
  return def ? { kind: 'attribute', def } : { kind: 'unknown', name: header.trim() };
}

export function rowsFromCsv(
  text: string,
  definitions: readonly AttributeDefinition[],
): ParsedImport {
  const cells = parseCsv(text);
  const [header = [], ...data] = cells;
  const byKey = new Map(definitions.map((d) => [d.key, d]));
  const columns = header.map((h) => classify(h, byKey));
  const unknownColumns = columns.flatMap((c) => (c.kind === 'unknown' && c.name ? [c.name] : []));
  const missingEmail = !columns.some((c) => c.kind === 'fixed' && c.name === 'email');

  const rows = data.map((line) => {
    const row: ImportRow = { email: '' };
    const attributes: Record<string, AttributeValue> = {};
    columns.forEach((col, i) => {
      const raw = line[i] ?? '';
      if (raw.trim() === '') return;
      if (col.kind === 'fixed') {
        if (col.name === 'tags') {
          row.tags = raw
            .split(TAG_SEPARATOR)
            .map((tag) => tag.trim())
            .filter(Boolean);
        } else {
          row[col.name] = raw.trim();
        }
      } else if (col.kind === 'attribute') {
        // Un valor que no encaja viaja tal cual: el servicio rechaza la fila y dice por que.
        const parsed = parseAttribute(col.def.type, raw);
        attributes[col.def.key] = parsed.ok && parsed.value !== null ? parsed.value : raw;
      }
    });
    if (Object.keys(attributes).length) row.attributes = attributes;
    return row;
  });

  return {
    rows,
    columns: header.map((h) => h.trim()),
    missingEmail,
    unknownColumns,
  };
}
