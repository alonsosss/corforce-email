import { describe, expect, it } from 'vitest';
import { GALLERY } from '.';
import { samplePreview } from './samplePreview';

describe('samplePreview', () => {
  it('resuelve las condiciones y sustituye cada variable por un ejemplo de su tipo', () => {
    const html =
      '<p>Hola{{if .first_name}} {{.first_name}}{{end}},</p><a href="{{.cart_url}}">Ver</a>' +
      '<a href="{{.unsubscribe_url}}">Baja</a><span>{{ .order_number }}</span>';
    const out = samplePreview(html, [
      { name: 'first_name', type: 'string', required: false },
      { name: 'cart_url', type: 'url', required: true },
      { name: 'order_number', type: 'number', required: true },
    ]);
    expect(out).not.toContain('{{');
    expect(out).toContain('href="#"');
    expect(out).toContain('1024');
  });

  it('deja la plantilla sin acciones de plantilla en la vista previa de toda la galeria', () => {
    for (const template of GALLERY) {
      const withActions = `<p>{{if .first_name}}{{.first_name}}{{else}}x{{end}}</p>${template.subject}`;
      expect(samplePreview(withActions, template.variables)).not.toMatch(/\{\{/);
    }
  });
});
