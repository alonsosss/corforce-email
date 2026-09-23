import type { MessageKey } from '@/i18n';
import { safeEmail, safeHttpUrl, type BrandTokens } from './brand';
import { escapeMjml } from './mjmlSource';

// Bloques del editor (docs/Plan_Editor_Correos.md, seccion 6) como MJML. Son funciones
// puras del kit de marca: la galeria y las pruebas los usan sin cargar el lienzo.

/**
 * Variables reservadas de services/templates/internal/domain/variables.go que el pie y la
 * baja necesitan; las inyecta quien envia (campaigns, transactional).
 */
export const UNSUBSCRIBE_VARIABLE = 'unsubscribe_url';
export const VIEW_IN_BROWSER_VARIABLE = 'view_in_browser_url';

export function variableToken(name: string): string {
  return `{{.${name}}}`;
}

export type BlockId =
  | 'section'
  | 'columns-1'
  | 'columns-2'
  | 'columns-3'
  | 'columns-4'
  | 'heading'
  | 'text'
  | 'image'
  | 'button'
  | 'divider'
  | 'spacer'
  | 'social'
  | 'video'
  | 'discount'
  | 'legal-footer';

export type BlockCategory = 'layout' | 'content' | 'brand';

export interface BlockDefinition {
  id: BlockId;
  label: MessageKey;
  category: BlockCategory;
  /** Abre el gestor de imagenes al soltarlo (imagen y video). */
  activate?: boolean;
  /** Bloques de contenido: van dentro de una columna. Los de estructura, en el cuerpo. */
  placement: 'body' | 'column';
  content: (brand: BrandTokens) => string;
}

/**
 * Lienzo gris en SVG para una imagen aun sin elegir: no depende de ningun servidor de
 * terceros y el verificador lo cuenta como imagen sin texto alternativo util si se queda.
 */
export const IMAGE_PLACEHOLDER =
  'data:image/svg+xml;charset=utf-8,' +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" width="600" height="300" viewBox="0 0 600 300">' +
      '<rect width="600" height="300" fill="#E5E7EB"/>' +
      '<path d="M250 190l40-50 30 36 20-24 40 38z" fill="#9CA3AF"/></svg>',
  );

/**
 * Atributos de una etiqueta MJML: los de `attrs` (texto `nombre="valor"`) sustituyen a los
 * valores por defecto del mismo nombre. MJML se queda con el primero si se repite.
 */
function withDefaults(defaults: Record<string, string>, attrs: string): string {
  const given = new Set([...attrs.matchAll(/(?:^|\s)([a-z-]+)="/g)].map((m) => m[1]));
  const base = Object.entries(defaults)
    .filter(([name]) => !given.has(name))
    .map(([name, value]) => `${name}="${escapeMjml(value)}"`);
  return [...base, attrs.trim()].filter(Boolean).join(' ');
}

/** Como withDefaults, con los valores por defecto escritos tambien como `nombre="valor"`. */
export function overrideAttrs(defaults: string, attrs: string): string {
  const parsed = Object.fromEntries(
    [...defaults.matchAll(/([a-z-]+)="([^"]*)"/g)].map((m) => [m[1] ?? '', m[2] ?? '']),
  );
  return withDefaults(parsed, attrs);
}

export function mjText(brand: BrandTokens, body: string, attrs = ''): string {
  const all = withDefaults(
    {
      'font-family': brand.fontFamily,
      'font-size': '15px',
      'line-height': '1.6',
      color: brand.text,
    },
    attrs,
  );
  return `<mj-text ${all}>${body}</mj-text>`;
}

export function mjColumn(inner: string): string {
  return `<mj-column>${inner}</mj-column>`;
}

/** Seccion con fondo y margen por defecto; `attrs` puede sustituir cualquiera de los dos. */
export function mjSection(brand: BrandTokens, inner: string, attrs = ''): string {
  const all = withDefaults({ 'background-color': brand.surface, padding: '20px 0' }, attrs);
  return `<mj-section ${all}>${inner}</mj-section>`;
}

function columns(brand: BrandTokens, count: number): string {
  const cells = Array.from({ length: count }, (_, i) =>
    mjColumn(mjText(brand, `Columna ${i + 1}. Escribe aquí el contenido.`)),
  );
  return mjSection(brand, cells.join(''));
}

export function mjButton(brand: BrandTokens, label: string, href: string): string {
  const all = withDefaults(
    {
      href,
      'background-color': brand.primary,
      color: '#FFFFFF',
      'font-family': brand.fontFamily,
      'font-size': '15px',
      'font-weight': '600',
      'border-radius': '6px',
      'inner-padding': '12px 24px',
    },
    '',
  );
  return `<mj-button ${all}>${escapeMjml(label)}</mj-button>`;
}

/** Enlace por defecto de un boton: la web del kit o, sin ella, un ancla que editar. */
export function defaultHref(brand: BrandTokens): string {
  return safeHttpUrl(brand.footer.website) ?? '#';
}

/**
 * Pie legal: datos de la empresa del kit, contacto y el enlace de baja con la variable
 * reservada. Sin direccion en el kit el pie sale igual y el verificador lo senala
 * (missing_physical_address) en vez de rellenarla con un dato falso.
 */
export function legalFooter(brand: BrandTokens): string {
  const { footer } = brand;
  const lines: string[] = [];
  const company = footer.company.trim();
  const address = footer.address.trim();
  if (company || address) {
    lines.push(escapeMjml([company, address].filter(Boolean).join(' · ')));
  }
  const contact: string[] = [];
  const website = safeHttpUrl(footer.website);
  if (website) {
    const label = new URL(website).host;
    contact.push(`<a href="${escapeMjml(website)}" style="color:${brand.muted};">${label}</a>`);
  }
  const email = safeEmail(footer.support_email);
  if (email) {
    contact.push(
      `<a href="mailto:${escapeMjml(email)}" style="color:${brand.muted};">${escapeMjml(email)}</a>`,
    );
  }
  if (contact.length) lines.push(contact.join(' · '));
  lines.push(
    'Recibes este correo porque te registraste para recibir nuestras comunicaciones. ' +
      `<a href="${variableToken(UNSUBSCRIBE_VARIABLE)}" style="color:${brand.muted};">Darte de baja</a>` +
      ` · <a href="${variableToken(VIEW_IN_BROWSER_VARIABLE)}" style="color:${brand.muted};">Ver en el navegador</a>`,
  );
  return mjSection(
    brand,
    mjColumn(
      mjText(
        brand,
        lines.join('<br/>'),
        `font-size="12px" color="${brand.muted}" align="center" padding="10px 25px"`,
      ),
    ),
    `background-color="${brand.background}"`,
  );
}

/** Codigo de descuento destacado; el codigo real lo escribe quien disena la campana. */
export function discountCode(brand: BrandTokens): string {
  return (
    mjText(
      brand,
      'Tu código de descuento',
      `align="center" font-size="14px" color="${brand.muted}"`,
    ) +
    mjText(
      brand,
      `<span style="display:inline-block;border:2px dashed ${brand.primary};padding:10px 24px;letter-spacing:3px;">TU-CODIGO</span>`,
      `align="center" font-size="26px" font-weight="700" color="${brand.primary}"`,
    )
  );
}

export function logoImage(brand: BrandTokens, url: string): string {
  const alt = brand.footer.company.trim() || 'Logo';
  return `<mj-image src="${escapeMjml(url)}" alt="${escapeMjml(alt)}" width="160px" align="center" padding="10px 25px" />`;
}

export const BLOCKS: readonly BlockDefinition[] = [
  {
    id: 'section',
    label: 'templates.editor.block.section',
    category: 'layout',
    placement: 'body',
    content: (b) => mjSection(b, mjColumn(mjText(b, 'Escribe aquí el contenido de la sección.'))),
  },
  {
    id: 'columns-1',
    label: 'templates.editor.block.columns1',
    category: 'layout',
    placement: 'body',
    content: (b) => columns(b, 1),
  },
  {
    id: 'columns-2',
    label: 'templates.editor.block.columns2',
    category: 'layout',
    placement: 'body',
    content: (b) => columns(b, 2),
  },
  {
    id: 'columns-3',
    label: 'templates.editor.block.columns3',
    category: 'layout',
    placement: 'body',
    content: (b) => columns(b, 3),
  },
  {
    id: 'columns-4',
    label: 'templates.editor.block.columns4',
    category: 'layout',
    placement: 'body',
    content: (b) => columns(b, 4),
  },
  {
    id: 'heading',
    label: 'templates.editor.block.heading',
    category: 'content',
    placement: 'column',
    content: (b) =>
      mjText(b, 'Título de la sección', 'font-size="24px" font-weight="700" line-height="1.3"'),
  },
  {
    id: 'text',
    label: 'templates.editor.block.text',
    category: 'content',
    placement: 'column',
    content: (b) =>
      mjText(
        b,
        'Escribe aquí el texto del mensaje. Puedes insertar variables del contacto desde el panel de variables.',
      ),
  },
  {
    id: 'image',
    label: 'templates.editor.block.image',
    category: 'content',
    placement: 'column',
    activate: true,
    content: () => `<mj-image src="${IMAGE_PLACEHOLDER}" alt="" padding="10px 25px" />`,
  },
  {
    id: 'button',
    label: 'templates.editor.block.button',
    category: 'content',
    placement: 'column',
    content: (b) => mjButton(b, 'Ver más', defaultHref(b)),
  },
  {
    id: 'divider',
    label: 'templates.editor.block.divider',
    category: 'content',
    placement: 'column',
    content: (b) =>
      `<mj-divider border-width="1px" border-color="${b.border}" padding="10px 25px" />`,
  },
  {
    id: 'spacer',
    label: 'templates.editor.block.spacer',
    category: 'content',
    placement: 'column',
    content: () => '<mj-spacer height="24px" />',
  },
  {
    // Enlaces de texto: los iconos por defecto de mj-social se sirven desde un tercero,
    // que veria cada apertura. Con iconos propios se cambian por imagenes del gestor.
    id: 'social',
    label: 'templates.editor.block.social',
    category: 'content',
    placement: 'column',
    content: (b) =>
      `<mj-navbar align="center">` +
      ['Facebook', 'Instagram', 'LinkedIn', 'YouTube']
        .map(
          (name) =>
            `<mj-navbar-link href="#" color="${b.primary}" font-family="${escapeMjml(b.fontFamily)}" ` +
            `font-size="13px" padding="6px 10px">${name}</mj-navbar-link>`,
        )
        .join('') +
      `</mj-navbar>`,
  },
  {
    id: 'video',
    label: 'templates.editor.block.video',
    category: 'content',
    placement: 'column',
    activate: true,
    content: (b) =>
      `<mj-image src="${IMAGE_PLACEHOLDER}" alt="Ver el video" href="#" padding="10px 25px" />` +
      mjText(
        b,
        'Pulsa la imagen para ver el video.',
        `font-size="13px" color="${b.muted}" align="center"`,
      ),
  },
  {
    id: 'discount',
    label: 'templates.editor.block.discount',
    category: 'content',
    placement: 'column',
    content: (b) => discountCode(b) + mjButton(b, 'Usar mi código', defaultHref(b)),
  },
  {
    id: 'legal-footer',
    label: 'templates.editor.block.legalFooter',
    category: 'brand',
    placement: 'body',
    content: legalFooter,
  },
];
