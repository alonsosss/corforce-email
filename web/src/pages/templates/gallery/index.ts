import type { TemplateKind, TemplateVariable } from '@/api/templates';
import type { MessageKey } from '@/i18n';
import type { BrandTokens } from '../editor/brand';
import { abandonedCartTemplate, CART_URL_VARIABLE } from './abandonedCart';
import { eventTemplate } from './event';
import { newsletterTemplate } from './newsletter';
import { promotionTemplate } from './promotion';
import { ORDER_NUMBER_VARIABLE, ORDER_URL_VARIABLE, transactionalTemplate } from './transactional';
import { welcomeTemplate } from './welcome';

// Plantillas predisenadas, versionadas con la aplicacion. Se generan con el kit de marca de
// la empresa (colores, tipografia, logo y pie legal) y declaran las variables que usan para
// que el editor las anada a la version al elegirlas.

export type GalleryId =
  'welcome' | 'promotion' | 'newsletter' | 'event' | 'abandoned-cart' | 'transactional';

export interface GalleryTemplate {
  id: GalleryId;
  name: MessageKey;
  description: MessageKey;
  /** Tipo de plantilla para el que esta pensada; se puede usar en el otro. */
  kind: TemplateKind;
  /** Asunto de partida, editable. */
  subject: string;
  variables: TemplateVariable[];
  build: (brand: BrandTokens) => string;
}

const FIRST_NAME: TemplateVariable = { name: 'first_name', type: 'string', required: false };

export const GALLERY: readonly GalleryTemplate[] = [
  {
    id: 'welcome',
    name: 'templates.gallery.welcome',
    description: 'templates.gallery.welcomeHint',
    kind: 'marketing',
    subject: 'Te damos la bienvenida',
    variables: [FIRST_NAME],
    build: welcomeTemplate,
  },
  {
    id: 'promotion',
    name: 'templates.gallery.promotion',
    description: 'templates.gallery.promotionHint',
    kind: 'marketing',
    subject: 'Una oferta especial para ti',
    variables: [FIRST_NAME],
    build: promotionTemplate,
  },
  {
    id: 'newsletter',
    name: 'templates.gallery.newsletter',
    description: 'templates.gallery.newsletterHint',
    kind: 'marketing',
    subject: 'Novedades del mes',
    variables: [],
    build: newsletterTemplate,
  },
  {
    id: 'event',
    name: 'templates.gallery.event',
    description: 'templates.gallery.eventHint',
    kind: 'marketing',
    subject: 'Te invitamos a nuestro próximo evento',
    variables: [FIRST_NAME],
    build: eventTemplate,
  },
  {
    id: 'abandoned-cart',
    name: 'templates.gallery.abandonedCart',
    description: 'templates.gallery.abandonedCartHint',
    kind: 'marketing',
    subject: 'Tu carrito te está esperando',
    variables: [FIRST_NAME, { name: CART_URL_VARIABLE, type: 'url', required: true }],
    build: abandonedCartTemplate,
  },
  {
    id: 'transactional',
    name: 'templates.gallery.transactional',
    description: 'templates.gallery.transactionalHint',
    kind: 'transactional',
    subject: 'Confirmación de tu pedido',
    variables: [
      FIRST_NAME,
      { name: ORDER_NUMBER_VARIABLE, type: 'string', required: true },
      { name: ORDER_URL_VARIABLE, type: 'url', required: true },
    ],
    build: transactionalTemplate,
  },
];
