import { describe, expect, it } from 'vitest';
import { normalizeTemplateKey } from './templateKey';

describe('normalizeTemplateKey', () => {
  it('admite segmentos en minusculas separados por punto', () => {
    expect(normalizeTemplateKey(' pedido.confirmado ', 64)).toBe('pedido.confirmado');
    expect(normalizeTemplateKey('pedido-enviado_v2', 64)).toBe('pedido-enviado_v2');
  });

  it('rechaza mayusculas, espacios, puntos sueltos y lo que pasa del tope', () => {
    for (const bad of ['Pedido', 'pedido x', 'pedido..x', '.pedido', 'pedido/x', '1pedido']) {
      expect(normalizeTemplateKey(bad, 64), bad).toBeNull();
    }
    expect(normalizeTemplateKey('a'.repeat(65), 64)).toBeNull();
  });
});
