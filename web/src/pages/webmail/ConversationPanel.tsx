import { Link } from 'react-router-dom';
import { FLAGS, FOLDER_ROLES, hasFlag, webmailApi, type WebmailFolder } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { webmailMeta } from '@/webmail/catalogs';
import { folderWithRole } from './folders';
import { addressList, formatMailDate } from './format';

export interface ConversationPanelProps {
  folderName: string;
  uid: number;
  folders: readonly WebmailFolder[];
  /** Enlace a un mensaje de la carpeta abierta, conservando la vista. */
  hrefFor: (uid: number) => string;
}

/**
 * La conversacion del mensaje abierto, de la mas antigua a la mas reciente, con las respuestas
 * propias que guarda Enviados. Solo aparece si hay mas de un mensaje; si no carga, el mensaje se
 * lee igual.
 */
export function ConversationPanel({ folderName, uid, folders, hrefFor }: ConversationPanelProps) {
  const conversation = useQuery(
    (signal) => webmailApi.conversation(folderName, uid, signal),
    [folderName, uid],
  );
  const cap = useResource(webmailMeta).data?.limits.max_thread_messages ?? null;
  const items = conversation.data ?? [];
  if (items.length < 2) return null;
  const sent = folderWithRole(folders, FOLDER_ROLES.sent);

  return (
    <nav className="cf-wm-conversation" aria-label={t('webmail.thread.label')}>
      <p className="cf-wm-conversation__count">{t('webmail.thread.count', { n: items.length })}</p>
      <ol className="cf-wm-conversation__list">
        {items.map((m) => {
          const current = m.folder === folderName && m.uid === uid;
          const mine = sent !== undefined && m.folder === sent.name;
          const href =
            m.folder === folderName
              ? hrefFor(m.uid)
              : paths.webmailView({ folder: m.folder, uid: m.uid });
          const classes = [
            'cf-wm-conversation__item',
            current ? 'cf-wm-conversation__item--current' : '',
            hasFlag(m, FLAGS.seen) ? '' : 'cf-wm-conversation__item--unread',
          ]
            .filter(Boolean)
            .join(' ');
          return (
            <li key={`${m.folder}-${m.uid}`}>
              <Link to={href} className={classes} aria-current={current ? 'true' : undefined}>
                <span className="cf-wm-conversation__who cf-truncate">
                  {mine
                    ? t('webmail.thread.sent')
                    : addressList(m.from) || t('webmail.list.noSender')}
                </span>
                <span className="cf-wm-conversation__date">{formatMailDate(m.date)}</span>
              </Link>
            </li>
          );
        })}
      </ol>
      {cap !== null && items.length >= cap ? (
        <p className="cf-field__hint">{t('webmail.thread.capped', { n: cap })}</p>
      ) : null}
    </nav>
  );
}
