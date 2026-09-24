// Grafo de un flujo: la misma regla que services/automations/internal/domain/graph.go. El
// primer paso es la entrada, cada paso sigue en next y una rama en then o en else; un
// destino vacio es el fin. Aqui se valida en vivo para el lienzo; el servicio lo vuelve a
// validar al guardar.

export type Port = 'next' | 'then' | 'else';

export interface GraphNode {
  /** Clave estable para React. */
  key: string;
  /** Nombre del paso en el grafo (el que viaja al API). */
  id: string;
  type: string;
  next: string;
  then: string;
  else: string;
  /** Solo en una rama que mira un correo: id del paso de envio. */
  conditionStep?: string;
}

export const BRANCH = 'branch';
export const SEND_EMAIL = 'send_email';

export function portsOf(node: Pick<GraphNode, 'type'>): Port[] {
  return node.type === BRANCH ? ['then', 'else'] : ['next'];
}

export function targetOf(node: GraphNode, port: Port): string {
  return node[port];
}

/** Da ids y enlaces a una lista sin ninguno (flujo lineal): s1 -> s2 -> ... */
export function linkLegacy<T extends GraphNode>(nodes: readonly T[]): T[] {
  if (nodes.some((n) => n.id || n.next || n.then || n.else)) return [...nodes];
  const ids = nodes.map((_, i) => `s${i + 1}`);
  return nodes.map((n, i) => ({
    ...n,
    id: ids[i] ?? '',
    next: n.type !== BRANCH && i + 1 < nodes.length ? (ids[i + 1] ?? '') : '',
  }));
}

/** Primer id sN libre. */
export function freshId(nodes: readonly GraphNode[]): string {
  const used = new Set(nodes.map((n) => n.id));
  for (let i = 1; ; i++) {
    const id = `s${i}`;
    if (!used.has(id)) return id;
  }
}

function indexOf(nodes: readonly GraphNode[]): Map<string, number> {
  const out = new Map<string, number>();
  nodes.forEach((n, i) => {
    if (n.id && !out.has(n.id)) out.set(n.id, i);
  });
  return out;
}

function adjacency(nodes: readonly GraphNode[]): number[][] {
  const index = indexOf(nodes);
  return nodes.map((n) =>
    portsOf(n)
      .map((p) => targetOf(n, p))
      .filter(Boolean)
      .map((id) => index.get(id))
      .filter((j): j is number => j !== undefined),
  );
}

function reachable(adj: number[][], skip = -1): boolean[] {
  const seen = adj.map(() => false);
  if (adj.length === 0 || skip === 0) return seen;
  const stack = [0];
  seen[0] = true;
  while (stack.length) {
    const n = stack.pop() as number;
    for (const m of adj[n] ?? []) {
      if (m !== skip && !seen[m]) {
        seen[m] = true;
        stack.push(m);
      }
    }
  }
  return seen;
}

/** Posiciones que estan en algun ciclo (vacio si no hay). */
function inCycle(adj: number[][]): Set<number> {
  const out = new Set<number>();
  const color = adj.map(() => 0);
  const stack: number[] = [];
  const visit = (n: number) => {
    color[n] = 1;
    stack.push(n);
    for (const m of adj[n] ?? []) {
      if (color[m] === 1) {
        for (let i = stack.lastIndexOf(m); i < stack.length; i++) out.add(stack[i] as number);
      } else if (color[m] === 0) {
        visit(m);
      }
    }
    stack.pop();
    color[n] = 2;
  };
  adj.forEach((_, n) => {
    if (color[n] === 0) visit(n);
  });
  return out;
}

/** Profundidad (pasos desde la entrada, la entrada es 1) de cada posicion; 0 si no se llega. */
export function depths(nodes: readonly GraphNode[]): number[] {
  const adj = adjacency(nodes);
  const cyc = inCycle(adj);
  const out = nodes.map(() => 0);
  if (!nodes.length) return out;
  const order: number[] = [];
  const seen = nodes.map(() => false);
  const topo = (n: number) => {
    seen[n] = true;
    for (const m of adj[n] ?? []) if (!seen[m] && !cyc.has(m)) topo(m);
    order.push(n);
  };
  topo(0);
  out[0] = 1;
  for (const n of order.reverse()) {
    const dn = out[n] ?? 0;
    if (!dn) continue;
    for (const m of adj[n] ?? []) {
      if (!cyc.has(m) && (out[m] ?? 0) < dn + 1) out[m] = dn + 1;
    }
  }
  return out;
}

export type GraphIssue =
  | { kind: 'missingTarget'; target: string }
  | { kind: 'self' }
  | { kind: 'cycle' }
  | { kind: 'unreachable' }
  | { kind: 'sameTargets' }
  | { kind: 'tooDeep'; max: number }
  | { kind: 'duplicateId' }
  | { kind: 'conditionStepMissing' }
  | { kind: 'conditionStepNotSend' }
  | { kind: 'conditionStepNotDominating' };

/** Problemas por clave de paso. Sin problemas, el mapa esta vacio. */
export function validateGraph(
  nodes: readonly GraphNode[],
  maxDepth: number,
): Map<string, GraphIssue> {
  const issues = new Map<string, GraphIssue>();
  const flag = (i: number, issue: GraphIssue) => {
    const key = nodes[i]?.key;
    if (key && !issues.has(key)) issues.set(key, issue);
  };
  const index = indexOf(nodes);
  nodes.forEach((n, i) => {
    if (index.get(n.id) !== i) flag(i, { kind: 'duplicateId' });
    for (const p of portsOf(n)) {
      const target = targetOf(n, p);
      if (!target) continue;
      if (target === n.id) flag(i, { kind: 'self' });
      else if (!index.has(target)) flag(i, { kind: 'missingTarget', target });
    }
    if (n.type === BRANCH && n.then && n.then === n.else) flag(i, { kind: 'sameTargets' });
  });
  const adj = adjacency(nodes);
  for (const i of inCycle(adj)) flag(i, { kind: 'cycle' });
  const reach = reachable(adj);
  reach.forEach((ok, i) => {
    if (!ok) flag(i, { kind: 'unreachable' });
  });
  depths(nodes).forEach((d, i) => {
    if (d > maxDepth) flag(i, { kind: 'tooDeep', max: maxDepth });
  });
  nodes.forEach((n, i) => {
    if (n.type !== BRANCH || n.conditionStep === undefined) return;
    const j = index.get(n.conditionStep);
    if (j === undefined) flag(i, { kind: 'conditionStepMissing' });
    else if (nodes[j]?.type !== SEND_EMAIL) flag(i, { kind: 'conditionStepNotSend' });
    else if (!dominates(adj, j, i)) flag(i, { kind: 'conditionStepNotDominating' });
  });
  return issues;
}

function dominates(adj: number[][], d: number, b: number): boolean {
  if (d === b) return true;
  return !reachable(adj, d)[b];
}

/** Pasos de envio por los que pasa todo recorrido hasta la posicion b (los que una rama
 * en b puede mirar). */
export function dominatingSends<T extends GraphNode>(nodes: readonly T[], b: number): T[] {
  const adj = adjacency(nodes);
  return nodes.filter(
    (n, i) => n.type === SEND_EMAIL && i !== b && reachable(adj)[b] && dominates(adj, i, b),
  );
}

/** Anclaje donde se inserta un paso: la salida port del paso from, o la del disparador. */
export interface Anchor {
  from: string | null;
  port: Port;
}

/**
 * Inserta node en la salida anchor: lo que antes seguia ahi pasa a ser su continuacion (next,
 * o then si es una rama; else queda en el fin). Desde el disparador, el paso nuevo es la
 * nueva entrada y va primero en la lista.
 */
export function insertAt<T extends GraphNode>(nodes: readonly T[], anchor: Anchor, node: T): T[] {
  const entry = nodes[0]?.id ?? '';
  const prev =
    anchor.from === null ? entry : (nodes.find((n) => n.key === anchor.from)?.[anchor.port] ?? '');
  const inserted: T =
    node.type === BRANCH
      ? { ...node, then: prev, else: '', next: '' }
      : { ...node, next: prev, then: '', else: '' };
  if (anchor.from === null) return [inserted, ...nodes];
  return [
    ...nodes.map((n) => (n.key === anchor.from ? { ...n, [anchor.port]: node.id } : n)),
    inserted,
  ];
}

/**
 * Quita el paso y reengancha a quien apuntaba a el con su continuacion (next, o then en una
 * rama). Lo que deja de ser alcanzable (el camino else de una rama quitada) se quita tambien.
 */
export function removeNode<T extends GraphNode>(nodes: readonly T[], key: string): T[] {
  const gone = nodes.find((n) => n.key === key);
  if (!gone) return [...nodes];
  const continuation = gone.type === BRANCH ? gone.then : gone.next;
  let rest = nodes
    .filter((n) => n.key !== key)
    .map((n) => {
      const out = { ...n };
      for (const p of portsOf(n)) if (out[p] === gone.id) out[p] = continuation;
      return out;
    });
  if (nodes[0]?.key === key && continuation) {
    const entry = rest.find((n) => n.id === continuation);
    if (entry) rest = [entry, ...rest.filter((n) => n !== entry)];
  }
  if (!rest.length) return rest;
  const reach = reachable(adjacency(rest));
  return rest.filter((_, i) => reach[i]);
}

/** Reenlaza una salida a otro paso o al fin (''). */
export function relink<T extends GraphNode>(
  nodes: readonly T[],
  key: string,
  port: Port,
  target: string,
): T[] {
  return nodes.map((n) => (n.key === key ? { ...n, [port]: target } : n));
}

export interface Edge {
  from: string | null;
  port: Port;
  /** Clave del paso destino; null = fin. */
  to: string | null;
}

export interface Placed {
  key: string;
  layer: number;
  column: number;
}

export interface Layout {
  placed: Map<string, Placed>;
  layers: number;
  columns: number;
  edges: Edge[];
}

/**
 * Coloca los pasos por capas (la capa de un paso es su recorrido mas largo desde la entrada)
 * y, dentro de cada capa, en el orden en que los visita un recorrido que va antes por then
 * que por else. Lo que no se alcanza va en una capa final.
 */
export function layoutGraph(nodes: readonly GraphNode[]): Layout {
  const d = depths(nodes);
  const index = indexOf(nodes);
  const order: string[] = [];
  const seen = new Set<string>();
  const walk = (i: number) => {
    const n = nodes[i];
    if (!n || seen.has(n.key)) return;
    seen.add(n.key);
    order.push(n.key);
    for (const p of portsOf(n)) {
      const j = index.get(targetOf(n, p));
      if (j !== undefined) walk(j);
    }
  };
  if (nodes.length) walk(0);
  nodes.forEach((n) => {
    if (!seen.has(n.key)) order.push(n.key);
  });
  const maxLayer = Math.max(0, ...d);
  const byKey = new Map(nodes.map((n, i) => [n.key, i]));
  const perLayer = new Map<number, number>();
  const placed = new Map<string, Placed>();
  for (const key of order) {
    const i = byKey.get(key) as number;
    const layer = d[i] ? (d[i] as number) - 1 : maxLayer;
    const column = perLayer.get(layer) ?? 0;
    perLayer.set(layer, column + 1);
    placed.set(key, { key, layer, column });
  }
  const edges: Edge[] = [{ from: null, port: 'next', to: nodes[0]?.key ?? null }];
  for (const n of nodes) {
    for (const p of portsOf(n)) {
      const j = index.get(targetOf(n, p));
      edges.push({ from: n.key, port: p, to: j === undefined ? null : (nodes[j]?.key ?? null) });
    }
  }
  const layers = placed.size ? Math.max(...[...placed.values()].map((p) => p.layer)) + 1 : 0;
  const columns = Math.max(1, ...perLayer.values());
  return { placed, layers, columns, edges };
}
