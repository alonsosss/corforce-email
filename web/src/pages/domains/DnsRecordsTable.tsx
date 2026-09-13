import type { DnsCheck, DnsRecord } from '@/api/domains';
import { Badge, CopyButton, DataTable, type Column } from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';

export interface DnsRecordsTableProps {
  records: DnsRecord[];
  /** Ultima comprobacion de cada registro; sin ella no se muestra esa columna. */
  checks?: DnsCheck[];
}

export function DnsRecordsTable({ records, checks }: DnsRecordsTableProps) {
  const byRecord = new Map((checks ?? []).map((check) => [check.record, check]));

  const columns: Column<DnsRecord>[] = [
    {
      key: 'record',
      header: t('domains.records.column.record'),
      render: (r) => (
        <div className="cf-cell-stack">
          <strong>{tEnum('domains.record', r.record)}</strong>
          {r.required ? (
            <Badge tone="accent">{t('domains.records.required')}</Badge>
          ) : (
            <Badge>{t('domains.records.recommended')}</Badge>
          )}
        </div>
      ),
    },
    {
      key: 'type',
      header: t('domains.records.column.type'),
      render: (r) => <span className="cf-mono">{r.type}</span>,
    },
    {
      key: 'host',
      header: t('domains.records.column.host'),
      render: (r) => <CopyableValue value={r.host} label={t('domains.records.copyHost')} />,
    },
    {
      key: 'value',
      header: t('domains.records.column.value'),
      render: (r) => <CopyableValue value={r.value} label={t('domains.records.copyValue')} />,
    },
    ...(checks
      ? [
          {
            key: 'check',
            header: t('domains.records.column.check'),
            render: (r: DnsRecord) => <CheckCell check={byRecord.get(r.record)} />,
          },
        ]
      : []),
  ];

  return (
    <DataTable
      columns={columns}
      rows={records}
      rowKey={(r) => r.record}
      empty={{ title: t('domains.records.empty') }}
    />
  );
}

function CopyableValue({ value, label }: { value: string; label: string }) {
  return (
    <div className="cf-copy-cell">
      <span className="cf-mono cf-break cf-text-sm">{value}</span>
      <CopyButton value={value} label={label} />
    </div>
  );
}

function CheckCell({ check }: { check: DnsCheck | undefined }) {
  if (!check) return <span className="cf-text-muted">{t('domains.records.notChecked')}</span>;
  return (
    <div className="cf-cell-stack">
      {check.ok ? (
        <Badge tone="success">{t('domains.records.ok')}</Badge>
      ) : (
        <Badge tone="danger">{t('domains.records.failed')}</Badge>
      )}
      {check.detail ? <span className="cf-text-sm cf-text-secondary">{check.detail}</span> : null}
      {!check.ok && check.observed ? (
        <span className="cf-text-sm">
          {t('domains.records.observed')} <span className="cf-mono cf-break">{check.observed}</span>
        </span>
      ) : null}
      <span className="cf-text-muted cf-text-sm">{formatDateTime(check.checked_at)}</span>
    </div>
  );
}
