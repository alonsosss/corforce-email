import { useState } from 'react';
import { ERROR_CODES, errorCode, errorDetail } from '@/api/errors';
import { webmailRemindersApi, type QuickReply, type QuickReplyList } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  FormField,
  Input,
  Modal,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconEdit, IconFileText, IconPlus, IconTrash } from '@/design/icons';
import { t } from '@/i18n';
import { formatBytes } from '@/lib/quota';
import { utf8Length } from '../format';
import { QUICK_REPLY_VARIABLE_LABELS, QUICK_REPLY_VARIABLES } from '../quickReplies';
import { RichEditor } from '../RichEditor';
import { htmlHasContent } from '../richText';

/** Respuestas rapidas del buzon: se crean aqui y se insertan desde la redaccion. */
export function QuickRepliesSettings() {
  const toast = useToast();
  const replies = useQuery((signal) => webmailRemindersApi.quickReplies(signal), []);
  const [editing, setEditing] = useState<QuickReply | 'new' | null>(null);
  const [deleting, setDeleting] = useState<QuickReply | null>(null);

  if (replies.error && !replies.data) {
    return (
      <Card title={t('webmail.quickReplies.title')}>
        <ErrorState error={replies.error} onRetry={replies.reload} />
      </Card>
    );
  }
  if (!replies.data) {
    return (
      <Card title={t('webmail.quickReplies.title')}>
        <Skeleton lines={5} />
      </Card>
    );
  }
  const list = replies.data;
  const full = list.items.length >= list.limits.max_items;
  const store = (saved: QuickReply) =>
    replies.setData((current) => {
      const base = current ?? list;
      const items = base.items.some((item) => item.id === saved.id)
        ? base.items.map((item) => (item.id === saved.id ? saved : item))
        : [...base.items, saved];
      return { ...base, items: items.sort((a, b) => a.name.localeCompare(b.name)) };
    });

  return (
    <Card
      title={t('webmail.quickReplies.title')}
      description={t('webmail.quickReplies.description')}
      actions={
        <Button
          size="sm"
          icon={<IconPlus size={16} />}
          disabled={full}
          title={full ? t('webmail.quickReplies.full', { n: list.limits.max_items }) : undefined}
          onClick={() => setEditing('new')}
        >
          {t('webmail.quickReplies.new')}
        </Button>
      }
    >
      {list.items.length === 0 ? (
        <EmptyState
          icon={<IconFileText size={32} />}
          title={t('webmail.quickReplies.empty')}
          description={t('webmail.quickReplies.emptyHint')}
        />
      ) : (
        <ul className="cf-wm-quick-replies">
          {list.items.map((reply) => (
            <li key={reply.id} className="cf-wm-quick-replies__row">
              <div className="cf-wm-scheduled__main">
                <span className="cf-wm-quick-replies__name">{reply.name}</span>
                <span className="cf-text-sm cf-text-secondary cf-truncate">{reply.text}</span>
              </div>
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                title={t('webmail.quickReplies.edit', { name: reply.name })}
                icon={<IconEdit size={16} />}
                onClick={() => setEditing(reply)}
              >
                {t('webmail.quickReplies.edit', { name: reply.name })}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                title={t('webmail.quickReplies.delete', { name: reply.name })}
                icon={<IconTrash size={16} />}
                onClick={() => setDeleting(reply)}
              >
                {t('webmail.quickReplies.delete', { name: reply.name })}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {editing ? (
        <QuickReplyDialog
          reply={editing === 'new' ? null : editing}
          limits={list.limits}
          onClose={() => setEditing(null)}
          onSaved={(saved) => {
            store(saved);
            toast.success(t('webmail.quickReplies.saved'));
            setEditing(null);
          }}
        />
      ) : null}
      <ConfirmDialog
        open={deleting !== null}
        title={t('webmail.quickReplies.deleteTitle')}
        message={t('webmail.quickReplies.deleteConfirm', { name: deleting?.name ?? '' })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setDeleting(null)}
        onConfirm={async () => {
          if (!deleting) return;
          await webmailRemindersApi.deleteQuickReply(deleting.id);
          replies.setData((current) =>
            current
              ? { ...current, items: current.items.filter((item) => item.id !== deleting.id) }
              : current,
          );
          toast.success(t('webmail.quickReplies.deleted'));
          setDeleting(null);
        }}
      />
    </Card>
  );
}

function QuickReplyDialog({
  reply,
  limits,
  onClose,
  onSaved,
}: {
  reply: QuickReply | null;
  limits: QuickReplyList['limits'];
  onClose: () => void;
  onSaved: (reply: QuickReply) => void;
}) {
  const [name, setName] = useState(reply?.name ?? '');
  const [html, setHtml] = useState(reply?.html ?? '');
  const [nameProblem, setNameProblem] = useState<string | null>(null);
  const [bodyProblem, setBodyProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const bytes = utf8Length(html);

  const submit = async () => {
    const cleanName = name.trim();
    const nameIssue = !cleanName
      ? t('webmail.quickReplies.nameRequired')
      : [...cleanName].length > limits.max_name_chars
        ? t('webmail.quickReplies.nameTooLong', { n: limits.max_name_chars })
        : null;
    const bodyIssue = !htmlHasContent(html)
      ? t('webmail.quickReplies.bodyRequired')
      : bytes > limits.max_html_bytes
        ? t('webmail.quickReplies.bodyTooLong', { max: formatBytes(limits.max_html_bytes) })
        : null;
    setNameProblem(nameIssue);
    setBodyProblem(bodyIssue);
    if (nameIssue || bodyIssue) return;
    setBusy(true);
    setError(null);
    try {
      const input = { name: cleanName, html };
      onSaved(
        reply
          ? await webmailRemindersApi.updateQuickReply(reply.id, input)
          : await webmailRemindersApi.createQuickReply(input),
      );
    } catch (err) {
      setBusy(false);
      const field =
        errorCode(err) === ERROR_CODES.VALIDATION_ERROR ? errorDetail(err, 'field') : null;
      if (field === 'name') setNameProblem(errorMessage(err));
      else if (field === 'html' || field === 'text') setBodyProblem(errorMessage(err));
      else setError(err);
    }
  };

  return (
    <Modal
      open
      size="lg"
      title={t(reply ? 'webmail.quickReplies.editTitle' : 'webmail.quickReplies.newTitle')}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void submit()}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <div className="cf-form">
        <FormField
          label={t('webmail.quickReplies.name')}
          htmlFor="wm-quick-name"
          error={nameProblem}
        >
          <Input
            id="wm-quick-name"
            value={name}
            invalid={Boolean(nameProblem)}
            onChange={(e) => {
              setName(e.target.value);
              setNameProblem(null);
            }}
          />
        </FormField>
        <div className="cf-field">
          <span className="cf-field__label" id="wm-quick-body-label">
            {t('webmail.quickReplies.body')}
          </span>
          <RichEditor
            id="wm-quick-body"
            labelledBy="wm-quick-body-label"
            initialHtml={reply?.html ?? ''}
            minHeight="10rem"
            onChange={(next) => {
              setHtml(next);
              setBodyProblem(null);
            }}
            disabled={busy}
          />
          {bodyProblem ? (
            <span className="cf-field__error" role="alert">
              {bodyProblem}
            </span>
          ) : (
            <span className="cf-field__hint">
              {t('webmail.quickReplies.size', {
                used: formatBytes(bytes),
                max: formatBytes(limits.max_html_bytes),
              })}
            </span>
          )}
        </div>
        <div className="cf-wm-quick-replies__vars">
          <span className="cf-field__label">{t('webmail.quickReplies.variables')}</span>
          <dl className="cf-dl">
            {QUICK_REPLY_VARIABLES.map((name) => (
              <div key={name} className="cf-wm-quick-replies__var">
                <dt>
                  <code>{`{${name}}`}</code>
                </dt>
                <dd>{t(QUICK_REPLY_VARIABLE_LABELS[name])}</dd>
              </div>
            ))}
          </dl>
        </div>
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
