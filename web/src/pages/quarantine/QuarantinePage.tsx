import { useState, type FormEvent } from 'react';
import { mailSecurityApi, type QuarantineItem } from '@/api/mailSecurity';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import {
  Badge,
  Button,
  Card,
  DataTable,
  Input,
  PageHeader,
  type Column,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import { isDecimal } from '@/pages/mailSecurity/policyObject';
import { QuarantineDetail } from './QuarantineDetail';

interface Filters {
  rcpt: string;
  scoreMin: string;
}

const EMPTY: Filters = { rcpt: '', scoreMin: '' };

export default function QuarantinePage() {
  const pager = usePagination();
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);
  const [scoreError, setScoreError] = useState<string | null>(null);
  const [selected, setSelected] = useState<QuarantineItem | null>(null);

  const items = useQuery(
    () =>
      mailSecurityApi.listQuarantine({
        page: pager.page,
        per_page: pager.perPage,
        rcpt: applied.rcpt.trim().toLowerCase() || undefined,
        score_min: applied.scoreMin.trim() || undefined,
      }),
    [pager.page, pager.perPage, applied],
  );

  const apply = (e: FormEvent) => {
    e.preventDefault();
    const invalid = draft.scoreMin.trim() !== '' && !isDecimal(draft.scoreMin);
    setScoreError(invalid ? t('validation.decimal') : null);
    if (invalid) return;
    setApplied(draft);
    pager.reset();
  };

  const columns: Column<QuarantineItem>[] = [
    {
      key: 'when',
      header: t('quarantine.column.when'),
      render: (q) => formatDateTime(q.created_at),
    },
    {
      key: 'sender',
      header: t('quarantine.column.sender'),
      render: (q) => <span className="cf-mono cf-break">{q.sender}</span>,
    },
    {
      key: 'rcpt',
      header: t('quarantine.column.rcpt'),
      render: (q) => <span className="cf-mono cf-break">{q.rcpt}</span>,
    },
    {
      key: 'subject',
      header: t('quarantine.column.subject'),
      render: (q) => <span className="cf-break">{q.subject}</span>,
    },
    {
      key: 'score',
      header: t('quarantine.column.score'),
      align: 'right',
      render: (q) => <Badge tone="warning">{q.score}</Badge>,
    },
    { key: 'action', header: t('quarantine.column.action'), render: (q) => q.action },
    {
      key: 'size',
      header: t('quarantine.column.size'),
      align: 'right',
      render: (q) => formatBytes(q.size),
    },
  ];

  return (
    <div>
      <PageHeader title={t('quarantine.title')} description={t('quarantine.subtitle')} />
      <Card flush>
        <form className="cf-toolbar" onSubmit={apply}>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="quarantine-rcpt">
              {t('quarantine.filter.rcpt')}
            </label>
            <Input
              id="quarantine-rcpt"
              className="cf-mono"
              value={draft.rcpt}
              onChange={(e) => setDraft((d) => ({ ...d, rcpt: e.target.value }))}
            />
          </div>
          <div className="cf-field">
            <label className="cf-field__label" htmlFor="quarantine-score">
              {t('quarantine.filter.scoreMin')}
            </label>
            <Input
              id="quarantine-score"
              inputMode="decimal"
              value={draft.scoreMin}
              onChange={(e) => setDraft((d) => ({ ...d, scoreMin: e.target.value }))}
              invalid={Boolean(scoreError)}
              aria-describedby={scoreError ? 'quarantine-score-error' : undefined}
            />
            {scoreError ? (
              <span className="cf-field__error" id="quarantine-score-error" role="alert">
                {scoreError}
              </span>
            ) : null}
          </div>
          <div className="cf-toolbar__actions">
            <Button
              onClick={() => {
                setDraft(EMPTY);
                setApplied(EMPTY);
                setScoreError(null);
                pager.reset();
              }}
            >
              {t('common.clear')}
            </Button>
            <Button type="submit" variant="primary">
              {t('common.apply')}
            </Button>
          </div>
        </form>
        <DataTable
          columns={columns}
          rows={items.data?.items ?? []}
          rowKey={(q) => q.id}
          loading={items.loading}
          error={items.error}
          onRetry={items.reload}
          empty={{ title: t('quarantine.empty') }}
          onRowClick={setSelected}
          pagination={{
            page: items.data?.page ?? pager.page,
            perPage: pager.perPage,
            total: items.data?.total ?? 0,
            totalPages: items.data?.totalPages ?? 0,
            onPageChange: pager.setPage,
          }}
        />
      </Card>
      {selected ? (
        <QuarantineDetail
          item={selected}
          onClose={() => setSelected(null)}
          onRemoved={() => {
            setSelected(null);
            items.reload();
          }}
        />
      ) : null}
    </div>
  );
}
