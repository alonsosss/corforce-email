import { describe, expect, it } from 'vitest';
import {
  dominatingSends,
  insertAt,
  layoutGraph,
  linkLegacy,
  removeNode,
  relink,
  validateGraph,
  type GraphNode,
} from './workflowGraph';

function node(id: string, type = 'wait', links: Partial<GraphNode> = {}): GraphNode {
  return { key: `k-${id}`, id, type, next: '', then: '', else: '', ...links };
}

const ids = (nodes: GraphNode[]) => nodes.map((n) => n.id);

describe('grafo de un flujo', () => {
  it('encadena una lista sin ids como el servicio', () => {
    const linked = linkLegacy([node(''), node('', 'send_email'), node('')]);
    expect(linked.map((n) => [n.id, n.next])).toEqual([
      ['s1', 's2'],
      ['s2', 's3'],
      ['s3', ''],
    ]);
    const already = [node('a', 'wait', { next: 'b' }), node('b')];
    expect(linkLegacy(already)).toEqual(already);
  });

  it('inserta en una conexion y el paso nuevo hereda lo que seguia', () => {
    const flow = [node('a', 'wait', { next: 'b' }), node('b')];
    const mid = insertAt(flow, { from: 'k-a', port: 'next' }, node('x'));
    expect(mid.find((n) => n.id === 'a')?.next).toBe('x');
    expect(mid.find((n) => n.id === 'x')?.next).toBe('b');

    const first = insertAt(flow, { from: null, port: 'next' }, node('y'));
    expect(first[0]?.id).toBe('y');
    expect(first[0]?.next).toBe('a');

    const branch = insertAt(flow, { from: 'k-a', port: 'next' }, node('r', 'branch'));
    const r = branch.find((n) => n.id === 'r');
    expect([r?.then, r?.else, r?.next]).toEqual(['b', '', '']);
    expect(validateGraph(branch, 20).size).toBe(0);
  });

  it('quitar un paso reengancha y quitar una rama se lleva su camino No', () => {
    const flow = [node('a', 'wait', { next: 'b' }), node('b', 'wait', { next: 'c' }), node('c')];
    expect(ids(removeNode(flow, 'k-b'))).toEqual(['a', 'c']);
    expect(removeNode(flow, 'k-b')[0]?.next).toBe('c');
    expect(ids(removeNode(flow, 'k-a'))).toEqual(['b', 'c']);

    const withBranch = [
      node('r', 'branch', { then: 'si', else: 'no' }),
      node('si', 'wait', { next: 'fin' }),
      node('no', 'wait', { next: 'solo-no' }),
      node('fin'),
      node('solo-no', 'wait'),
    ];
    const left = removeNode(withBranch, 'k-r');
    expect(ids(left)).toEqual(['si', 'fin']);
    expect(validateGraph(left, 20).size).toBe(0);
  });

  it('detecta ciclos, destinos que no existen, pasos sueltos y la profundidad', () => {
    const cycle = [node('a', 'wait', { next: 'b' }), node('b', 'wait', { next: 'a' })];
    expect([...validateGraph(cycle, 20).values()].map((i) => i.kind)).toContain('cycle');
    expect(validateGraph([node('a', 'wait', { next: 'a' })], 20).get('k-a')?.kind).toBe('self');
    expect(validateGraph([node('a', 'wait', { next: 'zz' })], 20).get('k-a')).toEqual({
      kind: 'missingTarget',
      target: 'zz',
    });
    expect(validateGraph([node('a'), node('b')], 20).get('k-b')?.kind).toBe('unreachable');
    expect(
      validateGraph([node('r', 'branch', { then: 'a', else: 'a' }), node('a')], 20).get('k-r')
        ?.kind,
    ).toBe('sameTargets');
    expect(validateGraph([node('a'), node('a')], 20).get('k-a')?.kind).toBe('duplicateId');
    const chain = Array.from({ length: 4 }, (_, i) =>
      node(`p${i}`, 'wait', { next: i < 3 ? `p${i + 1}` : '' }),
    );
    expect(validateGraph(chain, 3).get('k-p3')).toEqual({ kind: 'tooDeep', max: 3 });
    expect(validateGraph(chain, 4).size).toBe(0);
  });

  it('una rama que mira un correo exige un envio por el que pase todo camino', () => {
    const ok = [
      node('e', 'send_email', { next: 'r' }),
      node('r', 'branch', { conditionStep: 'e' }),
    ];
    expect(validateGraph(ok, 20).size).toBe(0);
    expect(ids(dominatingSends(ok, 1))).toEqual(['e']);

    const split = [
      node('s', 'branch', { then: 'e', else: 'w' }),
      node('e', 'send_email', { next: 'r' }),
      node('w', 'wait', { next: 'r' }),
      node('r', 'branch', { conditionStep: 'e' }),
    ];
    expect(validateGraph(split, 20).get('k-r')?.kind).toBe('conditionStepNotDominating');
    expect(dominatingSends(split, 3)).toEqual([]);
    const notSend = [node('w', 'wait', { next: 'r' }), node('r', 'branch', { conditionStep: 'w' })];
    expect(validateGraph(notSend, 20).get('k-r')?.kind).toBe('conditionStepNotSend');
    const missing = [node('r', 'branch', { conditionStep: 'zz' })];
    expect(validateGraph(missing, 20).get('k-r')?.kind).toBe('conditionStepMissing');
  });

  it('coloca los pasos por capas y las ramas una junto a otra', () => {
    const flow = relink(
      [
        node('r', 'branch', { then: 'a', else: 'b' }),
        node('a', 'wait', { next: 'c' }),
        node('b'),
        node('c'),
      ],
      'k-b',
      'next',
      'c',
    );
    const { placed, layers, columns, edges } = layoutGraph(flow);
    expect(placed.get('k-r')).toMatchObject({ layer: 0, column: 0 });
    expect(placed.get('k-a')).toMatchObject({ layer: 1, column: 0 });
    expect(placed.get('k-b')).toMatchObject({ layer: 1, column: 1 });
    expect(placed.get('k-c')?.layer).toBe(2);
    expect(layers).toBe(3);
    expect(columns).toBe(2);
    expect(edges.filter((e) => e.to === 'k-c')).toHaveLength(2);
    expect(edges[0]).toEqual({ from: null, port: 'next', to: 'k-r' });
    expect(edges.find((e) => e.from === 'k-c')?.to).toBeNull();
  });
});
