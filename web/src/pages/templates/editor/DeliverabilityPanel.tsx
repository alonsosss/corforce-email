import type { CheckIssue, CheckResult } from '@/api/templates';
import { errorMessage } from '@/api/messages';
import { Alert, Badge, DescriptionList, Skeleton } from '@/design/components';
import { formatBytes } from '@/lib/quota';
import { hasMessage, t } from '@/i18n';
import type { DeliverabilityState } from './useDeliverabilityCheck';

/** Titulo del codigo si la aplicacion lo conoce; el mensaje del servidor explica el caso. */
function issueTitle(code: string): string {
  const key = `templates.check.issue.${code}`;
  return hasMessage(key) ? t(key) : code;
}

function IssueList({ issues }: { issues: CheckIssue[] }) {
  const ordered = [...issues].sort((a, b) =>
    a.severity === b.severity ? 0 : a.severity === 'error' ? -1 : 1,
  );
  return (
    <ul className="cf-check-list">
      {ordered.map((issue) => (
        <li key={`${issue.code}-${issue.severity}`} className="cf-check-item">
          <Badge tone={issue.severity === 'error' ? 'danger' : 'warning'}>
            {issue.severity === 'error' ? t('templates.check.error') : t('templates.check.warning')}
          </Badge>
          <div>
            <div className="cf-check-item__title">
              {issueTitle(issue.code)}
              {issue.count && issue.count > 1 ? ` (${issue.count})` : ''}
            </div>
            <div className="cf-text-sm cf-text-secondary">{issue.message}</div>
          </div>
        </li>
      ))}
    </ul>
  );
}

function Stats({ result }: { result: CheckResult }) {
  const { stats, spam } = result;
  return (
    <DescriptionList
      items={[
        { label: t('templates.check.htmlSize'), value: formatBytes(stats.html_bytes) },
        { label: t('templates.check.textChars'), value: String(stats.text_chars) },
        { label: t('templates.check.images'), value: String(stats.images) },
        { label: t('templates.check.links'), value: String(stats.links) },
        {
          label: t('templates.check.textRatio'),
          value: Number.isFinite(stats.text_image_ratio)
            ? stats.text_image_ratio.toFixed(2)
            : t('common.dash'),
        },
        {
          label: t('templates.check.spamScore'),
          value:
            spam.available && spam.score !== undefined
              ? t('templates.check.spamValue', {
                  score: spam.score.toFixed(1),
                  required: spam.required?.toFixed(1) ?? t('common.dash'),
                })
              : t('templates.check.spamUnavailable'),
        },
      ]}
    />
  );
}

export interface DeliverabilityPanelProps {
  state: DeliverabilityState;
  /** Issues del 409 al publicar: la version guardada no paso la verificacion. */
  publishIssues: CheckIssue[] | null;
  compileWarnings: string[];
}

export function DeliverabilityPanel({
  state,
  publishIssues,
  compileWarnings,
}: DeliverabilityPanelProps) {
  const { result } = state;
  const symbols = result?.spam.available ? (result.spam.symbols ?? []) : [];
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <span className="cf-text-sm cf-text-secondary">{t('templates.check.hint')}</span>
      {publishIssues ? (
        <Alert tone="danger" title={t('templates.check.publishBlocked')}>
          <IssueList issues={publishIssues} />
        </Alert>
      ) : null}
      {compileWarnings.length ? (
        <Alert tone="warning" title={t('templates.check.mjmlWarnings')}>
          <ul className="cf-check-list">
            {compileWarnings.map((warning) => (
              <li key={warning} className="cf-text-sm">
                {warning}
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {state.error ? <Alert tone="danger">{errorMessage(state.error)}</Alert> : null}
      {!result ? (
        state.checking ? (
          <Skeleton lines={4} />
        ) : (
          <span className="cf-text-sm cf-text-secondary">{t('templates.check.pending')}</span>
        )
      ) : (
        <>
          <div className="cf-inline">
            <Badge tone={result.passed ? 'success' : 'danger'}>
              {result.passed ? t('templates.check.passed') : t('templates.check.failed')}
            </Badge>
            {state.checking ? (
              <span className="cf-text-sm cf-text-secondary">{t('templates.check.checking')}</span>
            ) : null}
          </div>
          {result.issues.length ? (
            <IssueList issues={result.issues} />
          ) : (
            <span className="cf-text-sm cf-text-secondary">{t('templates.check.noIssues')}</span>
          )}
          <Stats result={result} />
          {symbols.length ? (
            <div className="cf-field">
              <span className="cf-field__label">{t('templates.check.spamSymbols')}</span>
              <ul className="cf-check-list">
                {[...symbols]
                  .sort((a, b) => b.score - a.score)
                  .map((symbol) => (
                    <li key={symbol.name} className="cf-text-sm">
                      <code className="cf-mono">{symbol.name}</code> {symbol.score.toFixed(2)}
                      {symbol.description ? ` · ${symbol.description}` : ''}
                    </li>
                  ))}
              </ul>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
