import { useState } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { WEEKDAYS, webmailApi, type BookingPage, type Weekday } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  ConfirmDialog,
  CopyButton,
  ErrorState,
  FormField,
  Input,
  Select,
  Skeleton,
  Textarea,
  useToast,
} from '@/design/components';
import { IconLink, IconPlus, IconTrash } from '@/design/icons';
import { t } from '@/i18n';
import { davErrorField } from '../davErrors';
import { timeZoneOptions } from '../calendar/calendar';
import {
  BOOKING_API_FIELDS,
  bookingLink,
  bookingProblems,
  formToSettings,
  newBookingForm,
  settingsToForm,
  type BookingField,
  type BookingForm,
} from './booking';

/** Pagina publica de citas del buzon: su configuracion y su enlace. */
export function BookingSettings() {
  // { page: null } es un buzon sin pagina de citas; data null, que aun se esta leyendo.
  const loaded = useQuery(async (signal) => {
    try {
      return { page: await webmailApi.bookingSettings(signal) };
    } catch (err) {
      if (errorCode(err) === ERROR_CODES.NOT_FOUND) return { page: null };
      throw err;
    }
  }, []);

  if (loaded.error) {
    return (
      <Card title={t('webmail.booking.title')}>
        <ErrorState error={loaded.error} onRetry={loaded.reload} />
      </Card>
    );
  }
  if (!loaded.data) {
    return (
      <Card title={t('webmail.booking.title')}>
        <Skeleton lines={8} />
      </Card>
    );
  }
  const initial = loaded.data.page;
  return (
    <BookingSettingsForm
      key={initial?.updated_at ?? 'nueva'}
      initial={initial}
      onSaved={(page) => loaded.setData({ page })}
    />
  );
}

function BookingSettingsForm({
  initial,
  onSaved,
}: {
  initial: BookingPage | null;
  onSaved: (page: BookingPage) => void;
}) {
  const toast = useToast();
  const [form, setForm] = useState<BookingForm>(() =>
    initial ? settingsToForm(initial) : newBookingForm(),
  );
  const [problems, setProblems] = useState<Partial<Record<BookingField, string>>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [regenerating, setRegenerating] = useState(false);

  const update = (patch: Partial<BookingForm>) => {
    setForm((current) => ({ ...current, ...patch }));
    setProblems({});
  };
  const setDay = (day: Weekday, windows: BookingForm['weekly'][Weekday]) =>
    update({ weekly: { ...form.weekly, [day]: windows } });

  const save = async (regenerate: boolean) => {
    const found = bookingProblems(form);
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      const saved = await webmailApi.saveBookingSettings(formToSettings(form), regenerate);
      toast.success(t(regenerate ? 'webmail.booking.regenerated' : 'webmail.booking.saved'));
      onSaved(saved);
    } catch (err) {
      const field = davErrorField(err);
      const mapped = field
        ? (BOOKING_API_FIELDS[field] ?? (field.startsWith('weekly') ? 'weekly' : undefined))
        : undefined;
      if (mapped) setProblems({ [mapped]: errorMessage(err) });
      else setError(err);
    } finally {
      setBusy(false);
    }
  };

  const numberField = (
    field: 'duration' | 'buffer' | 'noticeHours' | 'advanceDays' | 'dailyLimit',
    label: string,
  ) => (
    <FormField label={label} htmlFor={`wm-booking-${field}`} error={problems[field]}>
      <Input
        id={`wm-booking-${field}`}
        type="number"
        min={0}
        value={form[field]}
        invalid={Boolean(problems[field])}
        onChange={(e) => update({ [field]: e.target.value })}
      />
    </FormField>
  );
  const link = initial ? bookingLink(initial, window.location.origin) : '';

  return (
    <Card title={t('webmail.booking.title')}>
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void save(false);
        }}
      >
        <p className="cf-field__hint">{t('webmail.booking.description')}</p>
        {initial ? (
          <div className="cf-wm-booking__link">
            <IconLink size={16} />
            <strong>{t('webmail.booking.link')}</strong>
            <a href={link} target="_blank" rel="noopener noreferrer">
              {link}
            </a>
            <CopyButton value={link} />
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setRegenerating(true)}>
              {t('webmail.booking.regenerate')}
            </Button>
          </div>
        ) : (
          <Alert tone="info">{t('webmail.booking.notConfigured')}</Alert>
        )}
        {initial && !initial.active ? (
          <Alert tone="warning">{t('webmail.booking.linkInactive')}</Alert>
        ) : null}
        <Checkbox
          label={t('webmail.booking.active')}
          checked={form.active}
          onChange={(e) => update({ active: e.target.checked })}
        />
        <FormField
          label={t('webmail.booking.fieldTitle')}
          htmlFor="wm-booking-title"
          error={problems.title}
          required
        >
          <Input
            id="wm-booking-title"
            value={form.title}
            invalid={Boolean(problems.title)}
            onChange={(e) => update({ title: e.target.value })}
          />
        </FormField>
        <FormField label={t('webmail.booking.fieldDescription')} htmlFor="wm-booking-description">
          <Textarea
            id="wm-booking-description"
            rows={3}
            value={form.description}
            onChange={(e) => update({ description: e.target.value })}
          />
        </FormField>
        <div className="cf-form__row">
          {numberField('duration', t('webmail.booking.duration'))}
          {numberField('buffer', t('webmail.booking.buffer'))}
        </div>
        <div className="cf-form__row">
          {numberField('noticeHours', t('webmail.booking.notice'))}
          {numberField('advanceDays', t('webmail.booking.advance'))}
        </div>
        <div className="cf-form__row">
          {numberField('dailyLimit', t('webmail.booking.daily'))}
          <FormField label={t('webmail.booking.timezone')} htmlFor="wm-booking-timezone">
            <Select
              id="wm-booking-timezone"
              value={form.timezone}
              options={timeZoneOptions(form.timezone).map((z) => ({ value: z, label: z }))}
              onChange={(e) => update({ timezone: e.target.value })}
            />
          </FormField>
        </div>
        <fieldset className="cf-wm-fieldset">
          <legend className="cf-field__label">{t('webmail.booking.weekly')}</legend>
          {problems.weekly ? (
            <span className="cf-field__error" role="alert">
              {problems.weekly}
            </span>
          ) : null}
          {WEEKDAYS.map((day) => {
            const windows = form.weekly[day];
            const dayLabel = t(`webmail.calendar.weekday.${day}`);
            return (
              <div key={day} className="cf-wm-booking__day">
                <strong>{dayLabel}</strong>
                <div className="cf-wm-booking__windows">
                  {windows.length === 0 ? (
                    <span className="cf-field__hint">{t('webmail.booking.noWindows')}</span>
                  ) : null}
                  {windows.map((w, i) => (
                    <div key={i} className="cf-wm-booking__window">
                      <Input
                        type="time"
                        aria-label={`${dayLabel} ${t('webmail.booking.from')} ${i + 1}`}
                        value={w.start}
                        onChange={(e) =>
                          setDay(
                            day,
                            windows.map((x, j) => (j === i ? { ...x, start: e.target.value } : x)),
                          )
                        }
                      />
                      <Input
                        type="time"
                        aria-label={`${dayLabel} ${t('webmail.booking.to')} ${i + 1}`}
                        value={w.end}
                        onChange={(e) =>
                          setDay(
                            day,
                            windows.map((x, j) => (j === i ? { ...x, end: e.target.value } : x)),
                          )
                        }
                      />
                      <Button
                        size="sm"
                        variant="ghost"
                        iconOnly
                        icon={<IconTrash size={14} />}
                        onClick={() =>
                          setDay(
                            day,
                            windows.filter((_, j) => j !== i),
                          )
                        }
                      >
                        {t('webmail.booking.removeWindow', { n: i + 1, day: dayLabel })}
                      </Button>
                    </div>
                  ))}
                  <Button
                    size="sm"
                    variant="ghost"
                    icon={<IconPlus size={14} />}
                    onClick={() => setDay(day, [...windows, { start: '09:00', end: '13:00' }])}
                  >
                    {t('webmail.booking.addWindow')}
                  </Button>
                </div>
              </div>
            );
          })}
        </fieldset>
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
        <div className="cf-form__actions">
          <Button type="submit" variant="primary" loading={busy}>
            {t('common.save')}
          </Button>
        </div>
      </form>
      <ConfirmDialog
        open={regenerating}
        title={t('webmail.booking.regenerate')}
        message={t('webmail.booking.regenerateConfirm')}
        confirmLabel={t('webmail.booking.regenerate')}
        onCancel={() => setRegenerating(false)}
        onConfirm={async () => {
          await save(true);
          setRegenerating(false);
        }}
      />
    </Card>
  );
}
