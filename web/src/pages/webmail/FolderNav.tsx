import type { ComponentType } from 'react';
import { Link } from 'react-router-dom';
import { FOLDER_ROLES, type WebmailFolder } from '@/api/webmail';
import {
  IconArchive,
  IconBan,
  IconEdit,
  IconFolder,
  IconInbox,
  IconSend,
  IconTrash,
  type IconProps,
} from '@/design/icons';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { orderFolders } from './folders';

const ROLE_ICONS: Record<string, ComponentType<IconProps>> = {
  [FOLDER_ROLES.inbox]: IconInbox,
  [FOLDER_ROLES.sent]: IconSend,
  [FOLDER_ROLES.drafts]: IconEdit,
  [FOLDER_ROLES.trash]: IconTrash,
  [FOLDER_ROLES.junk]: IconBan,
  [FOLDER_ROLES.archive]: IconArchive,
};

const INDENT_REM = 0.75;

export function FolderNav({
  folders,
  current,
}: {
  folders: readonly WebmailFolder[];
  current: string | null;
}) {
  const format = new Intl.NumberFormat(getLocale());
  return (
    <nav aria-label={t('webmail.folders.title')}>
      <ul className="cf-wm-folders">
        {orderFolders(folders).map(({ folder, label, depth }) => {
          const Icon = ROLE_ICONS[folder.role] ?? IconFolder;
          const indent = depth ? { paddingLeft: `${depth * INDENT_REM}rem` } : undefined;
          if (!folder.selectable) {
            return (
              <li key={folder.name} style={indent}>
                <span className="cf-wm-folders__group">{label}</span>
              </li>
            );
          }
          return (
            <li key={folder.name} style={indent}>
              <Link
                to={paths.webmailView({ folder: folder.name })}
                className="cf-wm-folders__link"
                aria-current={folder.name === current ? 'page' : undefined}
              >
                <Icon size={16} />
                <span className="cf-wm-folders__name">{label}</span>
                {folder.unread > 0 ? (
                  <span className="cf-wm-folders__count">
                    <span aria-hidden="true">{format.format(folder.unread)}</span>
                    <span className="cf-visually-hidden">
                      {t('webmail.folders.unread', { n: format.format(folder.unread) })}
                    </span>
                  </span>
                ) : null}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
