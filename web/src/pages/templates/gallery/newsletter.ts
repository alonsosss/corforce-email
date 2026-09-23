import type { BrandTokens } from '../editor/brand';
import {
  defaultHref,
  emailDocument,
  header,
  heading,
  mjColumn,
  mjSection,
  paragraph,
} from './layout';

function article(brand: BrandTokens, title: string, body: string): string {
  const href = defaultHref(brand);
  return mjSection(
    brand,
    mjColumn(
      heading(brand, title, 'font-size="19px" padding="10px 25px 4px"') +
        paragraph(brand, body, 'padding="0 25px 4px"') +
        paragraph(
          brand,
          `<a href="${href}" style="color:${brand.primary};font-weight:600;">Leer más</a>`,
          'padding="0 25px 10px"',
        ),
    ),
    `padding="12px 0" border-bottom="1px solid ${brand.border}"`,
  );
}

export function newsletterTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    preheader: 'Las novedades más importantes del mes, reunidas en un solo correo.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          heading(brand, 'Novedades del mes') +
            paragraph(
              brand,
              'Te traemos un resumen con lo más destacado de las últimas semanas: lanzamientos, ' +
                'artículos y recomendaciones que no te conviene perder.',
            ),
        ),
      ),
      article(
        brand,
        'Lo más destacado',
        'Un repaso a los cambios y mejoras más relevantes de este periodo y a cómo pueden ayudarte en tu día a día.',
      ),
      article(
        brand,
        'Guía práctica',
        'Consejos paso a paso para aprovechar mejor nuestras herramientas, con ejemplos que puedes aplicar hoy mismo.',
      ),
      article(
        brand,
        'Próximamente',
        'Un adelanto de lo que estamos preparando para las próximas semanas y de las fechas que conviene tener en cuenta.',
      ),
    ],
  });
}
