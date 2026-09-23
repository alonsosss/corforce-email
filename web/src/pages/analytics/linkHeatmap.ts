import type { LinkReport } from '@/api/analytics';

// Mapa de calor de una campana sobre la vista previa de su plantilla. Los enlaces de la
// vista previa se casan con los de analytics normalizando los dos lados igual: la regla es
// la de services/analytics/internal/domain/link.go (NormalizeLink) sin los identificadores
// del destinatario, que la vista previa no tiene.

const HEAT_ATTRIBUTE = 'data-cf-heat';
const BADGE_CLASS = 'cf-heat-badge';

/** URL normalizada como la agrega analytics, o null si no es un enlace http(s). */
export function normalizeLink(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    return null;
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || !url.hostname) return null;
  const query = url.search
    .slice(1)
    .split(/[&;]/)
    .filter((pair) => pair !== '' && !queryKey(pair).startsWith('utm_'))
    .join('&');
  return `${url.protocol}//${url.host}${url.pathname}${query ? `?${query}` : ''}${url.hash}`;
}

function queryKey(pair: string): string {
  const key = pair.split('=', 1)[0] ?? '';
  try {
    return decodeURIComponent(key.replace(/\+/g, ' ')).toLowerCase();
  } catch {
    return key.toLowerCase();
  }
}

export interface HeatmapResult {
  html: string;
  /** URL normalizadas de analytics que aparecen en la vista previa. */
  matched: Set<string>;
  /** Enlaces http(s) de la vista previa. */
  previewLinks: number;
}

export interface HeatmapOptions {
  totalClicks: number;
  /** Texto de la etiqueta de cada enlace (el porcentaje ya formateado). */
  label: (share: number, clicks: number) => string;
}

/**
 * Marca cada enlace http(s) de la vista previa con su porcentaje de los clics de la
 * campana. Devuelve un documento nuevo; el original no se toca. El resultado se pinta en
 * HtmlPreviewFrame, que lo vuelve a sanear.
 */
export function heatmapHtml(
  source: string,
  links: readonly LinkReport[],
  options: HeatmapOptions,
): HeatmapResult {
  const doc = new DOMParser().parseFromString(source, 'text/html');
  const byUrl = new Map<string, LinkReport>();
  for (const link of links) {
    if (!link.other) byUrl.set(link.url, link);
  }
  const maxClicks = Math.max(1, ...links.filter((l) => !l.other).map((l) => l.clicks));
  const matched = new Set<string>();
  let previewLinks = 0;

  doc.querySelectorAll('a[href], area[href]').forEach((el) => {
    const normalized = normalizeLink(el.getAttribute('href') ?? '');
    if (normalized === null) return;
    previewLinks += 1;
    const stat = byUrl.get(normalized);
    const clicks = stat?.clicks ?? 0;
    if (stat) matched.add(normalized);
    const share = options.totalClicks > 0 ? clicks / options.totalClicks : 0;
    const intensity = clicks / maxClicks;
    el.setAttribute(HEAT_ATTRIBUTE, clicks > 0 ? 'hot' : 'cold');
    el.setAttribute('style', `${el.getAttribute('style') ?? ''};--cf-heat:${intensity.toFixed(3)}`);
    if (el.tagName.toLowerCase() === 'a') {
      const badge = doc.createElement('span');
      badge.className = BADGE_CLASS;
      badge.textContent = options.label(share, clicks);
      el.appendChild(badge);
    }
  });

  const style = doc.createElement('style');
  style.textContent = HEATMAP_CSS;
  doc.head.appendChild(style);
  return { html: `<!DOCTYPE html>${doc.documentElement.outerHTML}`, matched, previewLinks };
}

// El marco no hereda los estilos de la aplicacion: los colores de la superposicion viven
// aqui, dentro del documento que se previsualiza.
const HEATMAP_CSS = `
[${HEAT_ATTRIBUTE}] {
  outline: 2px solid rgba(217, 45, 32, calc(0.25 + 0.75 * var(--cf-heat, 0)));
  outline-offset: 2px;
  background-color: rgba(217, 45, 32, calc(0.18 * var(--cf-heat, 0)));
}
[${HEAT_ATTRIBUTE}="cold"] { outline-color: rgba(102, 112, 133, 0.45); }
.${BADGE_CLASS} {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 6px;
  border-radius: 9px;
  background: #b42318;
  color: #ffffff;
  font: 600 11px/1.5 system-ui, -apple-system, 'Segoe UI', sans-serif;
  text-decoration: none;
  vertical-align: middle;
  white-space: nowrap;
}
[${HEAT_ATTRIBUTE}="cold"] .${BADGE_CLASS} { background: #667085; }
`;
