import type { BrandTokens } from '../editor/brand';
import { variableToken } from '../editor/blocks';
import {
  cta,
  emailDocument,
  greeting,
  header,
  heading,
  mjColumn,
  mjSection,
  paragraph,
} from './layout';

export const CART_URL_VARIABLE = 'cart_url';

export function abandonedCartTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    preheader: 'Guardamos tu carrito para que puedas terminar tu compra cuando quieras.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          heading(brand, 'Dejaste productos en tu carrito') +
            paragraph(brand, greeting('Hola')) +
            paragraph(
              brand,
              'Vimos que no terminaste tu compra. Hemos guardado los productos que elegiste para ' +
                'que puedas retomarla en el mismo punto, sin volver a buscarlos. La disponibilidad ' +
                'no está garantizada, así que te recomendamos completarla pronto.',
            ) +
            cta(brand, 'Volver a mi carrito', variableToken(CART_URL_VARIABLE)) +
            paragraph(
              brand,
              '¿Tuviste algún problema durante el pago? Responde a este correo y te ayudaremos a completarlo.',
              `font-size="14px" color="${brand.muted}"`,
            ),
        ),
      ),
    ],
  });
}
