import { useEffect, useState } from 'react';
import {
  auditApi,
  VERIFICATION_RUNNING,
  type ChainIntegrity,
  type IntegrityRun,
  type IntegrityRunMode,
} from '@/api/audit';
import { errorCode, errorDetail } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  DescriptionList,
  EmptyState,
  ErrorState,
  LoadingBlock,
  Meter,
  PageHeader,
} from '@/design/components';
import { IconAlertTriangle, IconCheckCircle, IconLink } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';

/** Cada cuanto se consulta el avance de una verificacion en curso. */
export const POLL_INTERVAL_MS = 3000;

/** Avance de la fase de audit_logs (la larga): posicion alcanzada sobre la de la cabeza al empezar. */
function progressRatio(run: IntegrityRun): number {
  if (run.phase !== 'audit_logs') return 1;
  return run.target_seq > 0 ? run.current_seq / run.target_seq : 0;
}

function RunningView({ run, onCancel, cancelling }: RunningViewProps) {
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <EmptyState
        icon={<IconLink size={36} />}
        title={t('audit.integrity.running')}
        description={
          <>
            <p>{tEnum('audit.integrity.phase', run.phase)}</p>
            <p>{t('audit.integrity.checked', { n: run.checked })}</p>
            <p>{t('audit.integrity.runningDescription')}</p>
          </>
        }
      />
      <Meter ratio={progressRatio(run)} label={t('audit.integrity.progress')} />
      <div>
        <Button
          loading={cancelling}
          disabled={run.cancel_requested}
          onClick={() => void onCancel()}
        >
          {run.cancel_requested ? t('audit.integrity.cancelling') : t('audit.integrity.cancel')}
        </Button>
      </div>
    </div>
  );
}

interface RunningViewProps {
  run: IntegrityRun;
  onCancel: () => Promise<boolean>;
  cancelling: boolean;
}

function BrokenView({ result, checked }: { result: ChainIntegrity; checked: number }) {
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <EmptyState
        icon={
          <span style={{ color: 'var(--cf-danger)' }}>
            <IconAlertTriangle size={36} />
          </span>
        }
        title={t('audit.integrity.broken')}
        description={
          result.reason
            ? tEnum('audit.integrity.reason', result.reason)
            : t('audit.integrity.brokenDescription')
        }
      />
      <DescriptionList
        items={[
          { label: t('audit.integrity.checked', { n: checked }), value: '' },
          {
            label: t('audit.integrity.chainLabel'),
            value: tEnum('audit.integrity.chain', result.chain),
          },
          {
            label: t('audit.integrity.brokenId'),
            value: <span className="cf-mono">{result.broken_id ?? t('common.dash')}</span>,
          },
          {
            label: t('audit.integrity.brokenSeq'),
            value: <span className="cf-mono">{result.broken_seq ?? t('common.dash')}</span>,
          },
        ]}
      />
    </div>
  );
}

function OkView({ checked }: { checked: number }) {
  return (
    <EmptyState
      icon={
        <span style={{ color: 'var(--cf-success)' }}>
          <IconCheckCircle size={36} />
        </span>
      }
      title={t('audit.integrity.ok')}
      description={
        <>
          <p>{t('audit.integrity.okDescription')}</p>
          <p>{t('audit.integrity.checked', { n: checked })}</p>
        </>
      }
    />
  );
}

function RunOutcome({ run }: { run: IntegrityRun }) {
  if (run.status === 'cancelled') {
    return (
      <Alert tone="info" title={t('audit.integrity.cancelled')}>
        {t('audit.integrity.cancelledDescription', { n: run.checked })}
      </Alert>
    );
  }
  if (run.status === 'failed') {
    return (
      <Alert tone="danger" title={t('audit.integrity.failed')}>
        <p>{t('audit.integrity.failedDescription')}</p>
        {run.error ? <p>{tEnum('audit.integrity.error', run.error)}</p> : null}
      </Alert>
    );
  }
  if (run.result?.ok) return <OkView checked={run.checked} />;
  if (run.result) return <BrokenView result={run.result} checked={run.checked} />;
  return null;
}

function RunDetails({ run }: { run: IntegrityRun }) {
  return (
    <DescriptionList
      items={[
        { label: t('audit.integrity.mode'), value: tEnum('audit.integrity.mode', run.mode) },
        {
          label: t('audit.integrity.trigger'),
          value: tEnum('audit.integrity.trigger', run.trigger),
        },
        { label: t('audit.integrity.startedAt'), value: formatDateTime(run.started_at) },
        { label: t('audit.integrity.finishedAt'), value: formatDateTime(run.finished_at) },
        {
          label: t('audit.integrity.requestedBy'),
          value: <span className="cf-mono">{run.requested_by ?? t('common.dash')}</span>,
        },
      ]}
    />
  );
}

export default function IntegrityPage() {
  const { can } = useAccess();
  const canVerify = can(...PERMISSIONS.integrity.verify);

  // La verificacion mas reciente de la empresa: la que corre o la ultima terminada.
  const latest = useQuery<IntegrityRun | null>(
    async (signal) => {
      if (!canVerify) return null;
      const { data } = await auditApi.listIntegrityRuns(signal);
      return data[0] ?? null;
    },
    [canVerify],
  );
  const run = latest.data;
  const { setData: setRun } = latest;

  // Mientras corre, se consulta su avance. Cada respuesta cambia `run` y programa la siguiente
  // consulta; un fallo de red no la detiene: se reintenta y se avisa.
  const runningId = run?.status === 'running' ? run.id : null;
  const [pollError, setPollError] = useState<unknown>(null);
  const [pollFailures, setPollFailures] = useState(0);
  useEffect(() => {
    if (!runningId) return;
    let cancelled = false;
    const timer = setTimeout(() => {
      auditApi
        .integrityRun(runningId)
        .then(({ data }) => {
          if (cancelled) return;
          setPollError(null);
          setRun(data);
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          setPollError(err);
          setPollFailures((n) => n + 1);
        });
    }, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [runningId, run, pollFailures, setRun]);

  const start = useAction(async (mode: IntegrityRunMode) => {
    try {
      const { data } = await auditApi.startIntegrityRun(mode);
      setRun(data);
    } catch (err) {
      // Otro lanzamiento ya la puso en marcha: se sigue esa en lugar de mostrar un error.
      const existing = errorCode(err) === VERIFICATION_RUNNING ? errorDetail(err, 'run_id') : null;
      if (!existing) throw err;
      const { data } = await auditApi.integrityRun(existing);
      setRun(data);
    }
  });
  const cancel = useAction(async (id: string) => {
    const { data } = await auditApi.cancelIntegrityRun(id);
    setRun(data);
  });

  const running = run?.status === 'running';

  return (
    <div>
      <PageHeader
        title={t('audit.integrity.title')}
        description={t('audit.integrity.subtitle')}
        actions={
          canVerify ? (
            <>
              <Button
                variant="primary"
                icon={<IconLink size={16} />}
                loading={start.busy}
                disabled={running}
                onClick={() => void start.run('full')}
              >
                {t('audit.integrity.verify')}
              </Button>
              <Button
                loading={start.busy}
                disabled={running}
                title={t('audit.integrity.verifyNewHint')}
                onClick={() => void start.run('incremental')}
              >
                {t('audit.integrity.verifyNew')}
              </Button>
            </>
          ) : null
        }
      />
      <Card>
        {!canVerify ? (
          <EmptyState icon={<IconLink size={32} />} title={t('audit.integrity.noVerify')} />
        ) : latest.loading && run === null ? (
          <LoadingBlock />
        ) : latest.error && run === null ? (
          <ErrorState error={latest.error} onRetry={latest.reload} />
        ) : (
          <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
            {start.error ? (
              <div className="cf-form__error" role="alert">
                {errorMessage(start.error)}
              </div>
            ) : null}
            {run === null ? (
              <EmptyState icon={<IconLink size={32} />} title={t('audit.integrity.idle')} />
            ) : running ? (
              <RunningView run={run} cancelling={cancel.busy} onCancel={() => cancel.run(run.id)} />
            ) : (
              <RunOutcome run={run} />
            )}
            {pollError ? <Alert tone="warning">{t('audit.integrity.pollError')}</Alert> : null}
            {cancel.error ? (
              <div className="cf-form__error" role="alert">
                {errorMessage(cancel.error)}
              </div>
            ) : null}
          </div>
        )}
      </Card>
      {canVerify && run ? (
        <Card title={t('audit.integrity.lastRun')}>
          <RunDetails run={run} />
        </Card>
      ) : null}
    </div>
  );
}
