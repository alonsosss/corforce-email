import { describe, expect, it } from 'vitest';
import type { PageDetail, PageVersion } from '@/api/pages';
import { t } from '@/i18n';
import { IMAGE_PLACEHOLDER } from '../../editor/blocks';
import { brandTokens } from '../../editor/brand';
import {
  basePageVersion,
  buildPageContent,
  initialPageDocument,
  previewDocument,
  savedPageDraft,
} from './pageContent';
import { escapeHtml, formMarker, WEB_BLOCKS } from './webBlocks';

const META = {
  max_title_length: 20,
  max_description_length: 40,
  max_html_bytes: 1000,
  max_css_bytes: 200,
  max_editor_bytes: 2000,
};

const CANVAS = {
  html: '<section><h2>Hola</h2></section>',
  css: 'h2{color:#123456;}',
  project: { pages: [{ id: 'p1' }] },
};

function version(partial: Partial<PageVersion>): PageVersion {
  return {
    id: 'v',
    page_id: 'p',
    version: 1,
    title: 'Titulo',
    description: '',
    html: '',
    css: '',
    editor: null,
    status: 'draft',
    published_at: null,
    created_by: 'u',
    created_at: '2026-09-01T00:00:00Z',
    ...partial,
  };
}

describe('version de una pagina desde el editor', () => {
  it('arma el cuerpo con el HTML, el CSS y el proyecto del editor web', () => {
    const { content, errors } = buildPageContent(
      { title: '  Oferta  ', description: ' Resumen ' },
      CANVAS,
      META,
    );
    expect(errors).toEqual({});
    expect(content).toEqual({
      title: 'Oferta',
      description: 'Resumen',
      html: CANVAS.html,
      css: CANVAS.css,
      editor: { kind: 'grapesjs-web', project: CANVAS.project },
    });
  });

  it('exige titulo y respeta los topes de texto', () => {
    const empty = buildPageContent({ title: ' ', description: '' }, CANVAS, META);
    expect(empty.content).toBeNull();
    expect(empty.errors.title).toBe(t('validation.required'));

    const long = buildPageContent({ title: 'T', description: 'd'.repeat(41) }, CANVAS, META);
    expect(long.errors.description).toBe(t('validation.maxLength', { n: 40 }));
  });

  it('comprueba max_editor_bytes en bytes UTF-8 antes de enviar', () => {
    const heavy = { ...CANVAS, project: { text: 'ñ'.repeat(1000) } };
    const result = buildPageContent({ title: 'T', description: '' }, heavy, META);
    expect(result.content).toBeNull();
    expect(result.errors.content).toBe(t('templates.pages.editor.tooLarge', { n: 2000 }));
  });

  it('rechaza HTML o CSS por encima de su tope e imagenes sin elegir', () => {
    const doc = { title: 'T', description: '' };
    expect(buildPageContent(doc, { ...CANVAS, css: 'a'.repeat(201) }, META).errors.content).toBe(
      t('templates.pages.editor.cssTooLarge', { n: 200 }),
    );
    expect(buildPageContent(doc, { ...CANVAS, html: 'a'.repeat(1001) }, META).errors.content).toBe(
      t('templates.pages.editor.htmlTooLarge', { n: 1000 }),
    );
    expect(
      buildPageContent(doc, { ...CANVAS, html: `<img src="${IMAGE_PLACEHOLDER}">` }, META).errors
        .content,
    ).toBe(t('templates.pages.editor.imagePending'));
  });

  it('abre el borrador mas reciente y solo reutiliza uno hecho con el editor web', () => {
    const detail = {
      page: { current_version: 1 },
      versions: [
        { version: 1, status: 'published' },
        { version: 2, status: 'draft' },
      ],
    } as unknown as PageDetail;
    expect(basePageVersion(detail)).toBe(2);
    expect(savedPageDraft(version({ version: 2 }))).toBeNull();
    expect(
      savedPageDraft(version({ version: 2, editor: { kind: 'grapesjs-web', project: {} } })),
    ).toBe(2);
    expect(
      savedPageDraft(
        version({ status: 'published', editor: { kind: 'grapesjs-web', project: {} } }),
      ),
    ).toBeNull();
  });

  it('sin version el titulo arranca con el nombre de la pagina', () => {
    expect(initialPageDocument(null, 'Lanzamiento')).toEqual({
      title: 'Lanzamiento',
      description: '',
    });
  });

  it('la vista previa no deja que el CSS cierre su hoja', () => {
    const doc = previewDocument({
      title: 'A <b>',
      html: '<p>Hola</p>',
      css: 'p{color:red}</style><script>x()</script>',
    });
    expect(doc).toContain('<title>A &lt;b&gt;</title>');
    expect(doc).not.toContain('</style><script>');
    expect(doc).toContain('<body><p>Hola</p></body>');
  });
});

describe('bloques del editor de paginas', () => {
  const brand = brandTokens(null, null);

  it('estan todos los del encargo y ninguno lleva scripts ni formularios', () => {
    expect(WEB_BLOCKS.map((b) => b.id)).toEqual([
      'section',
      'columns-2',
      'columns-3',
      'heading',
      'text',
      'image',
      'button',
      'divider',
      'spacer',
    ]);
    for (const block of WEB_BLOCKS) {
      const html = block.content(brand);
      expect(html).not.toMatch(/<script|<form|<iframe|\son[a-z]+=/i);
    }
  });

  it('el marcador del formulario lleva la clave y el alto', () => {
    expect(formMarker('fk_1')).toBe('<div data-cf-form="fk_1" data-cf-height="480"></div>');
    expect(formMarker('fk_1', 600)).toBe('<div data-cf-form="fk_1" data-cf-height="600"></div>');
  });

  it('una clave con comillas no se sale del atributo', () => {
    expect(formMarker('a"><script>')).toBe(
      `<div data-cf-form="${escapeHtml('a"><script>')}" data-cf-height="480"></div>`,
    );
    expect(formMarker('a"><script>')).not.toContain('<script>');
  });
});
