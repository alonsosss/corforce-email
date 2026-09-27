import { useEffect, useState } from 'react';
import {
  DELIVERY_DIRECTIONS,
  DELIVERY_STATUSES,
  mailSecurityApi,
  type DeliveryDirection,
  type DeliveryEvent,
  type DeliveryStatus,
} from '@/api/mailSecurity';
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
  Modal,
  Select,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { DateFilters } from './DateFilters';
import { dayRange, deliveryStatusTone, validRange } from './sendingLog';

const SEARCH_DEBOUNCE_MS = 300;

/**
 * Correo corporativo: lo que los buzones enviaron (webmail, Outlook, el ERP con usuario de buzon) y
 * lo que llego a los dominios de la empresa, con la respuesta del otro servidor.
 */
export function CorporateTab() {
  const pager = usePagination();
  const [direction, setDirection] = useState<DeliveryDirection | ''>('');
  const [status, setStatus] = useState<DeliveryStatus | ''>('');
  const [addressInput, setAddressInput] = useState('');
  const [address, setAddress] = useState('');
  const [from, setFrom] = useState('');
  const [until, setUntil] = useState('');
  const [open, setOpen] = useState<DeliveryEvent | null>(null);

  useEffect(() => {
    const handle = window.setTimeout(() => {
      setAddress(addressInput.trim().toLowerCase());
      pager.reset();
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // pager.reset es estable; solo el texto dispara la busqueda.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [addressInput]);

  const rangeOk = validRange(from, until);
  const events = useQuery(
    () =>
      mailSecurityApi.deliveryLog({
        page: pager.page,
        per_page: pager.perPage,
        direction: direction || undefined,
        status: status || undefined,
        address: address || undefined,
        ...(rangeOk ? dayRange(from, until) : {}),
      }),
    [pager.page, pager.perPage, direction, status, address, from, until, rangeOk],
  );

  const columns: Column<DeliveryEvent>[] = [
    {
      key: 'date',
      header: t('sendingLog.column.date'),
      render: (e) => formatDateTime(e.occurred_at),
    },
    {
      key: 'direction',
      header: t('sendingLog.column.direction'),
      render: (e) => (
        <Badge tone={e.direction === 'outbound' ? 'accent' : 'neutral'}>
          {tEnum('sendingLog.direction', e.direction)}
        </Badge>
      ),
    },
    {
      key: 'from',
      header: t('sendingLog.detail.from'),
      render: (e) => (
        <span className="cf-mono cf-break">{e.sender || t('sendingLog.nullSender')}</span>
      ),
    },
    {
      key: 'to',
      header: t('sendingLog.column.to'),
      render: (e) => <span className="cf-mono cf-break">{e.recipient}</span>,
    },
    {
      key: 'status',
      header: t('sendingLog.column.status'),
      render: (e) => (
        <Badge tone={deliveryStatusTone(e.status)}>
          {tEnum('sendingLog.deliveryStatus', e.status)}
        </Badge>
      ),
    },
    {
      key: 'reason',
      header: t('sendingLog.column.reason'),
      render: (e) =>
        e.status === 'sent' ? (
          t('common.dash')
        ) : (
          <span className="cf-text-sm cf-break">{e.reason || t('common.dash')}</span>
        ),
    },
  ];

  return (
    <Card
      flush
      title={t('sendingLog.corporate.title')}
      description={t('sendingLog.corporate.hint')}
    >
      <div className="cf-toolbar">
        <FormField label={t('sendingLog.filter.address')} htmlFor="delivery-log-address">
          <Input
            id="delivery-log-address"
            type="email"
            placeholder={t('sendingLog.filter.recipientPlaceholder')}
            value={addressInput}
            onChange={(e) => setAddressInput(e.target.value)}
          />
        </FormField>
        <FormField label={t('sendingLog.column.direction')} htmlFor="delivery-log-direction">
          <Select
            id="delivery-log-direction"
            placeholder={t('common.all')}
            options={DELIVERY_DIRECTIONS.map((d) => ({
              value: d,
              label: tEnum('sendingLog.direction', d),
            }))}
            value={direction}
            onChange={(e) => {
              setDirection(e.target.value as DeliveryDirection | '');
              pager.reset();
            }}
          />
        </FormField>
        <FormField label={t('sendingLog.column.status')} htmlFor="delivery-log-status">
          <Select
            id="delivery-log-status"
            placeholder={t('common.all')}
            options={DELIVERY_STATUSES.map((s) => ({
              value: s,
              label: tEnum('sendingLog.deliveryStatus', s),
            }))}
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as DeliveryStatus | '');
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
        rows={events.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={events.loading}
        error={events.error}
        onRetry={events.reload}
        onRowClick={setOpen}
        empty={{ title: t('sendingLog.empty'), description: t('sendingLog.corporate.emptyHint') }}
        pagination={{
          page: events.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: events.data?.total ?? 0,
          totalPages: events.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
      />
      {open ? (
        <Modal open title={t('sendingLog.detail.title')} onClose={() => setOpen(null)} size="lg">
          <DescriptionList
            items={[
              {
                label: t('sendingLog.column.status'),
                value: (
                  <Badge tone={deliveryStatusTone(open.status)}>
                    {tEnum('sendingLog.deliveryStatus', open.status)}
                  </Badge>
                ),
              },
              {
                label: t('sendingLog.column.direction'),
                value: tEnum('sendingLog.direction', open.direction),
              },
              { label: t('sendingLog.column.date'), value: formatDateTime(open.occurred_at) },
              {
                label: t('sendingLog.detail.from'),
                value: open.sender || t('sendingLog.nullSender'),
              },
              { label: t('sendingLog.column.to'), value: open.recipient },
              {
                label: t('sendingLog.detail.mailbox'),
                value: open.sasl_username || t('common.dash'),
              },
              { label: t('sendingLog.detail.dsn'), value: open.dsn || t('common.dash') },
              { label: t('sendingLog.detail.relay'), value: open.relay || t('common.dash') },
              {
                label: t('sendingLog.detail.delay'),
                value: open.delay_seconds
                  ? t('sendingLog.detail.seconds', { n: open.delay_seconds })
                  : t('common.dash'),
              },
              {
                label: t('sendingLog.detail.response'),
                value: <span className="cf-mono cf-break">{open.reason || t('common.dash')}</span>,
              },
              {
                label: t('sendingLog.detail.messageId'),
                value: open.message_id || t('common.dash'),
              },
              { label: t('sendingLog.detail.queueId'), value: open.queue_id || t('common.dash') },
            ]}
          />
        </Modal>
      ) : null}
    </Card>
  );
}
