import { describe, expect, it } from 'vitest';
import type { LinkReport } from '@/api/analytics';
import { heatmapHtml, normalizeLink } from './linkHeatmap';

function link(url: string, clicks: number, other = false): LinkReport {
  return {
    url,
    other,
    clicks,
    unique_clicks: clicks,
    first_clicked_at: '2026-09-20T10:00:00Z',
    last_clicked_at: '2026-09-21T10:00:00Z',
  };
}

describe('normalizeLink sigue la regla de analytics', () => {
  it.each([
    ['https://tienda.test/o?utm_source=x&utm_medium=email&id=9', 'https://tienda.test/o?id=9'],
    ['HTTPS://Tienda.TEST:443/Oferta', 'https://tienda.test/Oferta'],
    ['http://tienda.test:8080/o', 'http://tienda.test:8080/o'],
    ['https://tienda.test', 'https://tienda.test/'],
    ['https://tienda.test/p?UTM_Campaign=c#precio', 'https://tienda.test/p#precio'],
    ['https://tienda.test/p?utm%5Fsource=x&b=2&a=1', 'https://tienda.test/p?b=2&a=1'],
    ['https://usuario:clave@tienda.test/o', 'https://tienda.test/o'],
    ['  https://tienda.test/o  ', 'https://tienda.test/o'],
  ])('%s', (raw, want) => {
    expect(normalizeLink(raw)).toBe(want);
  });

  it.each(['mailto:ana@example.com', 'tel:+51', '/relativa', '#ancla', 'javascript:void(0)', ''])(
    '%s no es agregable',
    (raw) => {
      expect(normalizeLink(raw)).toBeNull();
    },
  );
});

describe('heatmapHtml', () => {
  const html =
    '<p><a href="https://tienda.test/oferta?utm_source=a">Oferta</a> ' +
    '<a href="https://tienda.test/blog">Blog</a> <a href="https://tienda.test/nuevo">Nuevo</a> ' +
    '<a href="mailto:ventas@tienda.test">Correo</a></p>';
  const links = [
    link('https://tienda.test/oferta', 6),
    link('https://tienda.test/blog', 2),
    link('https://tienda.test/personal?c=1', 1),
    link('', 1, true),
  ];
  const label = (share: number, clicks: number) => `${Math.round(share * 100)}%/${clicks}`;

  it('marca cada enlace http con su parte de los clics y deja los demas', () => {
    const out = heatmapHtml(html, links, { totalClicks: 10, label });
    const doc = new DOMParser().parseFromString(out.html, 'text/html');
    const anchors = [...doc.querySelectorAll('a')];
    expect(anchors.map((a) => a.getAttribute('data-cf-heat'))).toEqual([
      'hot',
      'hot',
      'cold',
      null,
    ]);
    expect(anchors.map((a) => a.querySelector('.cf-heat-badge')?.textContent ?? null)).toEqual([
      '60%/6',
      '20%/2',
      '0%/0',
      null,
    ]);
    expect(anchors[0]?.getAttribute('style')).toContain('--cf-heat:1.000');
    expect(anchors[1]?.getAttribute('style')).toContain('--cf-heat:0.333');
    expect(out.previewLinks).toBe(3);
    expect([...out.matched].sort()).toEqual([
      'https://tienda.test/blog',
      'https://tienda.test/oferta',
    ]);
    expect(doc.querySelector('style')?.textContent).toContain('.cf-heat-badge');
  });

  it('sin clics todo sale en cero sin dividir por cero', () => {
    const out = heatmapHtml(html, [], { totalClicks: 0, label });
    const doc = new DOMParser().parseFromString(out.html, 'text/html');
    expect([...doc.querySelectorAll('.cf-heat-badge')].map((b) => b.textContent)).toEqual([
      '0%/0',
      '0%/0',
      '0%/0',
    ]);
    expect(out.matched.size).toBe(0);
  });

  it('conserva el estilo propio del enlace', () => {
    const out = heatmapHtml(
      '<a href="https://tienda.test/o" style="color:red">x</a>',
      [link('https://tienda.test/o', 1)],
      {
        totalClicks: 1,
        label,
      },
    );
    const a = new DOMParser().parseFromString(out.html, 'text/html').querySelector('a');
    expect(a?.getAttribute('style')).toMatch(/^color:red;--cf-heat:1\.000$/);
  });
});
