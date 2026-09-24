import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  bookingApi,
  type BookingConfirmation,
  type BookingSlot,
  type BookingTarget,
} from '@/api/booking';
import { ERROR_CODES, errorCode, errorDetail } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import { Alert, Button, FormField, Input, Skeleton, Textarea } from '@/design/components';
import {
  IconCalendar,
  IconCheckCircle,
  IconChevronLeft,
  IconChevronRight,
  IconClock,
} from '@/design/icons';
import { getLocale, t } from '@/i18n';
import { bookingWindow, hasNextWindow, slotsByDay, validVisitorEmail } from './booking';
import './booking.css';

type Problems = Partial<Record<'name' | 'email' | 'note', string>>;

/**
 * Pagina publica de citas: sin sesion. El visitante ve los huecos libres del dueno en su propia zona horaria,
 * elige uno y reserva con su nombre, su correo y una nota. La cita aparece en el calendario del dueno y los dos
 * reciben la invitacion.
 */
export default function PublicBookingPage() {
  const params = useParams();
  const target: BookingTarget = {
    cell: params.cell ?? '',
    tenant: params.tenant ?? '',
    page: params.page ?? '',
  };
  const [now] = useState(() => new Date());
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<BookingSlot | null>(null);
  const [done, setDone] = useState<BookingConfirmation | null>(null);
  const range = useMemo(() => bookingWindow(now, offset), [now, offset]);
  const page = useQuery(
    (signal) => bookingApi.page(target, range.start.toISOString(), range.end.toISOString(), signal),
    [target.cell, target.tenant, target.page, range],
  );
  const locale = getLocale();
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const dayFormat = new Intl.DateTimeFormat(locale, {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
  });
  const timeFormat = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' });
  const whenFormat = new Intl.DateTimeFormat(locale, { dateStyle: 'full', timeStyle: 'short' });

  if (page.error && !page.data) {
    const notFound = errorCode(page.error) === ERROR_CODES.NOT_FOUND;
    return (
      <main className="cf-booking">
        <div className="cf-booking__card">
          <h1 className="cf-booking__title">{t('booking.public.title')}</h1>
          <Alert tone={notFound ? 'info' : 'danger'}>
            {notFound ? t('booking.public.notFound') : errorMessage(page.error)}
          </Alert>
          {notFound ? null : <Button onClick={page.reload}>{t('common.retry')}</Button>}
        </div>
      </main>
    );
  }
  if (!page.data) {
    return (
      <main className="cf-booking">
        <div className="cf-booking__card">
          <Skeleton lines={8} />
        </div>
      </main>
    );
  }
  const info = page.data;

  if (done) {
    return (
      <main className="cf-booking">
        <div className="cf-booking__card" role="status">
          <h1 className="cf-booking__title">
            <IconCheckCircle size={24} /> {t('booking.public.done')}
          </h1>
          <p>{t('booking.public.doneText', { when: whenFormat.format(new Date(done.start)) })}</p>
          <p>
            {t(done.confirmation_sent ? 'booking.public.doneMail' : 'booking.public.doneNoMail')}
          </p>
        </div>
      </main>
    );
  }

  const days = slotsByDay(info.slots);
  return (
    <main className="cf-booking">
      <div className="cf-booking__card">
        <header className="cf-booking__header">
          <h1 className="cf-booking__title">
            <IconCalendar size={22} /> {info.title || t('booking.public.title')}
          </h1>
          {info.owner_name ? (
            <p className="cf-booking__owner">
              {t('booking.public.with', { owner: info.owner_name })}
            </p>
          ) : null}
          <p className="cf-booking__meta">
            <IconClock size={16} /> {t('booking.public.duration', { n: info.duration_minutes })}
          </p>
          {info.description ? <p className="cf-booking__description">{info.description}</p> : null}
        </header>
        {selected ? (
          <BookingFormView
            target={target}
            slot={selected}
            when={whenFormat.format(new Date(selected.start))}
            onBack={() => setSelected(null)}
            onTaken={() => {
              setSelected(null);
              page.reload();
            }}
            onDone={setDone}
          />
        ) : (
          <section aria-label={t('booking.public.pickSlot')}>
            <h2 className="cf-booking__subtitle">{t('booking.public.pickSlot')}</h2>
            <p className="cf-field__hint">{t('booking.public.zoneNote', { zone })}</p>
            <div className="cf-booking__nav">
              <Button
                size="sm"
                variant="ghost"
                icon={<IconChevronLeft size={16} />}
                disabled={offset === 0}
                onClick={() => setOffset((o) => Math.max(0, o - 1))}
              >
                {t('booking.public.previous')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                icon={<IconChevronRight size={16} />}
                disabled={!hasNextWindow(offset, info.max_advance_days)}
                onClick={() => setOffset((o) => o + 1)}
              >
                {t('booking.public.next')}
              </Button>
            </div>
            {days.length === 0 ? (
              <p className="cf-booking__empty">{t('booking.public.noSlots')}</p>
            ) : (
              <ol className="cf-booking__days">
                {days.map(({ day, slots }) => (
                  <li key={day} className="cf-booking__day">
                    <h3 className="cf-booking__date">
                      {dayFormat.format(new Date(slots[0]?.start ?? day))}
                    </h3>
                    <div className="cf-booking__slots">
                      {slots.map((slot) => (
                        <Button key={slot.start} size="sm" onClick={() => setSelected(slot)}>
                          {timeFormat.format(new Date(slot.start))}
                        </Button>
                      ))}
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </section>
        )}
      </div>
    </main>
  );
}

function BookingFormView({
  target,
  slot,
  when,
  onBack,
  onTaken,
  onDone,
}: {
  target: BookingTarget;
  slot: BookingSlot;
  when: string;
  onBack: () => void;
  onTaken: () => void;
  onDone: (confirmation: BookingConfirmation) => void;
}) {
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [note, setNote] = useState('');
  const [website, setWebsite] = useState('');
  const [problems, setProblems] = useState<Problems>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    const found: Problems = {};
    if (!name.trim()) found.name = t('booking.public.nameRequired');
    if (!validVisitorEmail(email)) found.email = t('booking.public.emailInvalid');
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    try {
      onDone(
        await bookingApi.book(target, {
          start: slot.start,
          name: name.trim(),
          email: email.trim(),
          note: note.trim(),
          website,
        }),
      );
    } catch (err) {
      setBusy(false);
      const code = errorCode(err);
      if (code === 'SLOT_UNAVAILABLE') {
        onTaken();
        return;
      }
      if (code === ERROR_CODES.RATE_LIMITED || code === 'LIMIT_EXCEEDED') {
        setError(t('booking.public.limit'));
        return;
      }
      const field = errorDetail(err, 'field');
      if (
        code === ERROR_CODES.VALIDATION_ERROR &&
        (field === 'name' || field === 'email' || field === 'note')
      ) {
        setProblems({ [field]: errorMessage(err) });
        return;
      }
      setError(
        errorCode(err) === ERROR_CODES.NOT_FOUND ? t('booking.public.notFound') : errorMessage(err),
      );
    }
  };

  return (
    <form
      className="cf-form cf-booking__form"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <p className="cf-booking__selected">{t('booking.public.selected', { when })}</p>
      <FormField
        label={t('booking.public.name')}
        htmlFor="booking-name"
        error={problems.name}
        required
      >
        <Input
          id="booking-name"
          autoComplete="name"
          value={name}
          invalid={Boolean(problems.name)}
          onChange={(e) => setName(e.target.value)}
        />
      </FormField>
      <FormField
        label={t('booking.public.email')}
        htmlFor="booking-email"
        error={problems.email}
        required
      >
        <Input
          id="booking-email"
          type="email"
          autoComplete="email"
          value={email}
          invalid={Boolean(problems.email)}
          onChange={(e) => setEmail(e.target.value)}
        />
      </FormField>
      <FormField label={t('booking.public.note')} htmlFor="booking-note" error={problems.note}>
        <Textarea
          id="booking-note"
          rows={3}
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </FormField>
      {/* Trampa para robots: oculta a las personas y a los lectores de pantalla. */}
      <div className="cf-booking__trap" aria-hidden="true">
        <label htmlFor="booking-website">{t('booking.public.website')}</label>
        <input
          id="booking-website"
          name="website"
          type="text"
          tabIndex={-1}
          autoComplete="off"
          value={website}
          onChange={(e) => setWebsite(e.target.value)}
        />
      </div>
      {error ? (
        <div className="cf-form__error" role="alert">
          {error}
        </div>
      ) : null}
      <div className="cf-form__actions">
        <Button onClick={onBack} disabled={busy}>
          {t('booking.public.back')}
        </Button>
        <Button type="submit" variant="primary" loading={busy}>
          {t('booking.public.submit')}
        </Button>
      </div>
    </form>
  );
}
