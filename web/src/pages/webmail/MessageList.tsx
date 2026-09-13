import { useState } from 'react';
import { Link } from 'react-router-dom';
import { FLAGS, hasFlag, type MessageEnvelope } from '@/api/webmail';
import type { Page } from '@/api/types';
import { errorMessage } from '@/api/messages';
import type { QueryState } from '@/hooks/useQuery';
import { Button, EmptyState, ErrorState, Input, Pagination, Skeleton } from '@/design/components';
import { IconPaperclip, IconRefresh, IconSearch, IconStar } from '@/design/icons';
import { t } from '@/i18n';
import { showsRecipients } from './folders';
import { addressLabel, formatMailDate } from './format';

const SKELETON_ROWS = 6;

export interface MessageListProps {
  titleId: string;
  title: string;
  role: string;
  list: QueryState<Page<MessageEnvelope>>;
  selectedUid: number | null;
  search: string;
  hrefFor: (uid: number) => string;
  onSearch: (query: string) => void;
  onPage: (page: number) => void;
  onRefresh: () => void;
}

export function MessageList({
  titleId,
  title,
  role,
  list,
  selectedUid,
  search,
  hrefFor,
  onSearch,
  onPage,
  onRefresh,
}: MessageListProps) {
  const [draft, setDraft] = useState(search);
  const data = list.data;
  const items = data?.items ?? [];
  const recipients = showsRecipients(role);

  return (
    <>
      <div className="cf-wm-listbar">
        <div className="cf-wm-listbar__title">
          <h1 id={titleId} className="cf-wm-listbar__heading">
            {title}
          </h1>
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
        <form
          role="search"
          className="cf-wm-search"
          onSubmit={(e) => {
            e.preventDefault();
            onSearch(draft.trim());
          }}
        >
          <label htmlFor="wm-search" className="cf-visually-hidden">
            {t('webmail.list.search')}
          </label>
          <Input
            id="wm-search"
            type="search"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder={t('webmail.list.searchPlaceholder')}
          />
          <Button type="submit" iconOnly icon={<IconSearch size={16} />}>
            {t('common.search')}
          </Button>
        </form>
        {search ? (
          <div className="cf-wm-listbar__filter">
            <span className="cf-text-sm cf-truncate">
              {t('webmail.list.results', { q: search })}
            </span>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setDraft('');
                onSearch('');
              }}
            >
              {t('common.clear')}
            </Button>
          </div>
        ) : null}
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
          <EmptyState title={t(search ? 'webmail.list.noResults' : 'webmail.list.empty')} />
        ) : (
          <ul className="cf-wm-messages" aria-label={t('webmail.list.label')}>
            {items.map((message) => (
              <MessageRow
                key={message.uid}
                message={message}
                recipients={recipients}
                selected={message.uid === selectedUid}
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
  href,
}: {
  message: MessageEnvelope;
  recipients: boolean;
  selected: boolean;
  href: string;
}) {
  const unread = !hasFlag(message, FLAGS.seen);
  const flagged = hasFlag(message, FLAGS.flagged);
  const people = (recipients ? message.to : message.from).map(addressLabel).join(', ');
  const classes = ['cf-wm-message', unread ? 'cf-wm-message--unread' : '']
    .filter(Boolean)
    .join(' ');
  return (
    <li>
      <Link to={href} className={classes} aria-current={selected ? 'true' : undefined}>
        <span className="cf-wm-message__who">
          {unread ? <span className="cf-visually-hidden">{t('webmail.list.unread')} </span> : null}
          {people || t(recipients ? 'webmail.list.noRecipients' : 'webmail.list.noSender')}
        </span>
        <span className="cf-wm-message__date">{formatMailDate(message.date)}</span>
        <span className="cf-wm-message__subject">{message.subject || t('webmail.noSubject')}</span>
        <span className="cf-wm-message__marks">
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
