import { useEffect, useState } from 'react';
import {
  MESSAGE_STATUSES,
  transactionalApi,
  type MessageStatus,
  type TransactionalMessage,
} from '@/api/transactional';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Card,
  DataTable,
  DescriptionList,
  FormField,
  Input,
  LoadingBlock,
  Modal,
  Select,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { DateFilters } from './DateFilters';
import { dayRange, eventReason, messageStatusTone, recipientsOf, validRange } from './sendingLog';

const SEARCH_DEBOUNCE_MS = 300;

/** Correo transaccional: API, relay del ERP, campanas y avisos, con el estado que dio SES. */
export function TransactionalTab() {
  const pager = usePagination();
  const [status, setStatus] = useState<MessageStatus | ''>('');
  const [toInput, setToInput] = useState('');
  const [to, setTo] = useState('');
  const [from, setFrom] = useState('');
  const [until, setUntil] = useState('');
  const [open, setOpen] = useState<TransactionalMessage | null>(null);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setTo(toInput.trim().toLowerCase());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable; solo el texto dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [toInput]);

  const rangeOk = validRange(from, until);
  const messages = useQuery(
    () =>
      transactionalApi.messages({
        page: pager.page,
        per_page: pager.perPage,
        status: status || undefined,
        to: to || undefined,
        ...(rangeOk ? dayRange(from, until) : {}),
      }),
    [pager.page, pager.perPage, status, to, from, until, rangeOk],
  );

  const columns: Column<TransactionalMessage>[] = [
    {
      key: 'date',
      header: t('sendingLog.column.date'),
      render: (m) => formatDateTime(m.created_at),
    },
    {
      key: 'to',
      header: t('sendingLog.column.to'),
      render: (m) => <span className="cf-mono cf-break">{recipientsOf(m.to)}</span>,
    },
    {
      key: 'subject',
      header: t('sendingLog.column.subject'),
      render: (m) => <span className="cf-break">{m.subject || t('common.dash')}</span>,
    },
    {
      key: 'status',
      header: t('sendingLog.column.status'),
      render: (m) => (
        <Badge tone={messageStatusTone(m.status)}>
          {tEnum('sendingLog.messageStatus', m.status)}
        </Badge>
      ),
    },
    {
      key: 'reason',
      header: t('sendingLog.column.reason'),
      render: (m) => <span className="cf-text-sm cf-break">{m.error || t('common.dash')}</span>,
    },
    {
      key: 'origin',
      header: t('sendingLog.column.origin'),
      render: (m) => tEnum('sendingLog.origin', m.origin || 'api'),
    },
  ];

  return (
    <Card
      flush
      title={t('sendingLog.transactional.title')}
      description={t('sendingLog.transactional.hint')}
    >
      <div className="cf-toolbar">
        <FormField label={t('sendingLog.filter.recipient')} htmlFor="sending-log-to">
          <Input
            id="sending-log-to"
            type="email"
            placeholder={t('sendingLog.filter.recipientPlaceholder')}
            value={toInput}
            onChange={(e) => setToInput(e.target.value)}
          />
        </FormField>
        <FormField label={t('sendingLog.column.status')} htmlFor="sending-log-status">
          <Select
            id="sending-log-status"
            placeholder={t('common.all')}
            options={MESSAGE_STATUSES.map((s) => ({
              value: s,
              label: tEnum('sendingLog.messageStatus', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as MessageStatus | '');
              pager.reset();
            }}
          />
        </FormField>
        <DateFilters
          from={from}
          until={until}
          onFrom={setFrom}
          onUntil={setUntil}
          onChange={pager.reset}
        />
      </div>
      {!rangeOk ? <Alert tone="warning">{t('sendingLog.filter.badRange')}</Alert> : null}
      <DataTable
        columns={columns}
        rows={messages.data?.items ?? []}
        rowKey={(m) => m.id}
        loading={messages.loading}
        error={messages.error}
        onRetry={messages.reload}
        onRowClick={setOpen}
        empty={{ title: t('sendingLog.empty') }}
        pagination={{
          page: messages.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: messages.data?.total ?? 0,
          totalPages: messages.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {open ? <MessageDetail message={open} onClose={() => setOpen(null)} /> : null}
    </Card>
  );
}

/** Detalle de un mensaje con la linea de tiempo de SES: entrega, rebote y su motivo, quejas. */
function MessageDetail({
  message,
  onClose,
}: {
  message: TransactionalMessage;
  onClose: () => void;
}) {
  const events = useQuery(() => transactionalApi.events(message.id), [message.id]);
  return (
    <Modal open title={message.subject || t('sendingLog.detail.title')} onClose={onClose} size="lg">
      <div className="cf-stack">
        <DescriptionList
          items={[
            {
              label: t('sendingLog.column.status'),
              value: (
                <Badge tone={messageStatusTone(message.status)}>
                  {tEnum('sendingLog.messageStatus', message.status)}
                </Badge>
              ),
            },
            { label: t('sendingLog.detail.from'), value: message.from_email },
            {
              label: t('sendingLog.column.to'),
              value: recipientsOf(message.to) || t('common.dash'),
            },
            { label: t('sendingLog.detail.created'), value: formatDateTime(message.created_at) },
            {
              label: t('sendingLog.detail.sent'),
              value: message.sent_at ? formatDateTime(message.sent_at) : t('common.dash'),
            },
            {
              label: t('sendingLog.column.origin'),
              value: tEnum('sendingLog.origin', message.origin || 'api'),
            },
            { label: t('sendingLog.detail.attempts'), value: String(message.attempts) },
            { label: t('sendingLog.column.reason'), value: message.error || t('common.dash') },
          ]}
        />
        <div className="cf-form__section">{t('sendingLog.detail.timeline')}</div>
        {events.loading && !events.data ? <LoadingBlock /> : null}
        {events.error ? <Alert tone="danger">{t('sendingLog.detail.eventsError')}</Alert> : null}
        {events.data && events.data.length === 0 ? (
          <span className="cf-text-sm cf-text-muted">{t('sendingLog.detail.noEvents')}</span>
        ) : null}
        <ul
          className="cf-stack"
          style={{ gap: 'var(--cf-space-2)', listStyle: 'none', padding: 0 }}
        >
          {(events.data ?? []).map((ev) => {
            const reason = eventReason(ev);
            return (
              <li key={ev.id} className="cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
                <span className="cf-inline">
                  <strong>{tEnum('sendingLog.eventType', ev.type)}</strong>
                  <span className="cf-text-sm cf-text-secondary">
                    {formatDateTime(ev.occurred_at)} · {ev.recipient}
                  </span>
                </span>
                {reason ? <span className="cf-text-sm cf-mono cf-break">{reason}</span> : null}
              </li>
            );
          })}
        </ul>
      </div>
    </Modal>
  );
}
