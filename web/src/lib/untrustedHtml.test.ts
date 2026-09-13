import { describe, expect, it } from 'vitest';
import { prepareUntrustedHtml } from './untrustedHtml';

const TRACKER = 'https://tracker.test/pixel.gif?u=42';
const PIXEL_DATA = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';

const HOSTILE = `
<html><head>
  <meta http-equiv="refresh" content="0;url=https://tracker.test/visto">
  <link rel="stylesheet" href="https://tracker.test/estilos.css">
  <base href="https://tracker.test/">
  <style>@import url("https://tracker.test/import.css"); .x { background: url('https://tracker.test/fondo.png') }</style>
</head>
<body background="https://tracker.test/body.png">
  <img src="${TRACKER}" alt="pixel">
  <img srcset="https://tracker.test/a.png 1x, https://tracker.test/b.png 2x">
  <img src="${PIXEL_DATA}" alt="inline">
  <table><tr><td background="//tracker.test/td.png" style="background-image:url(https://tracker.test/celda.png)">x</td></tr></table>
  <svg><image href="https://tracker.test/svg.png"></image></svg>
  <iframe src="https://tracker.test/marco"></iframe>
  <video poster="https://tracker.test/poster.png"></video>
  <img src="/api/v1/relativa.png">
</body></html>`;

describe('HTML ajeno antes del iframe', () => {
  it('por defecto no deja ninguna referencia remota y cuenta las imagenes bloqueadas', () => {
    const { html, remoteImages } = prepareUntrustedHtml(HOSTILE, { allowRemoteImages: false });
    expect(html).not.toMatch(/tracker\.test/);
    expect(html).not.toContain('/api/v1/relativa.png');
    expect(html).toContain(PIXEL_DATA);
    expect(remoteImages).toBe(9);
    expect(html).toContain('img-src data:;');
    expect(html).toContain("default-src 'none'");
  });

  it('quita siempre lo que carga por si solo aunque se muestren las imagenes', () => {
    const { html } = prepareUntrustedHtml(HOSTILE, { allowRemoteImages: true });
    for (const leak of ['visto', 'estilos.css', 'import.css', 'marco', '<base', 'refresh']) {
      expect(html, leak).not.toContain(leak);
    }
  });

  it('al pedirlo muestra solo las imagenes http o https y lo declara en su CSP', () => {
    const { html, remoteImages } = prepareUntrustedHtml(HOSTILE, { allowRemoteImages: true });
    expect(html).toContain(TRACKER.replace('&', '&amp;'));
    expect(html).toContain('https://tracker.test/celda.png');
    expect(html).not.toContain('/api/v1/relativa.png');
    expect(html).not.toContain('//tracker.test/td.png"');
    expect(html).toContain('img-src data: https: http:');
    expect(remoteImages).toBe(9);
  });

  it('un documento sin imagenes remotas no avisa de nada', () => {
    const { remoteImages } = prepareUntrustedHtml(`<p>Hola</p><img src="${PIXEL_DATA}">`, {
      allowRemoteImages: false,
    });
    expect(remoteImages).toBe(0);
  });
});
