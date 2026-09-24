import { useState, type ComponentType } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { FOLDER_ROLES, type WebmailFolder } from '@/api/webmail';
import { Button } from '@/design/components';
import {
  IconArchive,
  IconBan,
  IconClock,
  IconEdit,
  IconFolder,
  IconInbox,
  IconPlus,
  IconSend,
  IconSettings,
  IconTrash,
  type IconProps,
} from '@/design/icons';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { CreateFolderDialog, EditFolderDialog } from './FolderDialogs';
import { isProtectedFolder, orderFolders } from './folders';

const ROLE_ICONS: Record<string, ComponentType<IconProps>> = {
  [FOLDER_ROLES.inbox]: IconInbox,
  [FOLDER_ROLES.sent]: IconSend,
  [FOLDER_ROLES.drafts]: IconEdit,
  [FOLDER_ROLES.trash]: IconTrash,
  [FOLDER_ROLES.junk]: IconBan,
  [FOLDER_ROLES.archive]: IconArchive,
  [FOLDER_ROLES.scheduled]: IconClock,
};

const INDENT_REM = 0.75;

export interface FolderNavProps {
  folders: readonly WebmailFolder[];
  current: string | null;
  /** Se creo, renombro o borro una carpeta: hay que volver a leer la lista. */
  onChanged: () => void;
}

export function FolderNav({ folders, current, onChanged }: FolderNavProps) {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<WebmailFolder | null>(null);
  const format = new Intl.NumberFormat(getLocale());

  return (
    <nav aria-label={t('webmail.folders.title')}>
      <div className="cf-wm-folders__head">
        <span className="cf-wm-folders__title">{t('webmail.folders.title')}</span>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon={<IconPlus size={16} />}
          onClick={() => setCreating(true)}
        >
          {t('webmail.folderAdmin.new')}
        </Button>
      </div>
      <ul className="cf-wm-folders">
        {orderFolders(folders).map(({ folder, label, depth }) => {
          const Icon = ROLE_ICONS[folder.role] ?? IconFolder;
          const indent = depth ? { paddingLeft: `${depth * INDENT_REM}rem` } : undefined;
          const manage = isProtectedFolder(folder) ? null : (
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              className="cf-wm-folders__manage"
              icon={<IconSettings size={14} />}
              onClick={() => setEditing(folder)}
            >
              {t('webmail.folderAdmin.manage', { name: label })}
            </Button>
          );
          if (!folder.selectable) {
            return (
              <li key={folder.name} style={indent} className="cf-wm-folders__item">
                <span className="cf-wm-folders__group">{label}</span>
                {manage}
              </li>
            );
          }
          const href =
            folder.role === FOLDER_ROLES.scheduled
              ? paths.webmailScheduled
              : paths.webmailView({ folder: folder.name });
          return (
            <li key={folder.name} style={indent} className="cf-wm-folders__item">
              <Link
                to={href}
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
              {manage}
            </li>
          );
        })}
      </ul>
      {creating ? (
        <CreateFolderDialog
          folders={folders}
          onClose={() => setCreating(false)}
          onCreated={(created) => {
            setCreating(false);
            onChanged();
            navigate(paths.webmailView({ folder: created.name }));
          }}
        />
      ) : null}
      {editing ? (
        <EditFolderDialog
          folder={editing}
          folders={folders}
          onClose={() => setEditing(null)}
          onRenamed={(renamed) => {
            const wasCurrent =
              current !== null &&
              (current === editing.name ||
                current.startsWith(`${editing.name}${editing.delimiter}`));
            setEditing(null);
            onChanged();
            if (wasCurrent)
              navigate(paths.webmailView({ folder: renamed.name }), { replace: true });
          }}
          onDeleted={() => {
            const wasCurrent = current === editing.name;
            setEditing(null);
            onChanged();
            if (wasCurrent) navigate(paths.webmail, { replace: true });
          }}
        />
      ) : null}
    </nav>
  );
}
