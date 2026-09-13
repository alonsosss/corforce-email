import { describe, expect, it } from 'vitest';
import { prepareUntrustedHtml } from './untrustedHtml';

describe('HTML ajeno con imagenes propias y enlaces', () => {
  it('sustituye solo por data: de mapa de bits y no las cuenta como remotas', () => {
    const inlineImages = new Map([
      ['/parts/2', 'data:image/png;base64,AAAA'],
      ['/parts/3', 'data:text/html;base64,PHNjcmlwdD4='],
    ]);
    const prepared = prepareUntrustedHtml('<img src="/parts/2"><img src="/parts/3">', {
      allowRemoteImages: false,
      inlineImages,
    });
    expect(prepared.html).toContain('src="data:image/png;base64,AAAA"');
    expect(prepared.html).not.toContain('text/html');
    expect(prepared.html).not.toContain('/parts/3');
    expect(prepared.remoteImages).toBe(1);
  });

  it('los enlaces se abren fuera del marco con una base sin href', () => {
    const prepared = prepareUntrustedHtml('<a href="https://x.test">x</a>', {
      allowRemoteImages: false,
      openLinksInNewTab: true,
    });
    expect(prepared.html).toContain('<base target="_blank">');
    expect(prepared.html).not.toMatch(/<base[^>]*href/);
  });

  it('una base del propio documento se quita siempre y nunca manda el referer', () => {
    const prepared = prepareUntrustedHtml('<base href="https://evil.test/"><p>x</p>', {
      allowRemoteImages: true,
    });
    expect(prepared.html).not.toContain('evil.test');
    expect(prepared.html).toContain('<meta name="referrer" content="no-referrer">');
  });
});
