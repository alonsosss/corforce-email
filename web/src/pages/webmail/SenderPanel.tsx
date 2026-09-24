import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { FOLDER_ROLES, webmailApi, type MailAddress, type WebmailFolder } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import { Badge, Button, Skeleton } from '@/design/components';
import { IconCalendar, IconMail, IconUser, IconUserPlus, IconX } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { davLimits, davMeta } from '@/webmail/catalogs';
import { ContactFormDialog } from './contacts/ContactFormDialog';
import { contactFromSender, contactName } from './contacts/contacts';
import { folderLabel, folderWithRole } from './folders';
import { formatMailDate } from './format';
import { contactFor, meetingsWith, previousMessages } from './senderPanel';

export interface SenderPanelProps {
  sender: MailAddress;
  /** Del escudo: null si no respondio. */
  internal: boolean | null;
  folderName: string;
  uid: number;
  folders: readonly WebmailFolder[];
  onClose: () => void;
}

/**
 * Ficha lateral del remitente: su contacto personal (libreta de mail-dav), sus correos anteriores
 * (busqueda por remitente) y las proximas reuniones que lo mencionan (calendario personal). Cada
 * seccion se carga y falla por su cuenta.
 */
export function SenderPanel({
  sender,
  internal,
  folderName,
  uid,
  folders,
  onClose,
}: SenderPanelProps) {
  const titleId = useId();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const [adding, setAdding] = useState(false);
  // Escape cierra primero el dialogo de contacto abierto encima, no el panel.
  const addingRef = useRef(adding);
  addingRef.current = adding;
  const name = sender.name.trim() || sender.email;

  useEffect(() => {
    headingRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !addingRef.current) onCloseRef.current();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);

  const contacts = useQuery(
    (signal) => webmailApi.contacts({ q: sender.email }, signal),
    [sender.email],
  );
  const contact = contacts.data ? contactFor(contacts.data.items, sender.email) : null;

  const inbox = folderWithRole(folders, FOLDER_ROLES.inbox);
  const searchFolder = inbox?.name ?? folderName;
  const previous = useQuery(
    (signal) => webmailApi.messages(searchFolder, { from: sender.email }, signal),
    [searchFolder, sender.email],
  );
  const shown = previous.data
    ? previousMessages(previous.data.items, searchFolder, { folder: folderName, uid })
    : [];

  // La ventana es la mayor que admite mail-dav: la interfaz no fija cuanto mira hacia delante.
  const windowDays = davLimits(useResource(davMeta).data).maxEventWindowDays;
  const meetings = useQuery(
    async (signal) => {
      if (!windowDays) return null;
      const now = new Date();
      const end = new Date(now.getTime() + windowDays * 24 * 60 * 60 * 1000);
      const occurrences = await webmailApi.calendarOccurrences(
        now.toISOString(),
        end.toISOString(),
        signal,
      );
      return meetingsWith(occurrences, sender, now);
    },
    [windowDays, sender.email, sender.name],
  );

  return (
    <aside className="cf-wm-sidepanel" aria-labelledby={titleId}>
      <header className="cf-wm-sidepanel__header">
        <h2 id={titleId} ref={headingRef} tabIndex={-1} className="cf-wm-sidepanel__title">
          {t('webmail.sender.title', { name })}
        </h2>
        <Button size="sm" variant="ghost" iconOnly icon={<IconX size={16} />} onClick={onClose}>
          {t('common.close')}
        </Button>
      </header>
      <p className="cf-wm-sidepanel__who">
        <span className="cf-truncate">{sender.email}</span>
        {internal === true ? <Badge tone="success">{t('webmail.sender.internal')}</Badge> : null}
        {internal === false ? <Badge tone="warning">{t('webmail.shield.external')}</Badge> : null}
      </p>

      <PanelSection icon={<IconUser size={16} />} title={t('webmail.sender.contact')}>
        {contacts.error ? (
          <p className="cf-field__hint">{t('webmail.sender.loadError')}</p>
        ) : !contacts.data ? (
          <Skeleton lines={2} />
        ) : contact ? (
          <dl className="cf-dl cf-wm-sidepanel__contact">
            <dt className="cf-visually-hidden">{t('webmail.sender.contact')}</dt>
            <dd>
              <Link to={paths.webmailContacts}>{contactName(contact)}</Link>
            </dd>
            {contact.organization || contact.title ? (
              <dd>{[contact.title, contact.organization].filter(Boolean).join(' - ')}</dd>
            ) : null}
            {contact.phones.map((phone) => (
              <dd key={`${phone.type}-${phone.value}`}>{phone.value}</dd>
            ))}
          </dl>
        ) : (
          <>
            <p className="cf-field__hint">{t('webmail.sender.notContact')}</p>
            <Button size="sm" icon={<IconUserPlus size={14} />} onClick={() => setAdding(true)}>
              {t('webmail.sender.addContact')}
            </Button>
          </>
        )}
      </PanelSection>

      <PanelSection icon={<IconMail size={16} />} title={t('webmail.sender.previous')}>
        {previous.error ? (
          <p className="cf-field__hint">{t('webmail.sender.loadError')}</p>
        ) : !previous.data ? (
          <Skeleton lines={3} />
        ) : shown.length === 0 ? (
          <p className="cf-field__hint">
            {t('webmail.sender.previousNone', { folder: inbox ? folderLabel(inbox) : folderName })}
          </p>
        ) : (
          <>
            <ul className="cf-wm-sidepanel__list">
              {shown.map((m) => (
                <li key={m.uid}>
                  <Link to={paths.webmailView({ folder: searchFolder, uid: m.uid })}>
                    <span className="cf-truncate">{m.subject || t('webmail.noSubject')}</span>
                    <span className="cf-wm-sidepanel__date">{formatMailDate(m.date)}</span>
                  </Link>
                </li>
              ))}
            </ul>
            {previous.data.total > shown.length ? (
              <Link to={paths.webmailView({ folder: searchFolder, from: sender.email })}>
                {t('webmail.sender.previousAll', { n: previous.data.total })}
              </Link>
            ) : null}
          </>
        )}
      </PanelSection>

      {windowDays ? (
        <PanelSection icon={<IconCalendar size={16} />} title={t('webmail.sender.meetings')}>
          <p className="cf-field__hint">{t('webmail.sender.meetingsHint', { days: windowDays })}</p>
          {meetings.error ? (
            <p className="cf-field__hint">{t('webmail.sender.loadError')}</p>
          ) : !meetings.data ? (
            <Skeleton lines={2} />
          ) : meetings.data.length === 0 ? (
            <p className="cf-field__hint">{t('webmail.sender.meetingsNone')}</p>
          ) : (
            <ul className="cf-wm-sidepanel__list">
              {meetings.data.map((o) => (
                <li key={`${o.id}-${o.start}`}>
                  <Link to={paths.webmailCalendar}>
                    <span className="cf-truncate">{o.title}</span>
                    <span className="cf-wm-sidepanel__date">{formatDateTime(o.start)}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </PanelSection>
      ) : null}

      {adding ? (
        <ContactFormDialog
          seed={contactFromSender(sender)}
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false);
            contacts.reload();
          }}
        />
      ) : null}
    </aside>
  );
}

function PanelSection({
  icon,
  title,
  children,
}: {
  icon: ReactNode;
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="cf-wm-sidepanel__section">
      <h3 className="cf-wm-sidepanel__heading">
        {icon}
        {title}
      </h3>
      {children}
    </section>
  );
}
