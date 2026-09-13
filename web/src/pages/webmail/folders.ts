import { FOLDER_ROLES, type WebmailFolder } from '@/api/webmail';
import { hasMessage, t } from '@/i18n';

export interface FolderItem {
  folder: WebmailFolder;
  label: string;
  /** Nivel de anidamiento bajo otra carpeta de la lista. */
  depth: number;
}

function segments(folder: WebmailFolder): string[] {
  return folder.delimiter ? folder.name.split(folder.delimiter).filter(Boolean) : [folder.name];
}

/** Nombre visible: el del papel especial si lo tiene, si no el ultimo tramo de la ruta. */
export function folderLabel(folder: WebmailFolder): string {
  const key = `webmail.role.${folder.role}`;
  if (folder.role && hasMessage(key)) return t(key);
  const parts = segments(folder);
  return parts[parts.length - 1] ?? folder.name;
}

/**
 * Orden del panel: la bandeja de entrada, despues las carpetas con papel especial en el
 * orden del servidor y al final las demas por nombre, cada subcarpeta bajo su padre.
 */
export function orderFolders(folders: readonly WebmailFolder[]): FolderItem[] {
  const isInbox = (f: WebmailFolder) => f.role === FOLDER_ROLES.inbox;
  const special = [...folders.filter((f) => f.role)].sort(
    (a, b) => Number(isInbox(b)) - Number(isInbox(a)),
  );
  const rest = folders.filter((f) => !f.role).sort((a, b) => a.name.localeCompare(b.name));
  const restNames = new Set(rest.map((f) => f.name));
  const depthOf = (folder: WebmailFolder) => {
    const parts = segments(folder);
    let depth = 0;
    for (let i = 1; i < parts.length; i += 1) {
      if (restNames.has(parts.slice(0, i).join(folder.delimiter))) depth += 1;
    }
    return depth;
  };
  return [
    ...special.map((folder) => ({ folder, label: folderLabel(folder), depth: 0 })),
    ...rest.map((folder) => ({ folder, label: folderLabel(folder), depth: depthOf(folder) })),
  ];
}

/** Primera carpeta seleccionable con ese papel. */
export function folderWithRole(
  folders: readonly WebmailFolder[],
  role: string,
): WebmailFolder | undefined {
  return folders.find((f) => f.role === role && f.selectable);
}

/** Carpeta que se abre sin eleccion: la bandeja de entrada o la primera seleccionable. */
export function defaultFolder(folders: readonly WebmailFolder[]): string | null {
  return (
    folderWithRole(folders, FOLDER_ROLES.inbox)?.name ??
    folders.find((f) => f.selectable)?.name ??
    null
  );
}

/** En Enviados y Borradores importa a quien va el mensaje, no quien lo envia. */
export function showsRecipients(role: string): boolean {
  return role === FOLDER_ROLES.sent || role === FOLDER_ROLES.drafts;
}
