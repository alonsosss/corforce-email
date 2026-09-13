import { es } from './es';

export type MessageKey = keyof typeof es;
export type Messages = Record<MessageKey, string>;
export type Locale = 'es';

// Otro idioma se anade aqui con el mismo conjunto de claves; el tipo obliga a que no falte
// ninguna.
const catalogs: Record<Locale, Messages> = { es };

let current: Locale = 'es';

export function setLocale(locale: Locale): void {
  current = locale;
}

export function getLocale(): Locale {
  return current;
}

type Vars = Record<string, string | number>;

export function t(key: MessageKey, vars?: Vars): string {
  const text = catalogs[current][key];
  if (!vars) return text;
  return text.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = vars[name];
    return value === undefined ? match : String(value);
  });
}

/** Cierto si la clave existe: para textos derivados de un valor del backend. */
export function hasMessage(key: string): key is MessageKey {
  return Object.prototype.hasOwnProperty.call(catalogs[current], key);
}

/** Traduce `prefix.value` si existe; si no, devuelve el valor tal cual. */
export function tEnum(prefix: string, value: string): string {
  const key = `${prefix}.${value}`;
  return hasMessage(key) ? t(key) : value;
}
