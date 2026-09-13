import { useMemo, useState } from 'react';
import {
  schedulerApi,
  schedulerHandlers,
  schedulerMeta,
  type JobType,
  type SchedulerHandler,
  type SchedulerJob,
  type SchedulerMeta,
} from '@/api/scheduler';
import { useAction } from '@/hooks/useAction';
import { useResource } from '@/hooks/useResource';
import { Alert, FormField, Input, Select, Textarea } from '@/design/components';
import { formatBytes } from '@/lib/quota';
import { hasErrors } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import {
  draftFromJob,
  emptyDraft,
  maxTimeoutSeconds,
  serverFieldErrors,
  tenantHandlers,
  timeZoneSuggestions,
  toCreateRequest,
  toUpdateRequest,
  validateDraft,
  type JobDraft,
  type JobErrors,
  type JobField,
} from './jobDraft';
import { formatSeconds } from './schedulerFormat';

export interface JobFormProps {
  /** null en el alta. */
  job: SchedulerJob | null;
  onClose: () => void;
  onSaved: (job: SchedulerJob) => void;
}

const FORM_ID = 'scheduler-job-form';
const TIMEZONE_LIST_ID = 'job-timezone-suggestions';

const describedBy = (id: string, error: string | undefined) => (error ? `${id}-error` : undefined);

/** Alta y edicion de un trabajo con las reglas de GET /scheduler/meta y el catalogo del API. */
export function JobForm(props: JobFormProps) {
  const meta = useResource(schedulerMeta);
  const handlers = useResource(schedulerHandlers);
  const title = props.job ? t('scheduler.form.editTitle') : t('scheduler.form.createTitle');
  const modal = { title, onClose: props.onClose };
  return (
    <ResourceGate resource={meta} modal={modal}>
      {(m) => (
        <ResourceGate resource={handlers} modal={modal}>
          {(h) => <Form {...props} meta={m} handlers={h} title={title} />}
        </ResourceGate>
      )}
    </ResourceGate>
  );
}

interface FormProps extends JobFormProps {
  meta: SchedulerMeta;
  handlers: SchedulerHandler[];
  title: string;
}

function Form({ job, onClose, onSaved, meta, handlers, title }: FormProps) {
  const [draft, setDraft] = useState<JobDraft>(() => (job ? draftFromJob(job) : emptyDraft(meta)));
  const [errors, setErrors] = useState<JobErrors>({});
  // Campos tocados desde el ultimo envio: su error del servidor ya no aplica.
  const [touched, setTouched] = useState<ReadonlySet<JobField>>(() => new Set());
  const available = useMemo(() => tenantHandlers(handlers), [handlers]);
  const zones = useMemo(() => timeZoneSuggestions(meta.timezone.default), [meta.timezone.default]);
  const selected = available.find((h) => h.name === draft.handler) ?? null;
  const catalogEmpty = available.length === 0;

  const action = useAction(async (current: JobDraft, jobType: JobType) => {
    const { data } = job
      ? await schedulerApi.updateJob(job.id, toUpdateRequest(current, jobType))
      : await schedulerApi.createJob(toCreateRequest(current, jobType));
    onSaved(data);
  });
  const fromServer = useMemo(() => serverFieldErrors(action.error), [action.error]);
  const errorOf = (field: JobField): string | undefined =>
    errors[field] ?? (touched.has(field) ? undefined : fromServer[field]);

  const update = <K extends JobField>(field: K, value: JobDraft[K]) => {
    setDraft((d) => ({ ...d, [field]: value }));
    setErrors((e) => ({ ...e, [field]: undefined }));
    setTouched((s) => new Set(s).add(field));
  };

  const submit = async () => {
    action.clearError();
    setTouched(new Set());
    const next = validateDraft(draft, meta, job ? 'edit' : 'create', selected);
    setErrors(next);
    if (hasErrors(next) || !draft.job_type) return;
    await action.run(draft, draft.job_type);
  };

  const handlerOptions = available.map((h) => ({
    value: h.name,
    label: t('scheduler.handler.option', { name: h.name, service: h.service }),
  }));
  // Un trabajo cuyo manejador salio del catalogo lo conserva visible: cambiarlo en silencio
  // seria editar algo que nadie pidio. Si se guarda tal cual, el servidor lo rechaza.
  if (job && !available.some((h) => h.name === job.handler)) {
    handlerOptions.unshift({
      value: job.handler,
      label: t('scheduler.handler.optionOutOfCatalog', { name: job.handler }),
    });
  }
  const handlerHint = selected
    ? [
        selected.description,
        t('scheduler.handler.maxTimeout', { value: formatSeconds(selected.max_timeout_seconds) }),
      ]
        .filter(Boolean)
        .join(' ')
    : draft.handler
      ? t('scheduler.handler.outOfCatalogHint')
      : undefined;
  const { limits } = meta;
  const timeoutHint = selected
    ? t('scheduler.form.timeoutHintMax', {
        value: formatSeconds(maxTimeoutSeconds(meta, selected)),
      })
    : t('scheduler.form.timeoutHint', { max: formatSeconds(limits.max_timeout_seconds) });

  return (
    <FormModal
      id={FORM_ID}
      title={title}
      submitLabel={job ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={hasErrors(fromServer) ? null : action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
      submitDisabled={catalogEmpty}
    >
      {catalogEmpty ? (
        <Alert tone="warning" title={t('scheduler.catalogEmpty.title')}>
          {t('scheduler.catalogEmpty.body')}
        </Alert>
      ) : null}
      <div className="cf-form__row">
        <FormField label={t('common.name')} htmlFor="job-name" required error={errorOf('name')}>
          <Input
            id="job-name"
            value={draft.name}
            onChange={(e) => update('name', e.target.value)}
            invalid={Boolean(errorOf('name'))}
            aria-describedby={describedBy('job-name', errorOf('name'))}
          />
        </FormField>
        <FormField
          label={t('scheduler.detail.code')}
          htmlFor="job-code"
          required={!job}
          error={errorOf('code')}
          hint={t('scheduler.form.codeHint')}
        >
          <Input
            id="job-code"
            className="cf-mono"
            autoComplete="off"
            spellCheck={false}
            value={draft.code}
            disabled={job !== null}
            onChange={(e) => update('code', e.target.value)}
            invalid={Boolean(errorOf('code'))}
            aria-describedby={describedBy('job-code', errorOf('code'))}
          />
        </FormField>
      </div>
      <FormField
        label={t('common.description')}
        htmlFor="job-description"
        error={errorOf('description')}
      >
        <Textarea
          id="job-description"
          rows={2}
          value={draft.description}
          onChange={(e) => update('description', e.target.value)}
          invalid={Boolean(errorOf('description'))}
          aria-describedby={describedBy('job-description', errorOf('description'))}
        />
      </FormField>
      <div className="cf-form__row">
        <FormField
          label={t('scheduler.column.handler')}
          htmlFor="job-handler"
          required
          error={errorOf('handler')}
          hint={handlerHint}
        >
          <Select
            id="job-handler"
            placeholder={
              handlerOptions.length === 0 ? t('scheduler.form.handlerEmpty') : t('common.select')
            }
            options={handlerOptions}
            value={draft.handler}
            disabled={handlerOptions.length === 0}
            onChange={(e) => update('handler', e.target.value)}
            invalid={Boolean(errorOf('handler'))}
            aria-describedby={describedBy('job-handler', errorOf('handler'))}
          />
        </FormField>
        <FormField
          label={t('scheduler.column.type')}
          htmlFor="job-type"
          required
          error={errorOf('job_type')}
          hint={draft.job_type ? tEnum('scheduler.jobTypeHint', draft.job_type) : undefined}
        >
          <Select
            id="job-type"
            placeholder={t('common.select')}
            options={meta.job_types.map((type) => ({
              value: type,
              label: tEnum('scheduler.jobType', type),
            }))}
            value={draft.job_type}
            onChange={(e) => update('job_type', e.target.value as JobType | '')}
            invalid={Boolean(errorOf('job_type'))}
            aria-describedby={describedBy('job-type', errorOf('job_type'))}
          />
        </FormField>
      </div>
      {draft.job_type === 'cron' ? (
        <FormField
          label={t('scheduler.form.cron')}
          htmlFor="job-cron"
          required
          error={errorOf('cron_expression')}
          hint={t('scheduler.form.cronHint', {
            descriptors: meta.cron.descriptors.join(', '),
            min: formatSeconds(meta.cron.min_every_seconds),
            max: meta.cron.max_length,
          })}
        >
          <Input
            id="job-cron"
            className="cf-mono"
            autoComplete="off"
            spellCheck={false}
            value={draft.cron_expression}
            onChange={(e) => update('cron_expression', e.target.value)}
            invalid={Boolean(errorOf('cron_expression'))}
            aria-describedby={describedBy('job-cron', errorOf('cron_expression'))}
          />
        </FormField>
      ) : null}
      {draft.job_type === 'interval' ? (
        <FormField
          label={t('scheduler.form.interval')}
          htmlFor="job-interval"
          required
          error={errorOf('interval_minutes')}
          hint={t('scheduler.form.intervalHint', {
            min: limits.min_interval_minutes,
            max: formatSeconds(limits.max_interval_minutes * 60),
          })}
        >
          <Input
            id="job-interval"
            type="number"
            inputMode="numeric"
            min={limits.min_interval_minutes}
            step={1}
            value={draft.interval_minutes}
            onChange={(e) => update('interval_minutes', e.target.value)}
            invalid={Boolean(errorOf('interval_minutes'))}
            aria-describedby={describedBy('job-interval', errorOf('interval_minutes'))}
          />
        </FormField>
      ) : null}
      <FormField
        label={t('scheduler.form.timezone')}
        htmlFor="job-timezone"
        required
        error={errorOf('timezone')}
        hint={t('scheduler.form.timezoneHint', {
          format: tEnum('scheduler.timezoneFormat', meta.timezone.format),
          max: meta.timezone.max_length,
          default: meta.timezone.default,
        })}
      >
        <Input
          id="job-timezone"
          list={TIMEZONE_LIST_ID}
          autoComplete="off"
          spellCheck={false}
          value={draft.timezone}
          onChange={(e) => update('timezone', e.target.value)}
          invalid={Boolean(errorOf('timezone'))}
          aria-describedby={describedBy('job-timezone', errorOf('timezone'))}
        />
        <datalist id={TIMEZONE_LIST_ID}>
          {zones.map((zone) => (
            <option key={zone} value={zone} />
          ))}
        </datalist>
      </FormField>
      <div className="cf-form__row">
        <FormField
          label={t('scheduler.form.maxRetries')}
          htmlFor="job-max-retries"
          required
          error={errorOf('max_retries')}
          hint={t('scheduler.form.maxRetriesHint', { max: limits.max_retries })}
        >
          <Input
            id="job-max-retries"
            type="number"
            inputMode="numeric"
            min={0}
            step={1}
            value={draft.max_retries}
            onChange={(e) => update('max_retries', e.target.value)}
            invalid={Boolean(errorOf('max_retries'))}
            aria-describedby={describedBy('job-max-retries', errorOf('max_retries'))}
          />
        </FormField>
        <FormField
          label={t('scheduler.form.timeout')}
          htmlFor="job-timeout"
          required
          error={errorOf('timeout_seconds')}
          hint={timeoutHint}
        >
          <Input
            id="job-timeout"
            type="number"
            inputMode="numeric"
            min={0}
            step={1}
            value={draft.timeout_seconds}
            onChange={(e) => update('timeout_seconds', e.target.value)}
            invalid={Boolean(errorOf('timeout_seconds'))}
            aria-describedby={describedBy('job-timeout', errorOf('timeout_seconds'))}
          />
        </FormField>
      </div>
      <FormField
        label={t('scheduler.form.payload')}
        htmlFor="job-payload"
        error={errorOf('payload')}
        hint={t('scheduler.form.payloadHint', { max: formatBytes(limits.max_payload_bytes) })}
      >
        <Textarea
          id="job-payload"
          mono
          rows={4}
          value={draft.payload}
          onChange={(e) => update('payload', e.target.value)}
          invalid={Boolean(errorOf('payload'))}
          aria-describedby={describedBy('job-payload', errorOf('payload'))}
        />
      </FormField>
    </FormModal>
  );
}
