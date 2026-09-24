import { FOLDER_ROLES, type WebmailFolder } from '@/api/webmail';
import { hasMessage, t } from '@/i18n';
import { utf8Length } from './format';

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

/** INBOX y las carpetas con papel no se renombran ni se borran (FOLDER_PROTECTED). */
export function isProtectedFolder(folder: WebmailFolder): boolean {
  return Boolean(folder.role) || folder.name.toUpperCase() === 'INBOX';
}

/** Solo Papelera y Spam se vacian. */
export function isEmptiable(role: string): boolean {
  return role === FOLDER_ROLES.trash || role === FOLDER_ROLES.junk;
}

/** Separador de jerarquia del servidor; sin ninguno, las carpetas son planas. */
export function folderDelimiter(folders: readonly WebmailFolder[]): string {
  return folders.find((f) => f.delimiter)?.delimiter ?? '';
}

/** Carpeta que contiene a esta, o '' si es de primer nivel. */
export function parentPath(folder: WebmailFolder): string {
  if (!folder.delimiter) return '';
  const at = folder.name.lastIndexOf(folder.delimiter);
  return at > 0 ? folder.name.slice(0, at) : '';
}

export function joinFolderPath(parent: string, leaf: string, delimiter: string): string {
  return parent && delimiter ? `${parent}${delimiter}${leaf}` : leaf;
}

export function hasChildren(folders: readonly WebmailFolder[], folder: WebmailFolder): boolean {
  if (!folder.delimiter) return false;
  const prefix = `${folder.name}${folder.delimiter}`;
  return folders.some((f) => f.name.startsWith(prefix));
}

// Controles C0, DEL y C1: los rechaza ValidateFolderName del servicio.
function hasControl(text: string): boolean {
  return Array.from(text).some((char) => {
    const code = char.codePointAt(0) ?? 0;
    return code <= 0x1f || (code >= 0x7f && code <= 0x9f);
  });
}

/**
 * Nombre de una carpeta nueva o renombrada con los criterios del servicio: el tramo sin el
 * separador ni comodines, y la ruta completa dentro del tope de bytes que sirve la meta.
 */
export function folderNameProblem(
  leaf: string,
  path: string,
  delimiter: string,
  maxBytes: number | null,
): string | null {
  const name = leaf.trim();
  if (!name) return t('webmail.folderAdmin.nameRequired');
  if (delimiter && name.includes(delimiter)) {
    return t('webmail.folderAdmin.nameDelimiter', { delimiter });
  }
  if (name.includes('*') || name.includes('%') || hasControl(name)) {
    return t('webmail.folderAdmin.nameInvalid');
  }
  if (maxBytes !== null && utf8Length(path) > maxBytes) {
    return t('webmail.folderAdmin.nameTooLong');
  }
  return null;
}

/** Ya hay una carpeta con esa ruta (sin distinguir mayusculas); INBOX esta reservada (RFC 3501). */
export function folderNameTaken(
  folders: readonly WebmailFolder[],
  path: string,
  except?: string,
): boolean {
  const wanted = path.toLowerCase();
  return (
    wanted === 'inbox' || folders.some((f) => f.name !== except && f.name.toLowerCase() === wanted)
  );
}

/** En Enviados y Borradores importa a quien va el mensaje, no quien lo envia. */
export function showsRecipients(role: string): boolean {
  return role === FOLDER_ROLES.sent || role === FOLDER_ROLES.drafts;
}
