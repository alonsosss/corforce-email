// Unico uso permitido de localStorage en la aplicacion: la preferencia visual. La sesion
// nunca pasa por aqui (docs/arquitectura/CSP-Y-SESION.md).

export type Theme = 'light' | 'dark';

const STORAGE_KEY = 'cf.theme';

function readStored(): Theme | null {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    return value === 'light' || value === 'dark' ? value : null;
  } catch {
    return null;
  }
}

function systemTheme(): Theme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function resolveTheme(): Theme {
  return readStored() ?? systemTheme();
}

export function applyTheme(theme: Theme): void {
  document.documentElement.setAttribute('data-theme', theme);
}

export function persistTheme(theme: Theme): void {
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // Sin almacenamiento disponible el tema vive solo en la pestana.
  }
}
