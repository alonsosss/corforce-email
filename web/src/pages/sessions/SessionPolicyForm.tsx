import { useEffect, useState, type FormEvent } from 'react';
import { identityApi } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Button,
  Card,
  ErrorState,
  FormField,
  Input,
  Skeleton,
  useToast,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { hasErrors, rules, validateField, type FieldErrors } from '@/lib/validate';
import { t } from '@/i18n';

// Limites que valida identity (domain.SessionPolicy.Validate, texto del handler).
const LIMITS = {
  refreshTtlHours: { min: 1, max: 8760 },
  maxConcurrent: { min: 0, max: 100 },
  idleMinutes: { min: 0, max: 43200 },
} as const;

type Field = 'refresh' | 'concurrent' | 'idle';

export function SessionPolicyForm() {
  const toast = useToast();
  const { can } = useAccess();
  const policy = useQuery(async () => (await identityApi.getSessionPolicy()).data, []);
  const [refresh, setRefresh] = useState('');
  const [concurrent, setConcurrent] = useState('');
  const [idle, setIdle] = useState('');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  useEffect(() => {
    if (policy.data) {
      setRefresh(String(policy.data.refresh_ttl_hours));
      setConcurrent(String(policy.data.max_concurrent_sessions));
      setIdle(String(policy.data.idle_timeout_minutes));
    }
  }, [policy.data]);

  const save = useAction(async () => {
    const { data } = await identityApi.saveSessionPolicy({
      refresh_ttl_hours: Number(refresh),
      max_concurrent_sessions: Number(concurrent),
      idle_timeout_minutes: Number(idle),
    });
    policy.setData(data);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next: FieldErrors<Field> = {
      refresh:
        validateField(
          refresh,
          rules.required,
          rules.range(LIMITS.refreshTtlHours.min, LIMITS.refreshTtlHours.max),
        ) ?? undefined,
      concurrent:
        validateField(
          concurrent,
          rules.required,
          rules.range(LIMITS.maxConcurrent.min, LIMITS.maxConcurrent.max),
        ) ?? undefined,
      idle:
        validateField(
          idle,
          rules.required,
          rules.range(LIMITS.idleMinutes.min, LIMITS.idleMinutes.max),
        ) ?? undefined,
    };
    setErrors(next);
    if (hasErrors(next)) return;
    if (await save.run()) toast.success(t('sessions.policy.saved'));
  };

  const editable = can(...PERMISSIONS.sessionPolicies.update);

  return (
    <Card title={t('sessions.policy.title')} description={t('sessions.policy.description')}>
      {policy.error ? (
        <ErrorState error={policy.error} onRetry={policy.reload} />
      ) : !policy.data ? (
        <Skeleton lines={4} />
      ) : (
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <div className="cf-form__row">
            <FormField
              label={t('sessions.policy.refreshTtl')}
              htmlFor="policy-refresh"
              hint={t('sessions.policy.refreshTtlHint')}
              error={errors.refresh}
              required
            >
              <Input
                id="policy-refresh"
                type="number"
                min={LIMITS.refreshTtlHours.min}
                max={LIMITS.refreshTtlHours.max}
                value={refresh}
                onChange={(e) => setRefresh(e.target.value)}
                invalid={Boolean(errors.refresh)}
                disabled={!editable}
              />
            </FormField>
            <FormField
              label={t('sessions.policy.maxConcurrent')}
              htmlFor="policy-concurrent"
              hint={t('sessions.policy.maxConcurrentHint')}
              error={errors.concurrent}
              required
            >
              <Input
                id="policy-concurrent"
                type="number"
                min={LIMITS.maxConcurrent.min}
                max={LIMITS.maxConcurrent.max}
                value={concurrent}
                onChange={(e) => setConcurrent(e.target.value)}
                invalid={Boolean(errors.concurrent)}
                disabled={!editable}
              />
            </FormField>
            <FormField
              label={t('sessions.policy.idleTimeout')}
              htmlFor="policy-idle"
              hint={t('sessions.policy.idleTimeoutHint')}
              error={errors.idle}
              required
            >
              <Input
                id="policy-idle"
                type="number"
                min={LIMITS.idleMinutes.min}
                max={LIMITS.idleMinutes.max}
                value={idle}
                onChange={(e) => setIdle(e.target.value)}
                invalid={Boolean(errors.idle)}
                disabled={!editable}
              />
            </FormField>
          </div>
          <p className="cf-text-muted cf-text-sm">
            {t('sessions.policy.updatedAt')}: {formatDateTime(policy.data.updated_at)}
          </p>
          {save.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(save.error)}
            </div>
          ) : null}
          {editable ? (
            <div className="cf-form__actions">
              <Button type="submit" variant="primary" loading={save.busy}>
                {t('common.save')}
              </Button>
            </div>
          ) : null}
        </form>
      )}
    </Card>
  );
}
