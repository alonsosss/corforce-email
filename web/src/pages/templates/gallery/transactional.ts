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

export const ORDER_NUMBER_VARIABLE = 'order_number';
export const ORDER_URL_VARIABLE = 'order_url';

export function transactionalTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    preheader: 'Hemos recibido tu pedido y ya lo estamos preparando.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          heading(brand, 'Confirmación de tu pedido') +
            paragraph(brand, greeting('Hola')) +
            paragraph(
              brand,
              `Hemos recibido tu pedido <strong>${variableToken(ORDER_NUMBER_VARIABLE)}</strong> y ya ` +
                'lo estamos preparando. Te enviaremos otro correo en cuanto salga hacia su destino, ' +
                'con los datos para seguir el envío.',
            ) +
            cta(brand, 'Ver el detalle del pedido', variableToken(ORDER_URL_VARIABLE)) +
            paragraph(
              brand,
              'Guarda este correo como comprobante. Si algún dato no es correcto, responde a este ' +
                'mensaje indicando el número de pedido y lo revisaremos.',
              `font-size="14px" color="${brand.muted}"`,
            ),
        ),
      ),
    ],
  });
}
