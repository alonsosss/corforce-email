import { useState, type FormEvent } from 'react';
import { errorMessage } from '@/api/messages';
import {
  toVacationInput,
  type Vacation,
  type VacationInput,
  type VacationLimits,
} from '@/api/vacation';
import { useAction } from '@/hooks/useAction';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  FormField,
  Input,
  Textarea,
  useToast,
} from '@/design/components';
import { t } from '@/i18n';

export interface VacationDraft {
  enabled: boolean;
  subject: string;
  message: string;
  interval: string;
  startsOn: string;
  endsOn: string;
}

export type VacationErrors = Partial<
  Record<'subject' | 'message' | 'interval' | 'startsOn' | 'endsOn', string>
>;

export function toDraft(v: Vacation): VacationDraft {
  return {
    enabled: v.enabled,
    subject: v.subject,
    message: v.message,
    interval: String(v.interval_days),
    startsOn: v.starts_on ?? '',
    endsOn: v.ends_on ?? '',
  };
}

const DATE = /^\d{4}-\d{2}-\d{2}$/;

function characters(text: string): number {
  return Array.from(text).length;
}

/**
 * Comprueba en el navegador lo que el servicio comprobara de todos modos, con los topes que el
 * propio servicio manda: el objetivo es avisar antes de enviar, no decidir. Devuelve los errores por
 * campo, vacio si todo esta bien.
 */
export function validateVacation(draft: VacationDraft, limits: VacationLimits): VacationErrors {
  const errors: VacationErrors = {};
  if (/[\r\n\t]/.test(draft.subject)) errors.subject = t('vacation.error.subjectLine');
  else if (characters(draft.subject) > limits.subject_max_length)
    errors.subject = t('vacation.error.subjectTooLong', { max: limits.subject_max_length });
  if (draft.enabled && draft.message.trim() === '')
    errors.message = t('vacation.error.messageRequired');
  else if (characters(draft.message) > limits.message_max_length)
    errors.message = t('vacation.error.messageTooLong', { max: limits.message_max_length });
  const days = Number(draft.interval);
  if (!Number.isInteger(days) || days < limits.interval_min_days || days > limits.interval_max_days)
    errors.interval = t('vacation.error.interval', {
      min: limits.interval_min_days,
      max: limits.interval_max_days,
    });
  for (const [field, value] of [
    ['startsOn', draft.startsOn],
    ['endsOn', draft.endsOn],
  ] as const) {
    if (value !== '' && (!DATE.test(value) || Number.isNaN(Date.parse(value))))
      errors[field] = t('vacation.error.date');
  }
  if (
    !errors.startsOn &&
    !errors.endsOn &&
    draft.startsOn &&
    draft.endsOn &&
    draft.endsOn < draft.startsOn
  )
    errors.endsOn = t('vacation.error.window');
  return errors;
}

export function toInput(draft: VacationDraft): VacationInput {
  return {
    enabled: draft.enabled,
    subject: draft.subject.trim(),
    message: draft.message,
    interval_days: Number(draft.interval),
    starts_on: draft.startsOn || null,
    ends_on: draft.endsOn || null,
  };
}

export interface VacationFormProps {
  initial: Vacation;
  /** Guarda y devuelve lo guardado: el formulario se reinicia con ello. */
  onSave: (input: VacationInput) => Promise<Vacation>;
  editable: boolean;
  /** Aviso opcional sobre quien puede ver esta respuesta (por ejemplo, en la ficha del buzon). */
  notice?: string;
}

/**
 * Formulario de la respuesta automatica de un buzon. Lo usan la ficha del buzon (administracion) y
 * el webmail (el propio usuario): mismo formulario, mismos topes, distinto camino al servicio.
 */
export function VacationForm({ initial, onSave, editable, notice }: VacationFormProps) {
  const toast = useToast();
  const [draft, setDraft] = useState(() => toDraft(initial));
  const [errors, setErrors] = useState<VacationErrors>({});
  const limits = initial.limits;

  const save = useAction(async () => {
    const saved = await onSave(toInput(draft));
    setDraft(toDraft(saved));
    toast.success(t('vacation.saved'));
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next = validateVacation(draft, limits);
    setErrors(next);
    if (Object.keys(next).length > 0) return;
    await save.run();
  };

  const set = <K extends keyof VacationDraft>(key: K, value: VacationDraft[K]) =>
    setDraft((current) => ({ ...current, [key]: value }));

  return (
    <Card title={t('vacation.title')} description={t('vacation.description')}>
      <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        {notice ? <Alert tone="info">{notice}</Alert> : null}
        <Checkbox
          label={t('vacation.enabled')}
          checked={draft.enabled}
          onChange={(e) => set('enabled', e.target.checked)}
          disabled={!editable}
        />
        <FormField
          label={t('vacation.subject')}
          htmlFor="vacation-subject"
          error={errors.subject}
          hint={t('vacation.subjectHint', { max: limits.subject_max_length })}
        >
          <Input
            id="vacation-subject"
            value={draft.subject}
            onChange={(e) => set('subject', e.target.value)}
            readOnly={!editable}
            invalid={Boolean(errors.subject)}
          />
        </FormField>
        <FormField
          label={t('vacation.message')}
          htmlFor="vacation-message"
          error={errors.message}
          hint={t('vacation.messageHint', { max: limits.message_max_length })}
          required={draft.enabled}
        >
          <Textarea
            id="vacation-message"
            rows={8}
            value={draft.message}
            onChange={(e) => set('message', e.target.value)}
            readOnly={!editable}
            invalid={Boolean(errors.message)}
          />
        </FormField>
        <FormField
          label={t('vacation.interval')}
          htmlFor="vacation-interval"
          error={errors.interval}
          hint={t('vacation.intervalHint', {
            min: limits.interval_min_days,
            max: limits.interval_max_days,
          })}
        >
          <Input
            id="vacation-interval"
            type="number"
            inputMode="numeric"
            min={limits.interval_min_days}
            max={limits.interval_max_days}
            value={draft.interval}
            onChange={(e) => set('interval', e.target.value)}
            readOnly={!editable}
            invalid={Boolean(errors.interval)}
          />
        </FormField>
        <div className="cf-split">
          <FormField
            label={t('vacation.startsOn')}
            htmlFor="vacation-starts"
            error={errors.startsOn}
          >
            <Input
              id="vacation-starts"
              type="date"
              value={draft.startsOn}
              onChange={(e) => set('startsOn', e.target.value)}
              readOnly={!editable}
              invalid={Boolean(errors.startsOn)}
            />
          </FormField>
          <FormField
            label={t('vacation.endsOn')}
            htmlFor="vacation-ends"
            error={errors.endsOn}
            hint={t('vacation.datesHint')}
          >
            <Input
              id="vacation-ends"
              type="date"
              value={draft.endsOn}
              onChange={(e) => set('endsOn', e.target.value)}
              readOnly={!editable}
              invalid={Boolean(errors.endsOn)}
            />
          </FormField>
        </div>
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
    </Card>
  );
}

export { toVacationInput };
