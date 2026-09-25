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

  it('recorre las listas, formatea importes y sigue la linea de estado', () => {
    const order = GALLERY.find((g) => g.id === 'order-confirmation')!;
    const html =
      '<p>{{if eq .status "received"}}recibido{{else if eq .status "shipped"}}enviado{{end}}</p>' +
      '{{range take 20 .items}}<b>{{.name}}</b> {{$.currency}} {{money .price}}{{end}}' +
      '{{if rest 1 .items}}<i>y {{rest 1 .items}} más</i>{{end}}{{if nonzero .discount}}desc{{end}}' +
      '<a href="{{.view_in_browser_url}}">{{.tenant_name}}</a>';
    const out = samplePreview(html, order.variables);
    expect(out).not.toContain('{{');
    expect(out).toContain('<p>recibido</p>');
    expect(out.match(/<b>/g)).toHaveLength(2);
    expect(out).toContain('1,024.00');
    expect(out).toContain('<i>y 1 más</i>');
    expect(out).toContain('desc');
    expect(out).toContain('href="#"');
    expect(out).not.toContain('undefined');
  });
});
