import { useMemo } from 'react';
import {
  mailSecurityApi,
  RSPAMD_HISTORY_MAX_ROWS,
  type RspamdHistoryRow,
  type RspamdStatfile,
  type RspamdStats,
} from '@/api/mailSecurity';
import { isApiError, ERROR_CODES } from '@/api/errors';
import { useQuery } from '@/hooks/useQuery';
import { useTabParam } from '@/hooks/useTabParam';
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  DescriptionList,
  PageHeader,
  Tabs,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t } from '@/i18n';

const TABS = ['stats', 'history'] as const;

const ACTION_TONES: Record<string, BadgeTone> = {
  reject: 'danger',
  'soft reject': 'warning',
  greylist: 'warning',
  'add header': 'warning',
  'rewrite subject': 'warning',
  'no action': 'success',
};

const integer = () => new Intl.NumberFormat();

/**
 * Antispam de la celda: lectura del controller de Rspamd, solo para el superadmin (permiso de
 * plataforma mail_security/rspamd/read). Nada de aqui escribe en el controller.
 */
export default function RspamdPage() {
  const [tab, setTab] = useTabParam(TABS, 'stats');
  const stats = useQuery((signal) => mailSecurityApi.rspamdStats(signal), []);
  const history = useQuery(
    (signal) =>
      tab === 'history'
        ? mailSecurityApi.rspamdHistory(RSPAMD_HISTORY_MAX_ROWS, signal)
        : Promise.resolve(null),
    [tab],
  );
  const notConfigured = isApiError(stats.error) && stats.error.code === ERROR_CODES.NOT_CONFIGURED;
  const reload = () => {
    stats.reload();
    if (tab === 'history') history.reload();
  };

  return (
    <div>
      <PageHeader
        title={t('rspamd.title')}
        description={t('rspamd.subtitle')}
        actions={
          <Button onClick={reload} disabled={stats.loading || notConfigured}>
            <IconRefresh size={16} />
            {t('common.refresh')}
          </Button>
        }
      />
      {notConfigured ? (
        <Alert tone="warning">{t('rspamd.notConfigured')}</Alert>
      ) : (
        <>
          <Tabs
            items={TABS.map((id) => ({ id, label: t(`rspamd.tab.${id}`) }))}
            value={tab}
            onChange={setTab}
            label={t('rspamd.tabs')}
          />
          {tab === 'stats' ? (
            <StatsTab stats={stats.data} loading={stats.loading} error={stats.error} onRetry={stats.reload} />
          ) : (
            <HistoryTab
              rows={history.data?.rows ?? []}
              total={history.data?.total ?? 0}
              truncated={history.data?.truncated ?? false}
              loading={history.loading}
              error={history.error}
              onRetry={history.reload}
            />
          )}
        </>
      )}
    </div>
  );
}

function StatsTab({
  stats,
  loading,
  error,
  onRetry,
}: {
  stats: RspamdStats | null;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  const actionRows = useMemo(
    () =>
      Object.entries(stats?.actions ?? {})
        .map(([action, count]) => ({ action, count }))
        .sort((a, b) => b.count - a.count),
    [stats],
  );
  const actionColumns: Column<{ action: string; count: number }>[] = [
    {
      key: 'action',
      header: t('rspamd.column.action'),
      render: (r) => <Badge tone={ACTION_TONES[r.action] ?? 'neutral'}>{r.action}</Badge>,
    },
    { key: 'count', header: t('rspamd.column.count'), align: 'right', render: (r) => integer().format(r.count) },
  ];
  const bayesColumns: Column<RspamdStatfile>[] = [
    { key: 'symbol', header: t('rspamd.column.statfile'), render: (f) => <span className="cf-mono">{f.symbol}</span> },
    { key: 'total', header: t('rspamd.column.total'), align: 'right', render: (f) => integer().format(f.total) },
    { key: 'used', header: t('rspamd.column.used'), align: 'right', render: (f) => integer().format(f.used) },
    { key: 'revision', header: t('rspamd.column.revision'), align: 'right', render: (f) => integer().format(f.revision) },
  ];
  const fuzzy = Object.entries(stats?.fuzzy_hashes ?? {});
  return (
    <div className="cf-stack">
      <div className="cf-grid-cards">
        <Card>
          <div className="cf-stat" style={{ padding: 0 }}>
            <div className="cf-stat__label">{t('rspamd.stat.scanned')}</div>
            <div className="cf-stat__value">{stats ? integer().format(stats.scanned) : t('common.dash')}</div>
          </div>
        </Card>
        <Card>
          <div className="cf-stat" style={{ padding: 0 }}>
            <div className="cf-stat__label">{t('rspamd.stat.spam')}</div>
            <div className="cf-stat__value">{stats ? integer().format(stats.spam_count) : t('common.dash')}</div>
          </div>
        </Card>
        <Card>
          <div className="cf-stat" style={{ padding: 0 }}>
            <div className="cf-stat__label">{t('rspamd.stat.ham')}</div>
            <div className="cf-stat__value">{stats ? integer().format(stats.ham_count) : t('common.dash')}</div>
          </div>
        </Card>
        <Card>
          <div className="cf-stat" style={{ padding: 0 }}>
            <div className="cf-stat__label">{t('rspamd.stat.learned')}</div>
            <div className="cf-stat__value">{stats ? integer().format(stats.learned) : t('common.dash')}</div>
          </div>
        </Card>
      </div>
      <Card>
        <DescriptionList
          items={[
            { label: t('rspamd.stat.version'), value: stats?.version ?? t('common.dash') },
            {
              label: t('rspamd.stat.uptime'),
              value: stats ? t('rspamd.stat.hours', { n: Math.floor(stats.uptime_seconds / 3600) }) : t('common.dash'),
            },
            {
              label: t('rspamd.stat.scanTime'),
              value: stats
                ? t('rspamd.stat.scanTimeValue', {
                    avg: stats.scan_time.average_ms,
                    max: stats.scan_time.max_ms,
                    n: stats.scan_time.samples,
                  })
                : t('common.dash'),
            },
            { label: t('rspamd.stat.connections'), value: stats ? integer().format(stats.connections) : t('common.dash') },
            {
              label: t('rspamd.stat.fuzzy'),
              value:
                fuzzy.length === 0
                  ? t('rspamd.stat.fuzzyEmpty')
                  : fuzzy.map(([storage, n]) => `${storage}: ${integer().format(n)}`).join(', '),
            },
          ]}
        />
      </Card>
      <div className="cf-split">
        <Card flush>
          <div className="cf-stat">
            <div className="cf-stat__label">{t('rspamd.stat.actions')}</div>
          </div>
          <DataTable
            columns={actionColumns}
            rows={actionRows}
            rowKey={(r) => r.action}
            loading={loading}
            error={error}
            onRetry={onRetry}
            empty={{ title: t('common.none') }}
          />
        </Card>
        <Card flush>
          <div className="cf-stat">
            <div className="cf-stat__label">{t('rspamd.stat.bayes')}</div>
          </div>
          <DataTable
            columns={bayesColumns}
            rows={stats?.statfiles ?? []}
            rowKey={(f) => f.symbol}
            loading={loading}
            error={error}
            onRetry={onRetry}
            empty={{ title: t('common.none') }}
          />
        </Card>
      </div>
    </div>
  );
}

function HistoryTab({
  rows,
  total,
  truncated,
  loading,
  error,
  onRetry,
}: {
  rows: RspamdHistoryRow[];
  total: number;
  truncated: boolean;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  const columns: Column<RspamdHistoryRow>[] = [
    { key: 'time', header: t('rspamd.column.time'), render: (r) => formatDateTime(r.time) },
    {
      key: 'action',
      header: t('rspamd.column.action'),
      render: (r) => (
        <Badge tone={r.skipped ? 'neutral' : (ACTION_TONES[r.action] ?? 'neutral')}>
          {r.skipped ? t('rspamd.history.skipped') : r.action}
        </Badge>
      ),
    },
    {
      key: 'score',
      header: t('rspamd.column.score'),
      align: 'right',
      render: (r) => (
        <span className="cf-mono">
          {r.score} / {r.required_score}
        </span>
      ),
    },
    { key: 'sender', header: t('rspamd.column.sender'), render: (r) => <span className="cf-mono cf-break">{r.sender}</span> },
    {
      key: 'recipients',
      header: t('rspamd.column.recipients'),
      render: (r) => (
        <span className="cf-mono cf-break">
          {r.recipients[0] ?? ''}
          {r.recipients.length > 1 ? ` ${t('rspamd.history.moreRecipients', { n: r.recipients.length - 1 })}` : ''}
        </span>
      ),
    },
    {
      key: 'subject',
      header: t('rspamd.column.subject'),
      render: (r) => <span className="cf-break">{r.subject || t('rspamd.history.noSubject')}</span>,
    },
    {
      key: 'symbols',
      header: t('rspamd.column.symbols'),
      render: (r) => (
        <span className="cf-mono cf-text-sm cf-break" aria-label={t('rspamd.history.symbolsFor', { id: r.id })}>
          {r.symbols.map((s) => `${s.name}(${s.score})`).join(' ')}
        </span>
      ),
    },
  ];
  return (
    <Card flush>
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={{ title: t('rspamd.history.empty') }}
      />
      {truncated ? (
        <p className="cf-text-sm cf-text-secondary">
          {t('rspamd.history.showing', { shown: rows.length, total })}
        </p>
      ) : null}
    </Card>
  );
}
