import { useState } from 'react';
import { Link } from 'react-router-dom';
import { webmailRemindersApi, type QuickReply } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Button, EmptyState, ErrorState, Modal, Skeleton } from '@/design/components';
import { IconFileText } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';

export interface QuickReplyPickerProps {
  disabled?: boolean;
  onPick: (reply: QuickReply) => void;
}

/** Boton de la redaccion que abre las respuestas rapidas del buzon; se leen al abrirlo. */
export function QuickReplyPicker({ disabled = false, onPick }: QuickReplyPickerProps) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        icon={<IconFileText size={16} />}
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        {t('webmail.quickReplies.insert')}
      </Button>
      {open ? (
        <QuickReplyList
          onClose={() => setOpen(false)}
          onPick={(reply) => {
            setOpen(false);
            onPick(reply);
          }}
        />
      ) : null}
    </>
  );
}

function QuickReplyList({
  onClose,
  onPick,
}: {
  onClose: () => void;
  onPick: (reply: QuickReply) => void;
}) {
  const replies = useQuery((signal) => webmailRemindersApi.quickReplies(signal), []);
  const items = replies.data?.items ?? [];

  return (
    <Modal open title={t('webmail.quickReplies.pickTitle')} onClose={onClose}>
      {replies.error && !replies.data ? (
        <ErrorState error={replies.error} onRetry={replies.reload} />
      ) : !replies.data ? (
        <Skeleton lines={4} />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<IconFileText size={32} />}
          title={t('webmail.quickReplies.empty')}
          action={
            <Link
              className="cf-btn cf-btn--secondary"
              to={paths.webmailSettingsTab('quickReplies')}
            >
              {t('webmail.quickReplies.manage')}
            </Link>
          }
        />
      ) : (
        <ul className="cf-wm-quick-replies" aria-label={t('webmail.quickReplies.pickTitle')}>
          {items.map((reply) => (
            <li key={reply.id}>
              <button
                type="button"
                className="cf-wm-quick-replies__item"
                onClick={() => onPick(reply)}
              >
                <span className="cf-wm-quick-replies__name">{reply.name}</span>
                <span className="cf-text-sm cf-text-secondary cf-truncate">{reply.text}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}
