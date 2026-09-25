import { describe, expect, it } from 'vitest';
import type { BrandKit } from '@/api/templates';
import { brandTokens } from '../editor/brand';
import { compileMjml } from '../editor/mjml';
import { htmlToText, readPreheader } from '../editor/mjmlSource';
import { GALLERY } from '.';

// Reglas de docs/Plan_Editor_Correos.md (3.5) que se pueden comprobar sin el servidor: la
// galeria no puede ofrecer una plantilla que el verificador rechace por construccion.

const GMAIL_CLIP_BYTES = 102 * 1024;
const MIN_VISIBLE_TEXT = 200;
const MAX_FIXED_WIDTH = 640;
const SHORTENERS =
  /(bit\.ly|tinyurl|t\.co|goo\.gl|ow\.ly|is\.gd|buff\.ly|cutt\.ly|rebrand\.ly|shorturl\.at)\//i;

const FULL_KIT: BrandKit = {
  logo_asset_id: '00000000-0000-4000-8000-000000000001',
  colors: ['#0B5FFF', '#111827'],
  fonts: ['Georgia'],
  footer: {
    company: 'Empresa de Prueba SAC',
    address: 'Calle de Prueba 123, Lima',
    website: 'https://empresa.example',
    support_email: 'ayuda@empresa.example',
  },
  image_hosts: [],
  updated_at: '2026-09-23T10:00:00Z',
};

const KITS = [
  { label: 'sin kit', tokens: brandTokens(null, null) },
  {
    label: 'con kit y logo',
    tokens: brandTokens(FULL_KIT, 'https://empresa.example/media/public/t/templates/logo.png'),
  },
];

function parse(html: string): Document {
  return new DOMParser().parseFromString(html, 'text/html');
}

describe('galeria de plantillas', () => {
  it('ofrece al menos las seis plantillas del plan', () => {
    const ids = GALLERY.map((g) => g.id);
    expect(ids).toEqual(
      expect.arrayContaining([
        'welcome',
        'promotion',
        'newsletter',
        'event',
        'abandoned-cart',
        'transactional',
      ]),
    );
    expect(new Set(ids).size).toBe(ids.length);
  });

  for (const { label, tokens } of KITS) {
    describe(label, () => {
      for (const template of GALLERY) {
        it(`${template.id}: compila sin errores y cumple las reglas basicas`, () => {
          const mjml = template.build(tokens);
          const { html, errors } = compileMjml(mjml);
          expect(errors).toEqual([]);

          const doc = parse(html);
          const links = [...doc.querySelectorAll('a[href]')].map(
            (a) => a.getAttribute('href') ?? '',
          );
          // La baja es del correo comercial. Un transaccional no la ofrece: no es una
          // suscripcion, y mezclarla lo acerca a Promociones.
          if (template.kind === 'marketing') expect(links).toContain('{{.unsubscribe_url}}');
          else {
            expect(links).not.toContain('{{.unsubscribe_url}}');
            expect(links).toContain('{{.view_in_browser_url}}');
          }
          expect(links.some((href) => href.startsWith('http://'))).toBe(false);
          expect(links.some((href) => SHORTENERS.test(href))).toBe(false);

          for (const img of doc.querySelectorAll('img')) {
            expect(img.getAttribute('alt')?.trim(), img.outerHTML).toBeTruthy();
            // Una imagen es del kit (https) o una variable de tipo imagen, que el servicio
            // solo acepta en https.
            expect(img.getAttribute('src') ?? '').toMatch(/^(https:\/\/|\{\{\.[a-z_]+\}\}$)/);
          }

          expect(new TextEncoder().encode(html).length).toBeLessThan(GMAIL_CLIP_BYTES);
          expect(htmlToText(html).length).toBeGreaterThanOrEqual(MIN_VISIBLE_TEXT);
          expect(html).not.toMatch(/<script|<iframe|<form|javascript:|\son[a-z]+=/i);
          expect(html).not.toMatch(/<link[^>]+stylesheet|@import/i);

          const widths = [...html.matchAll(/width:(\d+)px/g)].map((m) => Number(m[1]));
          expect(Math.max(...widths)).toBeLessThanOrEqual(MAX_FIXED_WIDTH);

          const preheader = readPreheader(mjml);
          expect(preheader.length).toBeGreaterThan(0);
          const first = doc.body.querySelector('div');
          expect(first?.getAttribute('style')).toContain('display:none');
          expect(first?.textContent?.trim()).toBe(preheader);
        });

        it(`${template.id}: ninguna etiqueta MJML repite un atributo`, () => {
          const mjml = template.build(tokens);
          for (const tag of mjml.match(/<mj-[a-z-]+\s[^>]*>/g) ?? []) {
            const names = [...tag.matchAll(/\s([a-z-]+)="/g)].map((m) => m[1]);
            expect(new Set(names).size, tag).toBe(names.length);
          }
        });

        it(`${template.id}: declara las variables que usa`, () => {
          const mjml = template.build(tokens);
          const declared = new Set(template.variables.map((v) => v.name));
          const fields = new Set(
            template.variables.flatMap((v) => v.fields ?? []).map((f) => f.name),
          );
          const reserved = new Set(['unsubscribe_url', 'view_in_browser_url', 'tenant_name']);
          // Toda referencia .nombre de una accion: fuera de un range es una variable, dentro
          // puede ser un campo de la lista.
          const actions = [...mjml.matchAll(/\{\{([^}]*)\}\}/g)].map((m) => m[1] ?? '');
          for (const action of actions) {
            for (const [, prefix, name] of action.matchAll(/(\$?)\.([a-z][a-z0-9_]*)/g)) {
              const ok =
                declared.has(name!) || reserved.has(name!) || (!prefix && fields.has(name!));
              expect(ok, `${name} en {{${action}}}`).toBe(true);
            }
          }
        });
      }
    });
  }

  it('usa los datos del kit y los escapa', () => {
    const kit: BrandKit = {
      ...FULL_KIT,
      footer: { ...FULL_KIT.footer, company: 'Perez & Hijos <SAC>' },
    };
    const html = compileMjml(GALLERY[0]!.build(brandTokens(kit, null))).html;
    expect(html).toContain('Perez &amp; Hijos &lt;SAC&gt;');
    expect(html).toContain('Calle de Prueba 123, Lima');
    expect(html).toContain('href="mailto:ayuda@empresa.example"');
    expect(html).toContain('#0B5FFF');
  });

  it('sin kit no inventa datos de la empresa', () => {
    const html = compileMjml(GALLERY[0]!.build(brandTokens(null, null))).html;
    expect(html).toContain('{{.tenant_name}}');
    expect(html).not.toContain('mailto:');
  });
});
