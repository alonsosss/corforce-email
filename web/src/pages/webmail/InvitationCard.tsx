import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import {
  INVITATION_RESPONSES,
  webmailApi,
  type Invitation,
  type InvitationApplied,
  type InvitationResponse,
  type MessagePart,
} from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import { Badge, Button, Skeleton, useToast } from '@/design/components';
import { IconCalendar, IconCheck, IconClock, IconX } from '@/design/icons';
import { hasMessage, t } from '@/i18n';
import { paths } from '@/paths';
import { formatEventWhen } from './calendar/calendar';

const CALENDAR_TYPES = ['text/calendar', 'application/ics'];

/** Cierto si el mensaje lleva una invitacion de calendario (RFC 6047). */
export function hasInvitation(message: { attachments: readonly MessagePart[] }): boolean {
  return message.attachments.some((a) => CALENDAR_TYPES.includes(a.content_type.toLowerCase()));
}

const RESPONSE_ICONS: Record<InvitationResponse, ReactNode> = {
  ACCEPTED: <IconCheck size={16} />,
  TENTATIVE: <IconClock size={16} />,
  DECLINED: <IconX size={16} />,
};

const RESPONSE_LABELS = {
  ACCEPTED: 'webmail.invitation.accept',
  TENTATIVE: 'webmail.invitation.tentative',
  DECLINED: 'webmail.invitation.decline',
} as const;

function partstatLabel(value: string): string {
  const key = `webmail.calendar.partstat.${value}`;
  return hasMessage(key) ? t(key) : value;
}

function methodLabel(value: string): string {
  const key = `webmail.invitation.method.${value}`;
  return hasMessage(key) ? t(key) : t('webmail.invitation.title');
}

/**
 * Invitacion de calendario de un mensaje: la valida mail-dav y se cruza con el calendario del buzon. Una
 * invitacion se acepta, se deja en tentativo o se rechaza (la respuesta sale hacia el organizador); una
 * respuesta a una reunion propia se anota sola en el calendario; una cancelacion se aplica a peticion.
 */
export function InvitationCard({ folder, uid }: { folder: string; uid: number }) {
  const toast = useToast();
  const invitation = useQuery(
    (signal) => webmailApi.invitation(folder, uid, signal),
    [folder, uid],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [applied, setApplied] = useState<InvitationApplied | null>(null);
  const inv = invitation.data;
  const autoApply = Boolean(inv && inv.method === 'REPLY' && inv.is_organizer);

  useEffect(() => {
    if (!autoApply) return;
    let cancelled = false;
    webmailApi
      .applyInvitation(folder, uid)
      .then((res) => !cancelled && setApplied(res))
      .catch((err: unknown) => !cancelled && setError(err));
    return () => {
      cancelled = true;
    };
  }, [autoApply, folder, uid]);

  if (invitation.error) {
    return (
      <section className="cf-wm-invitation" aria-label={t('webmail.invitation.title')}>
        <p className="cf-field__hint">{t('webmail.invitation.unreadable')}</p>
      </section>
    );
  }
  if (!inv) {
    return (
      <section className="cf-wm-invitation" aria-label={t('webmail.invitation.title')}>
        <Skeleton lines={2} />
      </section>
    );
  }

  const respond = async (response: InvitationResponse) => {
    setBusy(true);
    setError(null);
    try {
      const res = await webmailApi.respondInvitation(folder, uid, response);
      if (res.reply_sent) toast.success(t('webmail.invitation.responded'));
      else toast.error(t('webmail.invitation.respondedNoMail'));
      invitation.reload();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    setBusy(true);
    setError(null);
    try {
      setApplied(await webmailApi.applyInvitation(folder, uid));
      toast.success(t('webmail.invitation.cancelApplied'));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const when = inv.start && inv.end ? formatEventWhen(inv.start, inv.end, inv.all_day) : '';
  const replier = inv.attendees[0];
  const calendarLink = inv.start
    ? `${paths.webmailCalendar}?view=week&date=${inv.start.slice(0, 10)}`
    : paths.webmailCalendar;

  return (
    <section className="cf-wm-invitation" aria-label={t('webmail.invitation.title')}>
      <header className="cf-wm-invitation__head">
        <IconCalendar size={18} />
        <strong>{methodLabel(inv.method)}</strong>
        <span className="cf-wm-invitation__title">{inv.title}</span>
      </header>
      <dl className="cf-dl cf-wm-invitation__meta">
        {when ? (
          <>
            <dt>{t('webmail.invitation.when')}</dt>
            <dd>{when}</dd>
          </>
        ) : null}
        {inv.location ? (
          <>
            <dt>{t('webmail.invitation.where')}</dt>
            <dd>{inv.location}</dd>
          </>
        ) : null}
        {inv.organizer ? (
          <>
            <dt>{t('webmail.invitation.organizer')}</dt>
            <dd>
              {inv.organizer.name
                ? `${inv.organizer.name} <${inv.organizer.email}>`
                : inv.organizer.email}
            </dd>
          </>
        ) : null}
      </dl>
      {inv.recurring ? <p className="cf-field__hint">{t('webmail.invitation.recurring')}</p> : null}
      <InvitationActions
        inv={inv}
        busy={busy}
        applied={applied}
        replier={replier ? `${replier.name || replier.email}` : ''}
        replierStatus={replier ? partstatLabel(replier.partstat) : ''}
        onRespond={(r) => void respond(r)}
        onCancel={() => void cancel()}
      />
      {error ? (
        <div className="cf-form__error" role="alert">
          {errorMessage(error)}
        </div>
      ) : null}
      {inv.event_id || applied?.event_id ? (
        <Link className="cf-wm-invitation__link" to={calendarLink}>
          {t('webmail.invitation.openCalendar')}
        </Link>
      ) : null}
    </section>
  );
}

function InvitationActions({
  inv,
  busy,
  applied,
  replier,
  replierStatus,
  onRespond,
  onCancel,
}: {
  inv: Invitation;
  busy: boolean;
  applied: InvitationApplied | null;
  replier: string;
  replierStatus: string;
  onRespond: (response: InvitationResponse) => void;
  onCancel: () => void;
}) {
  if (inv.method === 'REPLY') {
    return (
      <p>
        {inv.is_organizer && applied
          ? t('webmail.invitation.replyApplied', { attendee: replier, status: replierStatus })
          : t('webmail.invitation.replyFrom', { attendee: replier, status: replierStatus })}
      </p>
    );
  }
  if (inv.method === 'CANCEL') {
    if (applied?.changed) return <p>{t('webmail.invitation.cancelApplied')}</p>;
    if (!inv.event_id)
      return <p className="cf-field__hint">{t('webmail.invitation.cancelNotInCalendar')}</p>;
    return (
      <div className="cf-wm-invitation__actions">
        <Button size="sm" variant="danger" loading={busy} onClick={onCancel}>
          {t('webmail.invitation.cancelApply')}
        </Button>
      </div>
    );
  }
  if (!inv.attendee) return <p className="cf-field__hint">{t('webmail.invitation.notInvited')}</p>;
  return (
    <>
      {inv.partstat ? (
        <p>
          {t('webmail.invitation.yourResponse', { status: partstatLabel(inv.partstat) })}{' '}
          {inv.event_id ? <Badge tone="success">{t('webmail.invitation.inCalendar')}</Badge> : null}
        </p>
      ) : null}
      <div
        className="cf-wm-invitation__actions"
        role="group"
        aria-label={t('webmail.invitation.title')}
      >
        {INVITATION_RESPONSES.map((response) => (
          <Button
            key={response}
            size="sm"
            variant={inv.partstat === response ? 'primary' : 'secondary'}
            icon={RESPONSE_ICONS[response]}
            aria-pressed={inv.partstat === response}
            disabled={busy}
            onClick={() => onRespond(response)}
          >
            {t(RESPONSE_LABELS[response])}
          </Button>
        ))}
      </div>
    </>
  );
}
