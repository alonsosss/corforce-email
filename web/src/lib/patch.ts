// Cuerpos de PATCH: solo viaja lo que cambio. Un campo ausente deja el valor como esta en
// el servidor, y un PATCH sin campos se evita antes de enviarlo (los servicios responden
// 422 "nada que actualizar").

/** El valor nuevo si difiere del actual; si no, undefined para que no viaje. */
export function changed<T>(next: T, current: T): T | undefined {
  return next === current ? undefined : next;
}

/** Cierto si ningun campo del cuerpo lleva valor. */
export function isEmptyPatch(body: object): boolean {
  return Object.values(body).every((value) => value === undefined);
}
