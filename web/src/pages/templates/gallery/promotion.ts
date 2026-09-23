import type { BrandTokens } from '../editor/brand';
import { discountCode, mjText } from '../editor/blocks';
import {
  cta,
  defaultHref,
  emailDocument,
  greeting,
  header,
  heading,
  mjColumn,
  mjSection,
  paragraph,
} from './layout';

export function promotionTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    preheader: 'Una oferta por tiempo limitado en nuestra selección de temporada.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          mjText(
            brand,
            'Oferta por tiempo limitado',
            'align="center" font-size="13px" font-weight="700" color="#FFFFFF" letter-spacing="2px"',
          ) +
            heading(
              brand,
              'Descuentos especiales en nuestra selección de temporada',
              'align="center" color="#FFFFFF" font-size="28px"',
            ),
        ),
        `background-color="${brand.primary}" padding="36px 0"`,
      ),
      mjSection(
        brand,
        mjColumn(
          paragraph(brand, greeting('Hola')) +
            paragraph(
              brand,
              'Durante los próximos días tienes acceso a condiciones especiales en una selección ' +
                'de productos y servicios. Usa tu código al finalizar la compra y el descuento se ' +
                'aplicará automáticamente sobre el total.',
            ) +
            discountCode(brand) +
            cta(brand, 'Aprovechar la oferta', defaultHref(brand)) +
            paragraph(
              brand,
              'Oferta válida hasta agotar existencias o hasta la fecha indicada en la tienda. No acumulable con otras promociones.',
              `font-size="12px" color="${brand.muted}" align="center"`,
            ),
        ),
      ),
    ],
  });
}
