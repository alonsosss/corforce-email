// Clase de envio (domain.Classes() de analytics y reputation, mismos valores). Es solo el
// tipo de los DTO: los valores que se ofrecen en un filtro salen del catalogo de cada
// servicio (GET /analytics/meta y GET /reputation/meta), nunca de una lista copiada.
export type SendClass = 'transactional' | 'marketing';
