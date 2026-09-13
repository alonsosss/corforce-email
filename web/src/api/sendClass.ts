// Clase de envio de analytics y reputation (domain.Classes() en ambos servicios, mismos
// valores y orden). Ninguno de los dos publica aun su catalogo: cuando exista, esta lista
// se sustituye por la del API.
export type SendClass = 'transactional' | 'marketing';

export const SEND_CLASSES: readonly SendClass[] = ['transactional', 'marketing'];
