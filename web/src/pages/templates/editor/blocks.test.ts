import { describe, expect, it } from 'vitest';
import type { BrandKit } from '@/api/templates';
import { BLOCKS, legalFooter } from './blocks';
import { brandTokens, fontStack } from './brand';
import { compileMjml } from './mjml';

const KIT: BrandKit = {
  logo_asset_id: null,
  colors: ['#123456'],
  fonts: ['Open Sans'],
  footer: {
    company: 'Empresa',
    address: 'Direccion 1',
    website: 'javascript:alert(1)',
    support_email: 'no es un correo',
  },
  updated_at: null,
};

describe('bloques del editor', () => {
  it('estan todos los de la seccion 6 del plan', () => {
    const ids = BLOCKS.map((b) => b.id);
    for (const id of [
      'section',
      'columns-1',
      'columns-2',
      'columns-3',
      'columns-4',
      'text',
      'heading',
      'image',
      'button',
      'divider',
      'spacer',
      'social',
      'video',
      'discount',
      'legal-footer',
    ]) {
      expect(ids).toContain(id);
    }
  });

  it('cada bloque compila dentro de un documento MJML', () => {
    const tokens = brandTokens(KIT, null);
    for (const block of BLOCKS) {
      const inner = block.content(tokens);
      const body =
        block.placement === 'body'
          ? inner
          : `<mj-section><mj-column>${inner}</mj-column></mj-section>`;
      const { errors } = compileMjml(`<mjml><mj-body>${body}</mj-body></mjml>`);
      expect(errors, block.id).toEqual([]);
    }
  });

  it('el pie legal lleva la baja y descarta datos del kit que no son validos', () => {
    const footer = legalFooter(brandTokens(KIT, null));
    expect(footer).toContain('href="{{.unsubscribe_url}}"');
    expect(footer).toContain('Empresa · Direccion 1');
    expect(footer).not.toContain('javascript:');
    expect(footer).not.toContain('mailto:');
  });

  it('la tipografia del kit lleva siempre una alternativa segura', () => {
    expect(fontStack('Open Sans')).toBe("'Open Sans', Helvetica, Arial, sans-serif");
    expect(fontStack('Georgia, serif')).toBe('Georgia, serif');
    expect(fontStack(undefined)).toContain('Arial');
  });
});
