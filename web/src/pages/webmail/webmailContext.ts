import { useOutletContext } from 'react-router-dom';
import type { WebmailFolder } from '@/api/webmail';
import type { QueryState } from '@/hooks/useQuery';

/** Lo que el marco del webmail comparte con sus pantallas. */
export interface WebmailOutlet {
  folders: QueryState<WebmailFolder[]>;
  /** Ajusta en local los no leidos de una carpeta tras leer o marcar un mensaje. */
  adjustUnread: (folder: string, delta: number) => void;
  reloadFolders: () => void;
}

export function useWebmailOutlet(): WebmailOutlet {
  return useOutletContext<WebmailOutlet>();
}
