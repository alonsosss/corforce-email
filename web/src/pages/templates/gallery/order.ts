import type { TemplateVariable } from '@/api/templates';
import type { BrandTokens } from '../editor/brand';
import { mjText, variableToken } from '../editor/blocks';
import {
  ORDER_ITEMS_VARIABLES,
  ORDER_SUMMARY_VARIABLES,
  ORDER_URL_VARIABLE,
  orderItemsHtml,
  orderStatusHtml,
  orderStatusVariables,
  orderSummaryHtml,
} from '../editor/orderMarkup';
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

// Correos de pedido con la lista de productos (docs/Plan_Plantillas_de_Pedido.md). Son
// transaccionales puros: sin baja ni contenido comercial, para que Gmail los trate como el
// correo que el cliente espera y no como publicidad.

const FIRST_NAME: TemplateVariable = { name: 'first_name', type: 'string', required: false };
const ORDER_NUMBER: TemplateVariable = { name: 'order_number', type: 'string', required: true };

/** Une listas de variables sin repetir nombres; gana la primera declaracion. */
function uniqueVariables(...groups: readonly TemplateVariable[][]): TemplateVariable[] {
  const seen = new Set<string>();
  const out: TemplateVariable[] = [];
  for (const v of groups.flat()) {
    if (seen.has(v.name)) continue;
    seen.add(v.name);
    out.push(v);
  }
  return out;
}

/** Etiqueta pequena en mayusculas sobre un titulo. */
function eyebrow(brand: BrandTokens, body: string): string {
  return mjText(
    brand,
    body,
    `font-size="12px" font-weight="700" letter-spacing="1px" color="${brand.primary}" padding="0 25px 4px"`,
  );
}

function sectionTitle(brand: BrandTokens, body: string): string {
  return mjText(brand, body, 'font-size="18px" font-weight="700" padding="0 25px 4px"');
}

function muted(brand: BrandTokens, body: string): string {
  return mjText(brand, body, `font-size="14px" color="${brand.muted}"`);
}

/** Bloque de datos: una etiqueta y su valor, solo si el valor llega. */
function detail(brand: BrandTokens, label: string, variable: string): string {
  return (
    `{{if .${variable}}}<div style="padding-bottom:10px;">` +
    `<div style="font-size:12px;line-height:1.4;color:${brand.muted};">${label}</div>` +
    `<div style="font-size:14px;line-height:1.5;color:${brand.text};">{{.${variable}}}</div>` +
    '</div>{{end}}'
  );
}

const CARD = 'padding="28px 0"';
const ACCENT = (brand: BrandTokens) => `border-top="4px solid ${brand.primary}" ${CARD}`;

export const ORDER_CONFIRMATION_VARIABLES: TemplateVariable[] = uniqueVariables(
  [FIRST_NAME, ORDER_NUMBER, { name: 'order_date', type: 'string', required: false }],
  orderStatusVariables('received'),
  ORDER_ITEMS_VARIABLES,
  ORDER_SUMMARY_VARIABLES,
  [
    { name: 'shipping_method', type: 'string', required: false },
    { name: 'shipping_address', type: 'string', required: false },
    { name: 'payment_method', type: 'string', required: false },
  ],
);

export function orderConfirmationTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    transactional: true,
    preheader:
      'Recibimos tu pedido y ya lo estamos preparando. Aquí tienes el detalle de tu compra.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          eyebrow(brand, `PEDIDO N.º ${variableToken('order_number')}`) +
            heading(brand, 'Gracias por tu compra', 'padding="0 25px 8px"') +
            paragraph(
              brand,
              `${greeting('Hola')} recibimos tu pedido y ya lo estamos preparando. Te avisaremos ` +
                'por correo en cada paso, hasta que llegue a tus manos.',
            ) +
            mjText(brand, orderStatusHtml(brand), 'padding="16px 22px 8px"') +
            cta(brand, 'Ver mi pedido', variableToken(ORDER_URL_VARIABLE)),
        ),
        ACCENT(brand),
      ),
      mjSection(
        brand,
        mjColumn(
          sectionTitle(brand, 'Tu compra') +
            muted(
              brand,
              `{{if .order_date}}Realizada el {{.order_date}}{{else}}Detalle de los productos{{end}}`,
            ) +
            mjText(brand, orderItemsHtml(brand), 'padding="0 25px"') +
            mjText(brand, orderSummaryHtml(brand), 'padding="16px 25px 0"'),
        ),
        CARD,
      ),
      mjSection(
        brand,
        mjColumn(
          sectionTitle(brand, 'Entrega y pago') +
            mjText(
              brand,
              detail(brand, 'Modalidad de entrega', 'shipping_method') +
                detail(brand, 'Dirección', 'shipping_address') +
                detail(brand, 'Medio de pago', 'payment_method'),
              'padding="8px 25px 0"',
            ) +
            muted(
              brand,
              '¿Algún dato no es correcto? Responde a este correo indicando el número de pedido y lo ' +
                'revisaremos contigo. Guarda este mensaje como comprobante de tu compra.',
            ),
        ),
        CARD,
      ),
    ],
  });
}

export const ORDER_SHIPPED_VARIABLES: TemplateVariable[] = uniqueVariables(
  [FIRST_NAME, ORDER_NUMBER],
  orderStatusVariables('shipped'),
  [
    { name: 'tracking_url', type: 'url', required: true },
    { name: 'carrier', type: 'string', required: false },
    { name: 'tracking_number', type: 'string', required: false },
    { name: 'estimated_delivery', type: 'string', required: false },
    { name: 'shipping_address', type: 'string', required: false },
  ],
  ORDER_ITEMS_VARIABLES,
);

export function orderShippedTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    transactional: true,
    preheader: 'Tu pedido ya salió y va en camino. Sigue el envío en cualquier momento.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          eyebrow(brand, `PEDIDO N.º ${variableToken('order_number')}`) +
            heading(brand, 'Tu pedido va en camino', 'padding="0 25px 8px"') +
            paragraph(
              brand,
              `${greeting('Hola')} tu pedido ya salió de nuestro almacén. Puedes seguir el envío ` +
                'en todo momento con el botón de abajo.',
            ) +
            mjText(brand, orderStatusHtml(brand), 'padding="16px 22px 8px"') +
            mjText(
              brand,
              detail(brand, 'Fecha estimada de entrega', 'estimated_delivery') +
                detail(brand, 'Transportista', 'carrier') +
                detail(brand, 'Número de seguimiento', 'tracking_number') +
                detail(brand, 'Dirección de entrega', 'shipping_address'),
              'padding="16px 25px 0"',
            ) +
            cta(brand, 'Seguir mi envío', variableToken('tracking_url')),
        ),
        ACCENT(brand),
      ),
      mjSection(
        brand,
        mjColumn(
          sectionTitle(brand, 'Lo que va en este envío') +
            mjText(brand, orderItemsHtml(brand), 'padding="0 25px"') +
            muted(
              brand,
              'Si no vas a estar en la dirección de entrega, responde a este correo y te ayudaremos ' +
                'a coordinar otra opción con el transportista.',
            ),
        ),
        CARD,
      ),
    ],
  });
}
