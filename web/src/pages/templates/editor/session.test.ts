import { describe, expect, it } from 'vitest';
import type { TemplateDetail, TemplateVersion, VersionSummary } from '@/api/templates';
import { variablesToDrafts } from '../variables';
import { applyGallery, baseVersionNumber, initialDocument, savedDraft } from './session';

function summary(version: number, status: VersionSummary['status']): VersionSummary {
  return {
    id: `v${version}`,
    version,
    status,
    published_at: null,
    created_by: 'u',
    created_at: '2026-09-23T10:00:00Z',
  };
}

function detail(current: number, versions: VersionSummary[]): TemplateDetail {
  return {
    id: 't',
    name: 'Plantilla',
    description: '',
    kind: 'marketing',
    status: 'active',
    current_version: current,
    created_by: 'u',
    created_at: '2026-09-23T10:00:00Z',
    updated_at: '2026-09-23T10:00:00Z',
    current: null,
    versions,
  };
}

function version(partial: Partial<TemplateVersion>): TemplateVersion {
  return {
    id: 'v',
    template_id: 't',
    version: 1,
    subject: 'Asunto',
    html: '<p>x</p>',
    text: null,
    variables: null,
    status: 'draft',
    published_at: null,
    created_by: 'u',
    created_at: '2026-09-23T10:00:00Z',
    ...partial,
  };
}

describe('version de partida del editor', () => {
  it('abre el borrador mas reciente', () => {
    expect(baseVersionNumber(detail(1, [summary(1, 'published'), summary(2, 'draft')]))).toBe(2);
  });

  it('sin borrador parte de la publicada', () => {
    expect(baseVersionNumber(detail(2, [summary(1, 'superseded'), summary(2, 'published')]))).toBe(
      2,
    );
  });

  it('sin versiones empieza en blanco', () => {
    expect(baseVersionNumber(detail(0, []))).toBeNull();
  });
});

describe('borrador publicable sin guardar', () => {
  const editor = { kind: 'grapesjs-mjml' as const, project: {}, mjml: '<mjml></mjml>' };

  it('solo un borrador disenado con el editor', () => {
    expect(savedDraft(version({ version: 3, editor }))).toBe(3);
    expect(savedDraft(version({ version: 3, editor: null }))).toBeNull();
    expect(savedDraft(version({ version: 3, status: 'published', editor }))).toBeNull();
    expect(savedDraft(null)).toBeNull();
  });

  it('el preheader sale del MJML guardado', () => {
    const doc = initialDocument(
      version({
        editor: {
          ...editor,
          mjml: '<mjml><mj-head><mj-preview>Hola</mj-preview></mj-head></mjml>',
        },
      }),
    );
    expect(doc.preheader).toBe('Hola');
    expect(doc.subject).toBe('Asunto');
  });
});

describe('aplicar una plantilla de la galeria', () => {
  it('conserva el asunto escrito y anade solo las variables que faltan', () => {
    const doc = {
      subject: 'Mi asunto',
      preheader: '',
      text: '',
      variables: variablesToDrafts([{ name: 'first_name', type: 'string', required: false }]),
    };
    const applied = applyGallery(
      doc,
      '<mjml><mj-head><mj-preview>Pre</mj-preview></mj-head><mj-body></mj-body></mjml>',
      'Asunto de la galeria',
      [
        { name: 'first_name', type: 'string', required: false },
        { name: 'cart_url', type: 'url', required: true },
      ],
    );
    expect(applied.doc.subject).toBe('Mi asunto');
    expect(applied.doc.preheader).toBe('Pre');
    expect(applied.doc.variables.map((v) => v.name)).toEqual(['first_name', 'cart_url']);
    expect(applied.canvas).toBe('<mjml><mj-body></mj-body></mjml>');
  });

  it('rellena el asunto si estaba vacio', () => {
    const applied = applyGallery(
      { subject: ' ', preheader: '', text: '', variables: [] },
      '<mjml><mj-body></mj-body></mjml>',
      'Asunto de la galeria',
      [],
    );
    expect(applied.doc.subject).toBe('Asunto de la galeria');
  });
});
