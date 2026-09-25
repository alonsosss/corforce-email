import { createContext, useContext } from 'react';

export type ComposerSize = 'normal' | 'minimized' | 'expanded';

export interface ComposeWindowState {
  size: ComposerSize;
  setSize: (size: ComposerSize) => void;
}

export const ComposeWindowContext = createContext<ComposeWindowState | null>(null);

/** Estado de la ventana flotante; null cuando la redaccion se monta sola (pruebas, enlaces). */
export function useComposeWindow(): ComposeWindowState | null {
  return useContext(ComposeWindowContext);
}
