import type { BrandTokens } from '../editor/brand';
import {
  defaultHref,
  legalFooter,
  logoImage,
  mjButton,
  mjColumn,
  mjSection,
  mjText,
  overrideAttrs,
  variableToken,
} from '../editor/blocks';
import { escapeMjml } from '../editor/mjmlSource';

// Piezas comunes de las plantillas de la galeria. Todas salen de los mismos bloques que el
// editor ofrece, para que una plantilla de la galeria se pueda seguir editando igual.

/** Nombre de la empresa que inyecta quien envia (variable reservada tenant_name). */
export const TENANT_NAME_VARIABLE = 'tenant_name';

export interface DocumentParts {
  preheader: string;
  sections: string[];
}

export function emailDocument(brand: BrandTokens, parts: DocumentParts): string {
  return (
    `<mjml><mj-head><mj-preview>${escapeMjml(parts.preheader)}</mj-preview></mj-head>` +
    `<mj-body background-color="${brand.background}" width="600px">` +
    parts.sections.join('') +
    legalFooter(brand) +
    `</mj-body></mjml>`
  );
}

/** Cabecera con el logo del kit o, sin logo, el nombre de la empresa como texto. */
export function header(brand: BrandTokens): string {
  const company = brand.footer.company.trim();
  const inner = brand.logoUrl
    ? logoImage(brand, brand.logoUrl)
    : mjText(
        brand,
        company ? escapeMjml(company) : variableToken(TENANT_NAME_VARIABLE),
        `align="center" font-size="20px" font-weight="700" color="${brand.primary}"`,
      );
  return mjSection(brand, mjColumn(inner), 'padding="24px 0 8px"');
}

export function heading(brand: BrandTokens, body: string, attrs = ''): string {
  return mjText(
    brand,
    body,
    overrideAttrs('font-size="26px" font-weight="700" line-height="1.3"', attrs),
  );
}

export function paragraph(brand: BrandTokens, body: string, attrs = ''): string {
  return mjText(brand, body, attrs);
}

export function cta(brand: BrandTokens, label: string, href: string): string {
  return mjButton(brand, label, href);
}

/** Saludo que no deja un "Hola ," si el contacto no tiene nombre. */
export function greeting(prefix: string): string {
  return `${prefix}{{if .first_name}} {{.first_name}}{{end}},`;
}

export { defaultHref, mjColumn, mjSection };
