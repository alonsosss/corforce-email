import { useEffect, useRef, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { FLAGS, hasFlag, type MessageEnvelope } from '@/api/webmail';
import type { Page } from '@/api/types';
import { errorMessage } from '@/api/messages';
import type { QueryState } from '@/hooks/useQuery';
import {
  Button,
  Checkbox,
  EmptyState,
  ErrorState,
  Pagination,
  Skeleton,
} from '@/design/components';
import { IconPaperclip, IconRefresh, IconStar } from '@/design/icons';
import { t } from '@/i18n';
import { showsRecipients } from './folders';
import { addressLabel, formatMailDate, initialsOf } from './format';
import { rowUnread } from './smartInbox';
import { criteriaSummary, EMPTY_CRITERIA, isSearching, type SearchCriteria } from './search';

const SKELETON_ROWS = 6;

export interface MessageListProps {
  titleId: string;
  title: string;
  role: string;
  list: QueryState<Page<MessageEnvelope>>;
  selectedUid: number | null;
  criteria: SearchCriteria;
  hrefFor: (uid: number) => string;
  onSearch: (criteria: SearchCriteria) => void;
  onPage: (page: number) => void;
  onRefresh: () => void;
  /** Mensajes marcados con su casilla para actuar sobre varios a la vez. */
  checked: ReadonlySet<number>;
  onCheck: (uid: number, checked: boolean) => void;
  onCheckAll: (checked: boolean) => void;
  /** Acciones sobre los marcados; solo se muestra con alguno marcado. */
  selectionBar?: ReactNode;
  /** Accion de la carpeta junto al titulo (vaciar Papelera o Spam). */
  folderAction?: ReactNode;
  /** Conmutador de vista y pestanas de la bandeja, bajo el titulo. */
  controls?: ReactNode;
}

export function MessageList({
  titleId,
  title,
  role,
  list,
  selectedUid,
  criteria,
  hrefFor,
  onSearch,
  onPage,
  onRefresh,
  checked,
  onCheck,
  onCheckAll,
  selectionBar,
  folderAction,
  controls,
}: MessageListProps) {
  const data = list.data;
  const items = data?.items ?? [];
  const recipients = showsRecipients(role);
  const searching = isSearching(criteria);
  const checkedOnPage = items.filter((m) => checked.has(m.uid)).length;
  const allChecked = items.length > 0 && checkedOnPage === items.length;
  const selectAllRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (selectAllRef.current) {
      selectAllRef.current.indeterminate = checkedOnPage > 0 && !allChecked;
    }
  }, [checkedOnPage, allChecked]);

  return (
    <>
      <div className="cf-wm-listbar">
        <div className="cf-wm-listbar__title">
          <h1 id={titleId} className="cf-wm-listbar__heading">
            {title}
          </h1>
          <div className="cf-wm-listbar__tools">
            {folderAction}
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon={<IconRefresh size={16} />}
              loading={list.loading && data !== null}
              onClick={onRefresh}
            >
              {t('common.refresh')}
            </Button>
          </div>
        </div>
        {controls}
        {searching ? (
          <div className="cf-wm-listbar__filter">
            <span className="cf-text-sm cf-truncate">
              {t('webmail.list.results', { q: criteriaSummary(criteria) })}
            </span>
            <Button size="sm" variant="ghost" onClick={() => onSearch(EMPTY_CRITERIA)}>
              {t('common.clear')}
            </Button>
          </div>
        ) : null}
        {data?.totalCapped ? (
          <p className="cf-field__hint" role="status">
            {t('webmail.search.capped')}
          </p>
        ) : null}
        {items.length > 0 ? (
          <div className="cf-wm-listbar__select">
            <Checkbox
              ref={selectAllRef}
              label={
                checked.size
                  ? t('webmail.batch.selected', { n: checked.size })
                  : t('webmail.batch.selectPage')
              }
              checked={allChecked}
              onChange={(e) => onCheckAll(e.target.checked)}
            />
          </div>
        ) : null}
        {checked.size > 0 ? selectionBar : null}
        {list.error && data ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(list.error)}
          </div>
        ) : null}
      </div>
      <div className="cf-wm-listbody" aria-busy={list.loading || undefined}>
        {list.error && !data ? (
          <ErrorState error={list.error} onRetry={list.reload} />
        ) : !data ? (
          <div aria-hidden="true">
            {Array.from({ length: SKELETON_ROWS }, (_, i) => (
              <div key={i} className="cf-wm-message cf-wm-message--skeleton">
                <Skeleton width="60%" />
                <Skeleton width="3rem" />
                <Skeleton width="85%" />
              </div>
            ))}
          </div>
        ) : items.length === 0 ? (
          <EmptyState title={t(searching ? 'webmail.list.noResults' : 'webmail.list.empty')} />
        ) : (
          <ul className="cf-wm-messages" aria-label={t('webmail.list.label')}>
            {items.map((message) => (
              <MessageRow
                key={message.uid}
                message={message}
                recipients={recipients}
                selected={message.uid === selectedUid}
                checked={checked.has(message.uid)}
                onCheck={(value) => onCheck(message.uid, value)}
                href={hrefFor(message.uid)}
              />
            ))}
          </ul>
        )}
      </div>
      {data && data.totalPages > 1 ? (
        <Pagination
          page={data.page}
          perPage={data.perPage}
          total={data.total}
          totalPages={data.totalPages}
          totalCapped={data.totalCapped}
          onPageChange={onPage}
        />
      ) : null}
    </>
  );
}

function MessageRow({
  message,
  recipients,
  selected,
  checked,
  onCheck,
  href,
}: {
  message: MessageEnvelope;
  recipients: boolean;
  selected: boolean;
  checked: boolean;
  onCheck: (checked: boolean) => void;
  href: string;
}) {
  const unread = rowUnread(message, hasFlag(message, FLAGS.seen));
  const flagged = hasFlag(message, FLAGS.flagged);
  const thread = message.thread;
  const senders = thread && !recipients ? thread.participants : message.from;
  const shown = recipients ? message.to : senders;
  const people = shown.map(addressLabel).join(', ');
  const subject = message.subject || t('webmail.noSubject');
  const classes = [
    'cf-wm-message',
    unread ? 'cf-wm-message--unread' : '',
    checked ? 'cf-wm-message--checked' : '',
  ]
    .filter(Boolean)
    .join(' ');
  return (
    <li className="cf-wm-message-row">
      <Checkbox
        className="cf-wm-message-row__check"
        aria-label={t('webmail.batch.check', { subject })}
        checked={checked}
        onChange={(e) => onCheck(e.target.checked)}
      />
      <Link to={href} className={classes} aria-current={selected ? 'true' : undefined}>
        <span className="cf-wm-avatar cf-wm-message__avatar" aria-hidden="true">
          {shown[0] ? initialsOf(addressLabel(shown[0])) : null}
        </span>
        <span className="cf-wm-message__who">
          {unread ? <span className="cf-visually-hidden">{t('webmail.list.unread')} </span> : null}
          {people || t(recipients ? 'webmail.list.noRecipients' : 'webmail.list.noSender')}
        </span>
        <span className="cf-wm-message__date">{formatMailDate(message.date)}</span>
        <span className="cf-wm-message__subject">{subject}</span>
        <span className="cf-wm-message__marks">
          {thread && thread.size > 1 ? (
            <span
              className="cf-wm-message__count"
              title={t('webmail.thread.count', { n: thread.size })}
            >
              <span aria-hidden="true">{thread.size}</span>
              <span className="cf-visually-hidden">
                {t('webmail.thread.count', { n: thread.size })}
              </span>
            </span>
          ) : null}
          {message.has_attachments ? (
            <IconPaperclip size={14} title={t('webmail.list.hasAttachments')} />
          ) : null}
          {flagged ? (
            <IconStar size={14} className="cf-wm-star" title={t('webmail.list.flagged')} />
          ) : null}
        </span>
      </Link>
    </li>
  );
}
