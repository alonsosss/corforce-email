import { describe, expect, it } from 'vitest';
import {
  escapeMjml,
  htmlToText,
  readPreheader,
  stripPreheader,
  writePreheader,
} from './mjmlSource';

const BODY =
  '<mjml><mj-body><mj-section><mj-column><mj-text>Hola</mj-text></mj-column></mj-section></mj-body></mjml>';

describe('preheader en el MJML', () => {
  it('lo crea con su cabecera si no existe y lo lee de vuelta', () => {
    const out = writePreheader(BODY, 'Novedades & <ofertas>');
    expect(out).toContain(
      '<mjml><mj-head><mj-preview>Novedades &amp; &lt;ofertas&gt;</mj-preview></mj-head>',
    );
    expect(readPreheader(out)).toBe('Novedades & <ofertas>');
  });

  it('reemplaza el anterior y conserva el resto de la cabecera', () => {
    const withHead =
      '<mjml><mj-head><mj-title>T</mj-title><mj-preview>viejo</mj-preview></mj-head><mj-body></mj-body></mjml>';
    const out = writePreheader(withHead, 'nuevo');
    expect(out.match(/<mj-preview>/g)).toHaveLength(1);
    expect(out).toContain('<mj-title>T</mj-title>');
    expect(readPreheader(out)).toBe('nuevo');
  });

  it('vacio lo quita, y stripPreheader deja el lienzo sin cabecera vacia', () => {
    const out = writePreheader(writePreheader(BODY, 'algo'), '   ');
    expect(readPreheader(out)).toBe('');
    expect(stripPreheader(writePreheader(BODY, 'algo'))).toBe(BODY);
  });
});

describe('escapeMjml', () => {
  it('escapa lo que rompe el contenido o un atributo', () => {
    expect(escapeMjml(`a&b<c>"d"`)).toBe('a&amp;b&lt;c&gt;&quot;d&quot;');
  });
});

describe('htmlToText', () => {
  it('saca el texto visible con los enlaces y sin el preheader oculto', () => {
    const html =
      '<html><head><style>p{}</style></head><body>' +
      '<div style="display:none;max-height:0">oculto</div>' +
      '<p>Hola <strong>Ana</strong></p>' +
      '<p><a href="https://acme.example/oferta">Ver oferta</a></p>' +
      '<p><a href="{{.unsubscribe_url}}">Darte de baja</a></p>' +
      '</body></html>';
    const text = htmlToText(html);
    expect(text).not.toContain('oculto');
    expect(text).toContain('Hola Ana');
    expect(text).toContain('Ver oferta (https://acme.example/oferta)');
    expect(text).toContain('Darte de baja ({{.unsubscribe_url}})');
  });
});
