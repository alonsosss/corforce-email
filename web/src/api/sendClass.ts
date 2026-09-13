// Clase de envio (domain.Classes() de analytics y reputation, mismos valores y orden).
// reputation la publica en GET /reputation/meta y sus pantallas la leen de alli. analytics
// no publica catalogo y su modulo de permisos es otro, asi que su filtro aun usa esta
// lista: se sustituira cuando analytics exponga el suyo.
export type SendClass = 'transactional' | 'marketing';

export const SEND_CLASSES: readonly SendClass[] = ['transactional', 'marketing'];
