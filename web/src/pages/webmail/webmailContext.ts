import { createContext, useContext } from 'react';
import { useOutletContext } from 'react-router-dom';
import type { WebmailFolder } from '@/api/webmail';
import type { QueryState } from '@/hooks/useQuery';

/** Lo que el marco del webmail comparte con sus pantallas. */
export interface WebmailOutlet {
  folders: QueryState<WebmailFolder[]>;
  /** Ajusta en local los no leidos de una carpeta tras leer o marcar un mensaje. */
  adjustUnread: (folder: string, delta: number) => void;
  reloadFolders: () => void;
  /** Sube cuando llega un aviso de cambios en la bandeja de entrada: quien la muestra vuelve a leerla. */
  inboxTick: number;
}

/** El mismo valor para lo que el marco monta fuera del Outlet (la ventana de redaccion). */
export const WebmailShellContext = createContext<WebmailOutlet | null>(null);

export function useWebmailOutlet(): WebmailOutlet {
  const shell = useContext(WebmailShellContext);
  const outlet = useOutletContext<WebmailOutlet | undefined>();
  const value = shell ?? outlet;
  if (!value) throw new Error('useWebmailOutlet fuera del marco del webmail');
  return value;
}
