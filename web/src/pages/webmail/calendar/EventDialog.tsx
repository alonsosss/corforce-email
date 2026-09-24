import { useState } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { davErrorField, davErrorMessage } from '../davErrors';
import {
  RECURRENCE_FREQUENCIES,
  WEEKDAYS,
  webmailApi,
  type CalendarEvent,
  type InvitationDelivery,
  type Occurrence,
  type Weekday,
} from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ChipsInput,
  ConfirmDialog,
  ErrorState,
  FormField,
  Input,
  Modal,
  Select,
  Skeleton,
  Textarea,
  useToast,
  type BadgeTone,
} from '@/design/components';
import { IconTrash } from '@/design/icons';
import { hasMessage, t } from '@/i18n';
import { useWebmailStore } from '@/webmail/store';
import { normalizeRecipient } from '../compose';
import { useRecipientSuggestions } from '../recipients';
import { AvailabilityPanel } from './AvailabilityPanel';
import {
  EVENT_API_FIELDS,
  eventProblems,
  eventToForm,
  formToInput,
  formZone,
  instantInZone,
  newEventForm,
  occurrenceToForm,
  timeZoneOptions,
  weekdayOf,
  type EventField,
  type EventForm,
} from './calendar';

// Recordatorios habituales, en minutos antes del inicio.
const REMINDER_OPTIONS = [0, 5, 15, 30, 60, 1440] as const;

type Scope = 'one' | 'series';

const PARTSTAT_TONES: Record<string, BadgeTone> = {
  ACCEPTED: 'success',
  TENTATIVE: 'warning',
  DECLINED: 'danger',
};

export interface EventDialogProps {
  /** Evento que se edita; sin el se crea uno en `day`. */
  eventId?: string;
  /** Aparicion desde la que se abrio: si es de una serie, se puede cambiar solo ella. */
  occurrence?: Occurrence;
  day: Date;
  onClose: () => void;
  onChanged: () => void;
}

export function EventDialog({ eventId, occurrence, day, onClose, onChanged }: EventDialogProps) {
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
      occurrence={occurrence}
      day={day}
      onClose={onClose}
      onChanged={onChanged}
      onReload={loaded.reload}
    />
  );
}

function sameAddress(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}

function EventFormDialog({
  event,
  occurrence,
  day,
  onClose,
  onChanged,
  onReload,
}: {
  event: CalendarEvent | null;
  occurrence: Occurrence | undefined;
  day: Date;
  onClose: () => void;
  onChanged: () => void;
  onReload: () => void;
}) {
  const toast = useToast();
  const username = useWebmailStore((s) => s.session?.username ?? '');
  const single = Boolean(event && occurrence?.recurring && occurrence.recurrence_id);
  const [scope, setScope] = useState<Scope>(single ? 'one' : 'series');
  const [seriesForm, setSeriesForm] = useState<EventForm>(() =>
    event ? eventToForm(event) : newEventForm(day),
  );
  const [oneForm, setOneForm] = useState<EventForm | null>(() =>
    event && occurrence && single ? occurrenceToForm(event, occurrence) : null,
  );
  const [problems, setProblems] = useState<Partial<Record<EventField, string>>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [conflict, setConflict] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [notify, setNotify] = useState(true);
  const [attendeeQuery, setAttendeeQuery] = useState('');
  const suggestions = useRecipientSuggestions(attendeeQuery);

  const onlyThis = scope === 'one' && oneForm !== null;
  const form = onlyThis && oneForm ? oneForm : seriesForm;
  const series = Boolean(event?.recurrence) || Boolean(occurrence?.recurring);
  // La copia de una reunion que organiza otro: sus invitados no se cambian desde aqui.
  const organizedByOther = Boolean(
    event?.organizer && !sameAddress(event.organizer.email, username),
  );
  const canInvite = !organizedByOther && !onlyThis;
  const invites = form.attendees.length > 0 && !organizedByOther;

  const update = (patch: Partial<EventForm>) => {
    if (onlyThis) setOneForm((current) => (current ? { ...current, ...patch } : current));
    else setSeriesForm((current) => ({ ...current, ...patch }));
    setProblems({});
  };

  const report = (delivery: InvitationDelivery | null | undefined) => {
    if (!delivery || delivery.recipients === 0) return;
    if (delivery.sent)
      toast.success(t('webmail.calendar.invitationsSent', { n: delivery.recipients }));
    else toast.error(t('webmail.calendar.invitationsFailed'));
  };

  const submit = async () => {
    const found = eventProblems(form);
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      const input = formToInput(form);
      const saved =
        event && onlyThis && occurrence
          ? await webmailApi.updateCalendarOccurrence(
              event.id,
              occurrence.recurrence_id,
              input,
              event.etag,
              notify,
            )
          : event
            ? await webmailApi.updateCalendarEvent(event.id, input, event.etag, notify)
            : await webmailApi.createCalendarEvent(input, notify);
      toast.success(t(event ? 'webmail.calendar.updated' : 'webmail.calendar.created'));
      report(saved.invitations);
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
  const zone = formZone(form);
  const zoneOptions = timeZoneOptions(form.timezone);
  const slotStart = form.allDay ? null : instantInZone(form.startDate, form.startTime, zone);
  const slotEnd = form.allDay ? null : instantInZone(form.endDate, form.endTime, zone);
  const deleteKey = onlyThis
    ? 'webmail.calendar.deleteOne'
    : series
      ? 'webmail.calendar.deleteSeries'
      : 'webmail.calendar.delete';
  const partstatLabel = (value: string) => {
    const key = `webmail.calendar.partstat.${value}`;
    return hasMessage(key) ? t(key) : value;
  };

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
                {t(deleteKey)}
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
          {single ? (
            <FormField label={t('webmail.calendar.scope')} htmlFor="wm-event-scope">
              <Select
                id="wm-event-scope"
                value={scope}
                options={[
                  { value: 'one', label: t('webmail.calendar.scope.one') },
                  { value: 'series', label: t('webmail.calendar.scope.series') },
                ]}
                onChange={(e) => {
                  setScope(e.target.value === 'series' ? 'series' : 'one');
                  setProblems({});
                }}
              />
            </FormField>
          ) : series ? (
            <Alert tone="info">{t('webmail.calendar.seriesNote')}</Alert>
          ) : null}
          {organizedByOther && event?.organizer ? (
            <Alert tone="info">
              {t('webmail.calendar.organizedBy', {
                organizer: event.organizer.name || event.organizer.email,
              })}
            </Alert>
          ) : null}
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
          {onlyThis ? null : (
            <Checkbox
              label={t('webmail.calendar.allDay')}
              checked={form.allDay}
              onChange={(e) => update({ allDay: e.target.checked })}
            />
          )}
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
          {form.allDay || onlyThis ? null : (
            <FormField
              label={t('webmail.calendar.timezone')}
              htmlFor="wm-event-timezone"
              hint={t('webmail.calendar.timezoneHint')}
            >
              <Select
                id="wm-event-timezone"
                value={form.timezone}
                options={[
                  ...(form.timezone === ''
                    ? [{ value: '', label: t('webmail.calendar.timezone.keep', { zone }) }]
                    : []),
                  ...zoneOptions.map((z) => ({ value: z, label: z })),
                ]}
                onChange={(e) => {
                  // Cambiar de zona conserva la hora de pared escrita: 10:00 sigue siendo 10:00 en la nueva.
                  update({ timezone: e.target.value });
                }}
              />
            </FormField>
          )}
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
          {canInvite ? (
            <FormField
              label={t('webmail.calendar.attendees')}
              htmlFor="wm-event-attendees"
              hint={t('webmail.calendar.attendeesHint')}
            >
              <ChipsInput
                id="wm-event-attendees"
                values={form.attendees}
                onChange={(attendees) => update({ attendees })}
                normalize={normalizeRecipient}
                removeLabel={(value) => t('webmail.calendar.attendeeRemove', { address: value })}
                rejectedLabel={(rejected) =>
                  t('webmail.calendar.attendeeRejected', { addresses: rejected.join(', ') })
                }
                suggestions={suggestions}
                onQueryChange={setAttendeeQuery}
                suggestionsLabel={t('webmail.suggest.label')}
              />
            </FormField>
          ) : null}
          {event && event.attendees.length ? (
            <ul className="cf-wm-attendees" aria-label={t('webmail.calendar.responses')}>
              {event.attendees.map((a) => (
                <li key={a.email} className="cf-wm-attendees__item">
                  <span>{a.name ? `${a.name} <${a.email}>` : a.email}</span>
                  <Badge tone={PARTSTAT_TONES[a.partstat] ?? 'neutral'}>
                    {partstatLabel(a.partstat)}
                  </Badge>
                </li>
              ))}
            </ul>
          ) : null}
          {invites && !form.allDay && canInvite ? (
            <AvailabilityPanel
              attendees={form.attendees}
              date={form.startDate}
              zone={zone}
              start={slotStart}
              end={slotEnd}
            />
          ) : null}
          {invites ? (
            <Checkbox
              label={t('webmail.calendar.notify')}
              checked={notify}
              onChange={(e) => setNotify(e.target.checked)}
            />
          ) : null}
          {onlyThis ? null : (
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
          )}
          {form.repeat && !onlyThis ? (
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
        title={t(deleteKey)}
        message={t(
          onlyThis
            ? 'webmail.calendar.deleteOneConfirm'
            : series
              ? 'webmail.calendar.deleteSeriesConfirm'
              : 'webmail.calendar.deleteConfirm',
          { title: event?.title ?? '' },
        )}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setRemoving(false)}
        onConfirm={async () => {
          if (!event) return;
          if (onlyThis && occurrence) {
            const saved = await webmailApi.deleteCalendarOccurrence(
              event.id,
              occurrence.recurrence_id,
              event.etag,
              notify,
            );
            report(saved.invitations);
          } else {
            report(await webmailApi.deleteCalendarEvent(event.id, notify));
          }
          toast.success(t('webmail.calendar.deleted'));
          onChanged();
          onClose();
        }}
      />
    </>
  );
}
