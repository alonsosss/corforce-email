// Prepara HTML que no es de la aplicacion (plantillas, pies de pagina, avisos, correo
// recibido) antes de darselo a un iframe aislado. El iframe hereda la CSP del gateway, que
// permite img-src https:, asi que una imagen remota se cargaria al abrir la vista previa:
// un pixel de seguimiento sabria quien mira y desde que IP. Por defecto se quitan todas las
// referencias remotas; las imagenes se muestran solo si quien mira lo pide.
//
// Dos capas: se reescriben las referencias en el documento y ademas se le pone una CSP
// propia (meta http-equiv) que no deja cargar nada remoto salvo las imagenes permitidas. El
// webmail va mas lejos: el servicio reescribe las imagenes remotas a su proxy firmado del
// mismo origen y aqui solo se admite ese proxy (remoteImageProxy).

export interface PreparedHtml {
  html: string;
  /** Referencias a imagenes remotas encontradas (bloqueadas o mostradas). */
  remoteImages: number;
}

export interface PrepareOptions {
  allowRemoteImages: boolean;
  /**
   * Imagenes propias del documento ya resueltas: URL tal como aparece en el HTML -> data:
   * de mapa de bits (las cid: de un correo). Se sustituyen y no cuentan como remotas.
   */
  inlineImages?: ReadonlyMap<string, string>;
  /**
   * Los enlaces se abren en una pestana nueva (base target) y no dentro del marco. Solo
   * tiene efecto si el iframe admite ventanas emergentes.
   */
  openLinksInNewTab?: boolean;
  /**
   * URL absoluta del proxy de imagenes del mismo origen (el webmail). Con ella, mostrar las
   * imagenes remotas solo deja las que ya pasan por ese proxy (absolutas o relativas a la raiz,
   * que se resuelven contra su origen porque un srcdoc no tiene base) y la CSP solo admite ese
   * origen: una URL remota directa que se escapara al servicio se quita igual.
   */
  remoteImageProxy?: string;
}

interface ImageProxy {
  origin: string;
  pathname: string;
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
// Las unicas imagenes incrustadas que se aceptan como sustitucion: mapas de bits.
const INLINE_BITMAP = /^data:image\/(png|jpeg|gif|webp);base64,/i;

function isInline(url: string): boolean {
  return /^data:/i.test(url.trim());
}

function isWebImage(url: string): boolean {
  return /^https?:\/\//i.test(url.trim());
}

/** srcset: lista de "url descriptor" separados por comas. */
function srcsetCandidates(value: string): [string, string][] {
  return value
    .split(',')
    .map((candidate): [string, string] => {
      const [url = '', ...descriptor] = candidate.trim().split(/\s+/);
      return [url, descriptor.join(' ')];
    })
    .filter(([url]) => url !== '');
}

function parseProxy(value: string | undefined): ImageProxy | null {
  if (!value) return null;
  const url = new URL(value);
  return { origin: url.origin, pathname: url.pathname };
}

/** La URL absoluta del proxy si la referencia apunta exactamente a el; null en otro caso. */
function proxiedImage(value: string, proxy: ImageProxy): string | null {
  const raw = value.trim();
  const candidate = raw.startsWith('/') && !raw.startsWith('//') ? `${proxy.origin}${raw}` : raw;
  if (!candidate.startsWith(`${proxy.origin}/`)) return null;
  try {
    const url = new URL(candidate);
    return url.origin === proxy.origin && url.pathname === proxy.pathname ? url.href : null;
  } catch {
    return null;
  }
}

function contentSecurityPolicy(allowRemoteImages: boolean, proxy: ImageProxy | null): string {
  const remote = proxy ? proxy.origin : 'https: http:';
  const images = allowRemoteImages ? `img-src data: ${remote}` : 'img-src data:';
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
  const proxy = parseProxy(options.remoteImageProxy);
  let remoteImages = 0;

  // La URL con la que se conserva una imagen, o null si se quita; cuenta las remotas.
  const resolveImage = (url: string): string | null => {
    if (isInline(url)) return url;
    remoteImages += 1;
    if (!allow) return null;
    if (proxy) return proxiedImage(url, proxy);
    return isWebImage(url) ? url : null;
  };

  const inlined = (url: string): string | undefined => {
    const value = options.inlineImages?.get(url.trim());
    return value !== undefined && INLINE_BITMAP.test(value) ? value : undefined;
  };

  const rewriteCss = (css: string): string =>
    css.replace(CSS_IMPORT, '').replace(CSS_URL, (whole, _q: string, url: string) => {
      if (url.startsWith('#')) return whole;
      const kept = resolveImage(url);
      if (kept === null) return 'none';
      return kept === url ? whole : `url("${kept}")`;
    });

  doc.querySelectorAll(ALWAYS_REMOVED.join(',')).forEach((el) => el.remove());

  doc.querySelectorAll('*').forEach((el) => {
    for (const name of IMAGE_ATTRIBUTES) {
      const value = el.getAttribute(name);
      if (value === null) continue;
      const replacement = inlined(value) ?? resolveImage(value);
      if (replacement === null) el.removeAttribute(name);
      else if (replacement !== value) el.setAttribute(name, replacement);
    }
    const srcset = el.getAttribute('srcset');
    if (srcset !== null) {
      // map antes que every: cada candidato cuenta aunque el primero ya se bloquee.
      const candidates = srcsetCandidates(srcset);
      const kept = candidates.map(([url]) => resolveImage(url));
      if (kept.every((url): url is string => url !== null)) {
        el.setAttribute(
          'srcset',
          candidates
            .map(([, descriptor], i) => [kept[i], descriptor].filter(Boolean).join(' '))
            .join(', '),
        );
      } else {
        el.removeAttribute('srcset');
      }
    }
    const style = el.getAttribute('style');
    if (style !== null) el.setAttribute('style', rewriteCss(style));
  });

  doc.querySelectorAll(SVG_IMAGE_SELECTOR).forEach((el) => {
    for (const name of ['href', 'xlink:href']) {
      const value = el.getAttribute(name);
      if (value === null || value.startsWith('#')) continue;
      const kept = resolveImage(value);
      if (kept === null) el.removeAttribute(name);
      else if (kept !== value) el.setAttribute(name, kept);
    }
  });

  doc.querySelectorAll('style').forEach((el) => {
    el.textContent = rewriteCss(el.textContent ?? '');
  });

  // Base sin href: solo fija el destino de los enlaces, no carga nada (base-uri 'none'
  // prohibe cualquier href).
  if (options.openLinksInNewTab) {
    const base = doc.createElement('base');
    base.setAttribute('target', '_blank');
    doc.head.prepend(base);
  }
  const referrer = doc.createElement('meta');
  referrer.setAttribute('name', 'referrer');
  referrer.setAttribute('content', 'no-referrer');
  doc.head.prepend(referrer);
  const csp = doc.createElement('meta');
  csp.setAttribute('http-equiv', 'Content-Security-Policy');
  csp.setAttribute('content', contentSecurityPolicy(allow, proxy));
  doc.head.prepend(csp);

  return { html: `<!DOCTYPE html>${doc.documentElement.outerHTML}`, remoteImages };
}
