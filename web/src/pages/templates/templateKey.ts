// Clave estable de una plantilla: el mismo formato que NormalizeTemplateKey en
// services/templates/internal/domain/entities.go (el servicio vuelve a validarla).

const KEY = /^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$/;

/** La clave sin espacios de borde, o null si no tiene el formato admitido. */
export function normalizeTemplateKey(raw: string, max: number): string | null {
  const key = raw.trim();
  return key.length <= max && KEY.test(key) ? key : null;
}
