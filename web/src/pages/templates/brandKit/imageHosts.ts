// Servidores de imagen permitidos del kit de marca: el mismo criterio que normalizeImageHost en
// services/templates/internal/domain/brandkit.go (el servicio vuelve a validarlos al guardar).

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

/** Nombre de host en minusculas, sin esquema, puerto ni ruta y con al menos un punto; null si no. */
export function normalizeImageHost(raw: string): string | null {
  const host = raw.trim().toLowerCase().replace(/\.$/, '');
  if (!host || host.length > 253 || !host.includes('.')) return null;
  return host.split('.').every((label) => LABEL.test(label)) ? host : null;
}
