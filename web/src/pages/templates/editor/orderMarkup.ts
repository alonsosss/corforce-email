import type { TemplateVariable } from '@/api/templates';
import type { BrandTokens } from './brand';
import { escapeMjml } from './mjmlSource';

// Marcado de los bloques de pedido (docs/Plan_Plantillas_de_Pedido.md): estado, productos y
// resumen. Es el HTML que va dentro de un <mj-text>; blocks.ts y la galeria lo envuelven.
//
// Tres reglas que no se ven en el resultado y lo sostienen:
// * Ninguna accion de plantilla ({{range}}, {{if}}) queda entre <table>, <tr> y <td>: el
//   lienzo del editor lee el documento con el parser del navegador, que saca el texto suelto
//   de una tabla. Cada fila opcional es su propia tabla y cada condicion va dentro de una celda.
// * Los importes solo se formatean (money); nunca se calculan aqui. Son del sistema de origen.
// * La lista se recorre con un tope (take) y el resto se enlaza al pedido: Gmail recorta el
//   correo a partir de 102 KB.

/** Productos que el correo muestra como mucho; el resto se enlaza al pedido completo. */
export const ORDER_ITEMS_LIMIT = 20;

/** Estados del pedido que entiende la linea de estado, en orden. */
export const ORDER_STATUSES = ['received', 'preparing', 'shipped', 'delivered'] as const;
export type OrderStatus = (typeof ORDER_STATUSES)[number];

const STATUS_LABELS: Record<OrderStatus, string> = {
  received: 'Pedido recibido',
  preparing: 'En preparación',
  shipped: 'En camino',
  delivered: 'Entregado',
};

export const ORDER_URL_VARIABLE = 'order_url';

export const ORDER_ITEMS_VARIABLE: TemplateVariable = {
  name: 'items',
  type: 'list',
  required: true,
  fields: [
    { name: 'name', type: 'string', required: true },
    { name: 'detail', type: 'string', required: false },
    { name: 'quantity', type: 'number', required: true },
    { name: 'price', type: 'number', required: true },
    { name: 'image_url', type: 'image', required: false },
    { name: 'delivery', type: 'string', required: false },
  ],
};

const CURRENCY: TemplateVariable = { name: 'currency', type: 'string', required: true };
const ORDER_URL: TemplateVariable = { name: ORDER_URL_VARIABLE, type: 'url', required: true };

/** Variables de cada bloque: el editor las declara al soltarlo. */
export function orderStatusVariables(initial: OrderStatus): TemplateVariable[] {
  return [{ name: 'status', type: 'string', required: false, default: initial }];
}

export const ORDER_ITEMS_VARIABLES: TemplateVariable[] = [
  ORDER_ITEMS_VARIABLE,
  CURRENCY,
  ORDER_URL,
];

export const ORDER_SUMMARY_VARIABLES: TemplateVariable[] = [
  CURRENCY,
  { name: 'subtotal', type: 'number', required: false },
  { name: 'shipping', type: 'number', required: false },
  { name: 'discount', type: 'number', required: false },
  { name: 'total', type: 'number', required: true },
];

function reached(status: OrderStatus): string {
  const from = ORDER_STATUSES.indexOf(status);
  const states = ORDER_STATUSES.slice(from).map((s) => `(eq .status "${s}")`);
  return states.length === 1 ? `eq .status "${status}"` : `or ${states.join(' ')}`;
}

function tableOpen(brand: BrandTokens, style = ''): string {
  return (
    '<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" ' +
    `style="border-collapse:collapse;font-family:${escapeMjml(brand.fontFamily)};${style}">`
  );
}

/**
 * Linea de estado: una barra por paso, llena hasta el estado actual, con su nombre debajo.
 * El primer paso siempre esta cumplido: el correo existe porque el pedido se recibio.
 */
export function orderStatusHtml(brand: BrandTokens): string {
  const bar = (color: string) =>
    `<div style="height:6px;line-height:6px;font-size:0;border-radius:3px;background-color:${color};">&nbsp;</div>`;
  const label = (text: string, on: boolean) =>
    on
      ? `<span style="color:${brand.primary};font-weight:700;">${text}</span>`
      : `<span style="color:${brand.muted};">${text}</span>`;
  const cell = (content: string) =>
    `<td width="25%" valign="top" style="width:25%;padding:0 3px;">${content}</td>`;
  const bars = ORDER_STATUSES.map((s, i) =>
    cell(
      i === 0
        ? bar(brand.primary)
        : `{{if ${reached(s)}}}${bar(brand.primary)}{{else}}${bar(brand.border)}{{end}}`,
    ),
  );
  const labels = ORDER_STATUSES.map((s, i) =>
    cell(
      `<div style="padding-top:8px;font-size:12px;line-height:1.35;text-align:center;">` +
        (i === 0
          ? label(STATUS_LABELS[s], true)
          : `{{if ${reached(s)}}}${label(STATUS_LABELS[s], true)}{{else}}${label(STATUS_LABELS[s], false)}{{end}}`) +
        '</div>',
    ),
  );
  return (
    tableOpen(brand, 'table-layout:fixed;') +
    `<tr>${bars.join('')}</tr><tr>${labels.join('')}</tr></table>`
  );
}

/**
 * Productos: imagen, nombre, detalle, cantidad, fecha de entrega y el importe de la linea. El
 * importe es el de la linea (cantidad incluida), tal como lo cobra el sistema de origen.
 */
export function orderItemsHtml(brand: BrandTokens): string {
  const small = `font-size:13px;line-height:1.45;color:${brand.muted};`;
  const item =
    tableOpen(brand, `border-bottom:1px solid ${brand.border};`) +
    '<tr>' +
    '<td width="72" valign="top" style="width:72px;padding:16px 14px 16px 0;">' +
    '{{if .image_url}}<img src="{{.image_url}}" alt="{{.name}}" width="72" ' +
    `style="display:block;width:72px;max-width:72px;height:auto;border:1px solid ${brand.border};border-radius:8px;">{{end}}` +
    '</td>' +
    '<td valign="top" style="padding:16px 0;">' +
    `<div style="font-size:15px;line-height:1.4;font-weight:600;color:${brand.text};">{{.name}}</div>` +
    `{{if .detail}}<div style="${small}">{{.detail}}</div>{{end}}` +
    `<div style="${small}padding-top:2px;">Cantidad: {{.quantity}}</div>` +
    `{{if .delivery}}<div style="font-size:13px;line-height:1.45;font-weight:600;color:${brand.primary};padding-top:6px;">{{.delivery}}</div>{{end}}` +
    '</td>' +
    `<td valign="top" align="right" style="padding:16px 0 16px 14px;white-space:nowrap;font-size:15px;line-height:1.4;font-weight:600;color:${brand.text};">` +
    '{{$.currency}} {{money .price}}</td>' +
    '</tr></table>';
  const more =
    `{{if rest ${ORDER_ITEMS_LIMIT} .items}}<div style="${small}padding-top:12px;">` +
    `Y {{rest ${ORDER_ITEMS_LIMIT} .items}} productos más en tu pedido. ` +
    `<a href="{{.${ORDER_URL_VARIABLE}}}" style="color:${brand.primary};font-weight:600;">Ver el pedido completo</a>` +
    '</div>{{end}}';
  return `{{range take ${ORDER_ITEMS_LIMIT} .items}}${item}{{end}}${more}`;
}

/** Resumen: subtotal, envio y descuento solo si traen importe, y el total destacado. */
export function orderSummaryHtml(brand: BrandTokens): string {
  const row = (label: string, amount: string, style = '') =>
    tableOpen(brand) +
    '<tr>' +
    `<td style="padding:4px 0;font-size:14px;line-height:1.5;color:${brand.muted};${style}">${label}</td>` +
    `<td align="right" style="padding:4px 0;font-size:14px;line-height:1.5;color:${brand.text};white-space:nowrap;${style}">${amount}</td>` +
    '</tr></table>';
  const optional = (name: string, label: string, sign = '') =>
    `{{if nonzero .${name}}}${row(label, `${sign}{{.currency}} {{money .${name}}}`)}{{end}}`;
  return (
    optional('subtotal', 'Subtotal') +
    optional('shipping', 'Envío') +
    optional('discount', 'Descuento', '-') +
    `<div style="height:8px;line-height:8px;font-size:0;border-bottom:1px solid ${brand.border};">&nbsp;</div>` +
    tableOpen(brand) +
    '<tr>' +
    `<td style="padding:12px 0 0;font-size:16px;line-height:1.4;font-weight:700;color:${brand.text};">Total</td>` +
    `<td align="right" style="padding:12px 0 0;font-size:20px;line-height:1.4;font-weight:700;color:${brand.text};white-space:nowrap;">{{.currency}} {{money .total}}</td>` +
    '</tr></table>'
  );
}
