import { useState } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { davErrorField, davErrorMessage } from '../davErrors';
import {
  RECURRENCE_FREQUENCIES,
  WEEKDAYS,
  webmailApi,
  type CalendarEvent,
  type Weekday,
} from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Checkbox,
  ConfirmDialog,
  ErrorState,
  FormField,
  Input,
  Modal,
  Select,
  Skeleton,
  Textarea,
  useToast,
} from '@/design/components';
import { IconTrash } from '@/design/icons';
import { t } from '@/i18n';
import {
  EVENT_API_FIELDS,
  eventProblems,
  eventToForm,
  formToInput,
  newEventForm,
  weekdayOf,
  type EventField,
  type EventForm,
} from './calendar';

// Recordatorios habituales, en minutos antes del inicio.
const REMINDER_OPTIONS = [0, 5, 15, 30, 60, 1440] as const;

export interface EventDialogProps {
  /** Evento que se edita; sin el se crea uno en `day`. */
  eventId?: string;
  /** La ocurrencia pertenece a una serie: los cambios se aplican a toda ella. */
  recurring?: boolean;
  day: Date;
  onClose: () => void;
  onChanged: () => void;
}

export function EventDialog({
  eventId,
  recurring = false,
  day,
  onClose,
  onChanged,
}: EventDialogProps) {
  const loaded = useQuery(
    (signal) => (eventId ? webmailApi.calendarEvent(eventId, signal) : Promise.resolve(null)),
    [eventId],
  );

  if (eventId && !loaded.data) {
    return (
      <Modal open title={t('webmail.calendar.editTitle')} onClose={onClose}>
        {loaded.error ? (
          <ErrorState error={loaded.error} onRetry={loaded.reload} />
        ) : (
          <Skeleton lines={6} />
        )}
      </Modal>
    );
  }
  return (
    <EventFormDialog
      key={loaded.data?.etag ?? 'nuevo'}
      event={loaded.data}
      recurring={recurring}
      day={day}
      onClose={onClose}
      onChanged={onChanged}
      onReload={loaded.reload}
    />
  );
}

function EventFormDialog({
  event,
  recurring,
  day,
  onClose,
  onChanged,
  onReload,
}: {
  event: CalendarEvent | null;
  recurring: boolean;
  day: Date;
  onClose: () => void;
  onChanged: () => void;
  onReload: () => void;
}) {
  const toast = useToast();
  const [form, setForm] = useState<EventForm>(() =>
    event ? eventToForm(event) : newEventForm(day),
  );
  const [problems, setProblems] = useState<Partial<Record<EventField, string>>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [conflict, setConflict] = useState(false);
  const [removing, setRemoving] = useState(false);
  const series = Boolean(event?.recurrence) || recurring;

  const update = (patch: Partial<EventForm>) => {
    setForm((current) => ({ ...current, ...patch }));
    setProblems({});
  };

  const submit = async () => {
    const found = eventProblems(form);
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      const input = formToInput(form);
      if (event) await webmailApi.updateCalendarEvent(event.id, input, event.etag);
      else await webmailApi.createCalendarEvent(input);
      toast.success(t(event ? 'webmail.calendar.updated' : 'webmail.calendar.created'));
      onChanged();
      onClose();
    } catch (err) {
      setBusy(false);
      if (errorCode(err) === ERROR_CODES.PRECONDITION_FAILED) setConflict(true);
      else {
        const field = davErrorField(err);
        const mapped = field ? EVENT_API_FIELDS[field] : undefined;
        if (mapped) setProblems({ [mapped]: errorMessage(err) });
        else setError(err);
      }
    }
  };

  const toggleDay = (weekday: Weekday) =>
    update({
      byDay: form.byDay.includes(weekday)
        ? form.byDay.filter((d) => d !== weekday)
        : [...form.byDay, weekday],
    });

  const startDay = new Date(`${form.startDate}T00:00:00`);
  const weekdayLabel = (weekday: Weekday) => t(`webmail.calendar.weekday.${weekday}`);

  return (
    <>
      <Modal
        open={!removing}
        size="lg"
        title={t(event ? 'webmail.calendar.editTitle' : 'webmail.calendar.newTitle')}
        onClose={() => (busy ? undefined : onClose())}
        footer={
          <>
            {event ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                disabled={busy}
                onClick={() => setRemoving(true)}
              >
                {t(series ? 'webmail.calendar.deleteSeries' : 'webmail.calendar.delete')}
              </Button>
            ) : null}
            <span className="cf-wm-reader__spacer" />
            <Button onClick={onClose} disabled={busy}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="primary"
              loading={busy}
              disabled={conflict}
              onClick={() => void submit()}
            >
              {t('common.save')}
            </Button>
          </>
        }
      >
        <form
          className="cf-form"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          {series ? <Alert tone="info">{t('webmail.calendar.seriesNote')}</Alert> : null}
          {conflict ? (
            <Alert tone="warning" title={t('webmail.contacts.conflictTitle')}>
              <p>{t('webmail.calendar.conflict')}</p>
              <Button size="sm" onClick={onReload}>
                {t('webmail.contacts.loadLatest')}
              </Button>
            </Alert>
          ) : null}
          <FormField
            label={t('webmail.calendar.eventTitle')}
            htmlFor="wm-event-title"
            error={problems.title}
            required
          >
            <Input
              id="wm-event-title"
              value={form.title}
              invalid={Boolean(problems.title)}
              onChange={(e) => update({ title: e.target.value })}
            />
          </FormField>
          <Checkbox
            label={t('webmail.calendar.allDay')}
            checked={form.allDay}
            onChange={(e) => update({ allDay: e.target.checked })}
          />
          <div className="cf-form__row">
            <FormField
              label={t('webmail.calendar.start')}
              htmlFor="wm-event-start"
              error={problems.start}
            >
              <div className="cf-wm-datetime">
                <Input
                  id="wm-event-start"
                  type="date"
                  value={form.startDate}
                  onChange={(e) => update({ startDate: e.target.value })}
                />
                {form.allDay ? null : (
                  <Input
                    type="time"
                    aria-label={t('webmail.calendar.startTime')}
                    value={form.startTime}
                    onChange={(e) => update({ startTime: e.target.value })}
                  />
                )}
              </div>
            </FormField>
            <FormField
              label={t('webmail.calendar.end')}
              htmlFor="wm-event-end"
              error={problems.end}
            >
              <div className="cf-wm-datetime">
                <Input
                  id="wm-event-end"
                  type="date"
                  value={form.endDate}
                  invalid={Boolean(problems.end)}
                  onChange={(e) => update({ endDate: e.target.value })}
                />
                {form.allDay ? null : (
                  <Input
                    type="time"
                    aria-label={t('webmail.calendar.endTime')}
                    value={form.endTime}
                    invalid={Boolean(problems.end)}
                    onChange={(e) => update({ endTime: e.target.value })}
                  />
                )}
              </div>
            </FormField>
          </div>
          <FormField
            label={t('webmail.calendar.location')}
            htmlFor="wm-event-location"
            error={problems.location}
          >
            <Input
              id="wm-event-location"
              value={form.location}
              onChange={(e) => update({ location: e.target.value })}
            />
          </FormField>
          <FormField
            label={t('webmail.calendar.description')}
            htmlFor="wm-event-description"
            error={problems.description}
          >
            <Textarea
              id="wm-event-description"
              rows={3}
              value={form.description}
              onChange={(e) => update({ description: e.target.value })}
            />
          </FormField>
          <div className="cf-form__row">
            <FormField label={t('webmail.calendar.repeat')} htmlFor="wm-event-repeat">
              <Select
                id="wm-event-repeat"
                value={form.repeat}
                options={[
                  { value: '', label: t('webmail.calendar.repeat.none') },
                  ...RECURRENCE_FREQUENCIES.map((freq) => ({
                    value: freq,
                    label: t(`webmail.calendar.repeat.${freq}`),
                  })),
                ]}
                onChange={(e) =>
                  update({
                    repeat: RECURRENCE_FREQUENCIES.find((f) => f === e.target.value) ?? '',
                    byDay:
                      e.target.value === 'weekly' &&
                      form.byDay.length === 0 &&
                      !Number.isNaN(startDay.getTime())
                        ? [weekdayOf(startDay)]
                        : form.byDay,
                  })
                }
              />
            </FormField>
            <FormField label={t('webmail.calendar.reminder')} htmlFor="wm-event-reminder">
              <Select
                id="wm-event-reminder"
                value={form.reminder}
                options={[
                  { value: '', label: t('webmail.calendar.reminder.none') },
                  ...REMINDER_OPTIONS.map((minutes) => ({
                    value: String(minutes),
                    label: t(`webmail.calendar.reminder.${minutes}`),
                  })),
                ]}
                onChange={(e) => update({ reminder: e.target.value })}
              />
            </FormField>
          </div>
          {form.repeat ? (
            <fieldset className="cf-wm-fieldset">
              <legend className="cf-field__label">{t('webmail.calendar.recurrence')}</legend>
              <FormField
                label={t('webmail.calendar.interval')}
                htmlFor="wm-event-interval"
                error={problems.interval}
              >
                <Input
                  id="wm-event-interval"
                  type="number"
                  min={1}
                  value={form.interval}
                  invalid={Boolean(problems.interval)}
                  onChange={(e) => update({ interval: e.target.value })}
                />
              </FormField>
              {form.repeat === 'weekly' ? (
                <div
                  className="cf-wm-weekdays"
                  role="group"
                  aria-label={t('webmail.calendar.byDay')}
                >
                  {WEEKDAYS.map((weekday) => (
                    <Checkbox
                      key={weekday}
                      label={weekdayLabel(weekday)}
                      checked={form.byDay.includes(weekday)}
                      onChange={() => toggleDay(weekday)}
                    />
                  ))}
                </div>
              ) : null}
              <FormField label={t('webmail.calendar.ends')} htmlFor="wm-event-ends">
                <Select
                  id="wm-event-ends"
                  value={form.ends}
                  options={[
                    { value: 'never', label: t('webmail.calendar.ends.never') },
                    { value: 'count', label: t('webmail.calendar.ends.count') },
                    { value: 'until', label: t('webmail.calendar.ends.until') },
                  ]}
                  onChange={(e) =>
                    update({
                      ends:
                        e.target.value === 'count' || e.target.value === 'until'
                          ? e.target.value
                          : 'never',
                    })
                  }
                />
              </FormField>
              {form.ends === 'count' ? (
                <FormField
                  label={t('webmail.calendar.count')}
                  htmlFor="wm-event-count"
                  error={problems.count}
                >
                  <Input
                    id="wm-event-count"
                    type="number"
                    min={1}
                    value={form.count}
                    invalid={Boolean(problems.count)}
                    onChange={(e) => update({ count: e.target.value })}
                  />
                </FormField>
              ) : null}
              {form.ends === 'until' ? (
                <FormField
                  label={t('webmail.calendar.until')}
                  htmlFor="wm-event-until"
                  error={problems.until}
                >
                  <Input
                    id="wm-event-until"
                    type="date"
                    value={form.until}
                    invalid={Boolean(problems.until)}
                    onChange={(e) => update({ until: e.target.value })}
                  />
                </FormField>
              ) : null}
              {series ? null : <p className="cf-field__hint">{t('webmail.calendar.seriesNote')}</p>}
            </fieldset>
          ) : null}
          {error ? (
            <div className="cf-form__error" role="alert">
              {davErrorMessage(error)}
            </div>
          ) : null}
        </form>
      </Modal>
      <ConfirmDialog
        open={removing}
        title={t(series ? 'webmail.calendar.deleteSeries' : 'webmail.calendar.delete')}
        message={t(
          series ? 'webmail.calendar.deleteSeriesConfirm' : 'webmail.calendar.deleteConfirm',
          {
            title: event?.title ?? '',
          },
        )}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setRemoving(false)}
        onConfirm={async () => {
          if (!event) return;
          await webmailApi.deleteCalendarEvent(event.id);
          toast.success(t('webmail.calendar.deleted'));
          onChanged();
          onClose();
        }}
      />
    </>
  );
}
