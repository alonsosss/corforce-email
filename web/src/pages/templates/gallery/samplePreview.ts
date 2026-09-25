import type { ScalarValue, TemplateField, TemplateVariable, VariableType } from '@/api/templates';
import { t } from '@/i18n';
import { IMAGE_PLACEHOLDER } from '../editor/blocks';

// Vista previa de la galeria: las plantillas llevan acciones de plantilla ({{.first_name}},
// {{if}}, {{range}} y funciones como money) que el servidor resuelve al enviar. En la miniatura
// se ejecutan con valores de ejemplo para que se vea el correo y no el codigo. Es un evaluador
// minimo del mismo subconjunto que admite services/templates (render/walker.go), solo para la
// vista previa: lo que se guarda y se envia es la plantilla sin tocar.

type Item = Record<string, ScalarValue>;
type Value = ScalarValue | Item[];

/** Elementos de una lista de ejemplo en la miniatura. */
const SAMPLE_ITEMS = 2;
const SAMPLE_NUMBER = 1024;

function sampleFor(name: string, type: VariableType | undefined): ScalarValue {
  if (name === 'first_name') return t('templates.gallery.sampleFirstName');
  if (name === 'recipient_email' || type === 'email') return t('templates.gallery.sampleEmail');
  if (type === 'image') return IMAGE_PLACEHOLDER;
  if (name === 'unsubscribe_url' || name === 'view_in_browser_url' || type === 'url') return '#';
  if (type === 'number') return SAMPLE_NUMBER;
  if (type === 'boolean') return true;
  return t('templates.gallery.sampleValue');
}

function sampleItem(fields: readonly TemplateField[]): Item {
  const item: Item = {};
  for (const f of fields) {
    item[f.name] =
      f.name === 'name' ? t('templates.gallery.sampleProduct') : sampleFor(f.name, f.type);
  }
  return item;
}

function sampleValues(variables: readonly TemplateVariable[]): Map<string, Value> {
  const values = new Map<string, Value>();
  for (const v of variables) {
    if (v.type === 'list') {
      values.set(
        v.name,
        Array.from({ length: SAMPLE_ITEMS }, () => sampleItem(v.fields ?? [])),
      );
    } else {
      values.set(v.name, v.default ?? sampleFor(v.name, v.type));
    }
  }
  return values;
}

// ── Arbol ────────────────────────────────────────────────────────────────────

type Node =
  | { kind: 'text'; value: string }
  | { kind: 'print'; expr: string }
  | { kind: 'if'; cond: string; then: Node[]; else: Node[] }
  | { kind: 'range'; expr: string; body: Node[]; else: Node[] };

const ACTION = /\{\{-?\s*([\s\S]*?)\s*-?\}\}/g;

interface Frame {
  node: Extract<Node, { kind: 'if' | 'range' }>;
  inElse: boolean;
  /** Un {{else if}} abre un if anidado que cierra el mismo {{end}}. */
  chained: boolean;
}

function parse(src: string): Node[] {
  const root: Node[] = [];
  const stack: Frame[] = [];
  const target = (): Node[] => {
    const top = stack[stack.length - 1];
    if (!top) return root;
    if (top.node.kind === 'if') return top.inElse ? top.node.else : top.node.then;
    return top.inElse ? top.node.else : top.node.body;
  };
  let last = 0;
  for (const match of src.matchAll(ACTION)) {
    const index = match.index ?? 0;
    if (index > last) target().push({ kind: 'text', value: src.slice(last, index) });
    last = index + match[0].length;
    const action = (match[1] ?? '').trim();
    if (action.startsWith('if ')) {
      const node: Node = { kind: 'if', cond: action.slice(3), then: [], else: [] };
      target().push(node);
      stack.push({ node, inElse: false, chained: false });
    } else if (action.startsWith('range ')) {
      const node: Node = { kind: 'range', expr: action.slice(6), body: [], else: [] };
      target().push(node);
      stack.push({ node, inElse: false, chained: false });
    } else if (action.startsWith('else if ')) {
      const top = stack[stack.length - 1];
      if (!top) throw new Error('else sin if');
      top.inElse = true;
      const node: Node = { kind: 'if', cond: action.slice(8), then: [], else: [] };
      target().push(node);
      stack.push({ node, inElse: false, chained: true });
    } else if (action === 'else') {
      const top = stack[stack.length - 1];
      if (!top) throw new Error('else sin if');
      top.inElse = true;
    } else if (action === 'end') {
      let top = stack.pop();
      while (top?.chained) top = stack.pop();
      if (!top) throw new Error('end sin apertura');
    } else {
      target().push({ kind: 'print', expr: action });
    }
  }
  if (stack.length) throw new Error('bloque sin cerrar');
  if (last < src.length) root.push({ kind: 'text', value: src.slice(last) });
  return root;
}

// ── Expresiones ──────────────────────────────────────────────────────────────

const TOKEN = /\s*(\(|\)|"(?:[^"\\]|\\.)*"|\$?\.[a-z][a-z0-9_]*|-?\d+(?:\.\d+)?|[a-z]+)/gy;

interface Scope {
  root: Map<string, Value>;
  item: Item | null;
}

function truthy(v: Value | undefined): boolean {
  if (Array.isArray(v)) return v.length > 0;
  return v !== undefined && v !== '' && v !== 0 && v !== false;
}

function formatMoney(v: Value | undefined): string {
  const n = Number(v);
  if (!Number.isFinite(n)) return '';
  const [whole = '0', frac = '00'] = Math.abs(n).toFixed(2).split('.');
  return `${n < 0 ? '-' : ''}${whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}.${frac}`;
}

function asList(v: Value | undefined): Item[] {
  return Array.isArray(v) ? v : [];
}

type Fn = (...args: (Value | undefined)[]) => Value;

const FUNCS: Record<string, Fn> = {
  upper: (v) => String(v ?? '').toUpperCase(),
  lower: (v) => String(v ?? '').toLowerCase(),
  title: (v) => String(v ?? ''),
  default: (def, v) => (truthy(v) ? (v as Value) : (def ?? '')),
  date: (_layout, v) => (typeof v === 'string' ? v : ''),
  money: (v) => formatMoney(v),
  nonzero: (v) => Number(v) !== 0 && Number.isFinite(Number(v)),
  count: (v) => asList(v).length,
  take: (n, v) => asList(v).slice(0, Number(n)),
  rest: (n, v) => Math.max(0, asList(v).length - Number(n)),
  eq: (a, b) => String(a) === String(b),
  ne: (a, b) => String(a) !== String(b),
  and: (...args) => args.every(truthy),
  or: (...args) => args.some(truthy),
  not: (v) => !truthy(v),
};

function evaluate(expr: string, scope: Scope): Value {
  const tokens: string[] = [];
  TOKEN.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = TOKEN.exec(expr)) !== null) tokens.push(m[1] ?? '');
  let pos = 0;

  const operand = (): Value | undefined => {
    const token = tokens[pos++] ?? '';
    if (token === '(') {
      const value = call();
      pos++;
      return value;
    }
    if (token.startsWith('"')) return JSON.parse(token) as string;
    if (/^-?\d/.test(token)) return Number(token);
    // Una variable no declarada es una reservada (tenant_name, view_in_browser_url...).
    const rootValue = (name: string) => scope.root.get(name) ?? sampleFor(name, undefined);
    if (token.startsWith('$.')) return rootValue(token.slice(2));
    if (token.startsWith('.')) {
      const name = token.slice(1);
      return scope.item ? scope.item[name] : rootValue(name);
    }
    return FUNCS[token]?.();
  };

  const call = (): Value => {
    const head = tokens[pos] ?? '';
    const fn = FUNCS[head];
    if (!fn) return operand() ?? '';
    pos++;
    const args: (Value | undefined)[] = [];
    while (pos < tokens.length && tokens[pos] !== ')') args.push(operand());
    return fn(...args);
  };

  return call();
}

function escapeHtml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
}

function run(nodes: Node[], scope: Scope): string {
  let out = '';
  for (const node of nodes) {
    switch (node.kind) {
      case 'text':
        out += node.value;
        break;
      case 'print': {
        const value = evaluate(node.expr, scope);
        out += escapeHtml(Array.isArray(value) ? '' : String(value ?? ''));
        break;
      }
      case 'if':
        out += run(truthy(evaluate(node.cond, scope)) ? node.then : node.else, scope);
        break;
      case 'range': {
        const items = asList(evaluate(node.expr, scope));
        out += items.length
          ? items.map((item) => run(node.body, { ...scope, item })).join('')
          : run(node.else, scope);
        break;
      }
    }
  }
  return out;
}

/** Ejecuta las acciones de plantilla del HTML compilado con valores de ejemplo. */
export function samplePreview(html: string, variables: readonly TemplateVariable[]): string {
  try {
    return run(parse(html), { root: sampleValues(variables), item: null });
  } catch {
    // Una plantilla que el evaluador no entiende se muestra sin sus acciones.
    return html.replace(ACTION, '');
  }
}
