// Prepara HTML que no es de la aplicacion (plantillas, pies de pagina, avisos) antes de
// darselo a un iframe aislado. El iframe hereda la CSP del gateway, que permite img-src
// https:, asi que una imagen remota se cargaria al abrir la vista previa: un pixel de
// seguimiento sabria quien mira y desde que IP. Por defecto se quitan todas las
// referencias remotas; las imagenes se muestran solo si quien mira lo pide.
//
// Dos capas: se reescriben las referencias en el documento y ademas se le pone una CSP
// propia (meta http-equiv) que no deja cargar nada remoto salvo las imagenes permitidas.

export interface PreparedHtml {
  html: string;
  /** Referencias a imagenes remotas encontradas (bloqueadas o mostradas). */
  remoteImages: number;
}

export interface PrepareOptions {
  allowRemoteImages: boolean;
}

// Elementos que cargan algo por si solos y no son imagenes: se quitan siempre. meta
// http-equiv incluye refresh, que navegaria el iframe a una URL remota.
const ALWAYS_REMOVED = [
  'base',
  'link',
  'meta[http-equiv]',
  'script',
  'iframe',
  'frame',
  'frameset',
  'object',
  'embed',
  'applet',
  'portal',
  'audio',
  'video',
  'track',
];

// Atributos que cargan una imagen al pintar el documento.
const IMAGE_ATTRIBUTES = ['src', 'background', 'lowsrc', 'dynsrc', 'poster'];
const SVG_IMAGE_SELECTOR = 'image, feImage, use';
const CSS_URL = /url\(\s*(['"]?)(.*?)\1\s*\)/gi;
const CSS_IMPORT = /@import[^;]*;?/gi;

function isInline(url: string): boolean {
  return /^data:/i.test(url.trim());
}

function isWebImage(url: string): boolean {
  return /^https?:\/\//i.test(url.trim());
}

/** srcset: lista de "url descriptor" separados por comas. */
function srcsetUrls(value: string): string[] {
  return value
    .split(',')
    .map((candidate) => candidate.trim().split(/\s+/)[0] ?? '')
    .filter(Boolean);
}

function contentSecurityPolicy(allowRemoteImages: boolean): string {
  const images = allowRemoteImages ? 'img-src data: https: http:' : 'img-src data:';
  return [
    "default-src 'none'",
    images,
    "style-src 'unsafe-inline'",
    "font-src 'none'",
    "media-src 'none'",
    "frame-src 'none'",
    "form-action 'none'",
    "base-uri 'none'",
  ].join('; ');
}

export function prepareUntrustedHtml(source: string, options: PrepareOptions): PreparedHtml {
  const doc = new DOMParser().parseFromString(source, 'text/html');
  const allow = options.allowRemoteImages;
  let remoteImages = 0;

  // Decide si una URL de imagen se conserva; cuenta las remotas.
  const keepImage = (url: string): boolean => {
    if (isInline(url)) return true;
    remoteImages += 1;
    return allow && isWebImage(url);
  };

  const rewriteCss = (css: string): string =>
    css.replace(CSS_IMPORT, '').replace(CSS_URL, (whole, _q: string, url: string) => {
      if (url.startsWith('#')) return whole;
      return keepImage(url) ? whole : 'none';
    });

  doc.querySelectorAll(ALWAYS_REMOVED.join(',')).forEach((el) => el.remove());

  doc.querySelectorAll('*').forEach((el) => {
    for (const name of IMAGE_ATTRIBUTES) {
      const value = el.getAttribute(name);
      if (value !== null && !keepImage(value)) el.removeAttribute(name);
    }
    const srcset = el.getAttribute('srcset');
    if (srcset !== null) {
      // map antes que every: cada candidato cuenta aunque el primero ya se bloquee.
      const kept = srcsetUrls(srcset).map(keepImage);
      if (!kept.every(Boolean)) el.removeAttribute('srcset');
    }
    const style = el.getAttribute('style');
    if (style !== null) el.setAttribute('style', rewriteCss(style));
  });

  doc.querySelectorAll(SVG_IMAGE_SELECTOR).forEach((el) => {
    for (const name of ['href', 'xlink:href']) {
      const value = el.getAttribute(name);
      if (value === null || value.startsWith('#')) continue;
      if (!keepImage(value)) el.removeAttribute(name);
    }
  });

  doc.querySelectorAll('style').forEach((el) => {
    el.textContent = rewriteCss(el.textContent ?? '');
  });

  const csp = doc.createElement('meta');
  csp.setAttribute('http-equiv', 'Content-Security-Policy');
  csp.setAttribute('content', contentSecurityPolicy(allow));
  doc.head.prepend(csp);

  return { html: `<!DOCTYPE html>${doc.documentElement.outerHTML}`, remoteImages };
}
