import { describe, expect, it } from 'vitest';
import type { FormEmbed } from '@/api/forms';
import { iframeSnippet, scriptSnippet, submitBodyExample } from './embed';

const EMBED: FormEmbed = {
  key: 'fk_1',
  iframe_url: 'https://forms.plataforma.test/f/fk_1',
  script_url: 'https://forms.plataforma.test/f/fk_1/embed.js',
  definition_url: 'https://forms.plataforma.test/api/f/fk_1',
  submit_url: 'https://forms.plataforma.test/api/f/fk_1/submit',
};

describe('codigo para incrustar un formulario', () => {
  it('el script carga la URL del servicio de forma asincrona', () => {
    expect(scriptSnippet(EMBED)).toBe(
      '<script src="https://forms.plataforma.test/f/fk_1/embed.js" async></script>',
    );
  });

  it('el iframe lleva titulo, ancho completo, alto minimo y carga diferida', () => {
    expect(iframeSnippet(EMBED, 'Boletin')).toBe(
      '<iframe src="https://forms.plataforma.test/f/fk_1" title="Boletin" ' +
        'style="width:100%;border:0;min-height:480px" loading="lazy"></iframe>',
    );
  });

  it('un titulo con comillas o etiquetas no se sale del atributo', () => {
    const snippet = iframeSnippet(EMBED, 'Ofertas "VIP" <script>');
    expect(snippet).toContain('title="Ofertas &quot;VIP&quot; &lt;script&gt;"');
    expect(snippet).not.toContain('<script>');
  });

  it('el cuerpo del envio lleva token, un valor por campo, consentimiento y trampa vacia', () => {
    const body = JSON.parse(
      submitBodyExample(
        [
          { key: 'email', label: 'Correo', required: true, placeholder: '' },
          { key: 'first_name', label: 'Nombre', required: false, placeholder: '' },
        ],
        'TOKEN',
      ),
    ) as unknown;
    expect(body).toEqual({
      token: 'TOKEN',
      fields: { email: '', first_name: '' },
      consent: true,
      homepage: '',
    });
  });
});
