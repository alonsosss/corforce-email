import { useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { webmailApi, type Occurrence } from '@/api/webmail';
import { davLimits, davMeta } from '@/webmail/catalogs';
import { davErrorMessage } from '../davErrors';
import { useQuery } from '@/hooks/useQuery';
import { Button, EmptyState, Skeleton, Tabs } from '@/design/components';
import { IconCalendar, IconChevronLeft, IconChevronRight, IconPlus } from '@/design/icons';
import { getLocale, t } from '@/i18n';
import {
  CALENDAR_VIEWS,
  dayKey,
  groupByDay,
  mergeOccurrences,
  parseDay,
  shiftAnchor,
  splitWindow,
  visibleRange,
  type CalendarView,
} from './calendar';
import { EventDialog } from './EventDialog';

const MONTH_CELL_EVENTS = 3;

type Editing =
  { kind: 'new'; day: Date } | { kind: 'edit'; id: string; day: Date; occurrence: Occurrence };

/** Calendario personal del buzon (CalDAV en mail-dav). Solo se pide la ventana visible. */
export default function CalendarPage() {
  const [params, setParams] = useSearchParams();
  const view: CalendarView = CALENDAR_VIEWS.find((v) => v === params.get('view')) ?? 'month';
  const anchor = parseDay(params.get('date') ?? '') ?? new Date();
  const anchorKey = dayKey(anchor);
  const range = useMemo(
    () => visibleRange(view, parseDay(anchorKey) ?? new Date()),
    [view, anchorKey],
  );
  const [editing, setEditing] = useState<Editing | null>(null);
  const todayKey = dayKey(new Date());

  const occurrences = useQuery(
    async (signal) => {
      // Sin meta se pide la ventana entera: el servicio responde con su tope si la rechaza.
      const meta = await davMeta.get().catch(() => null);
      const windows = splitWindow(range.start, range.end, davLimits(meta).maxEventWindowDays);
      const parts = await Promise.all(
        windows.map((w) =>
          webmailApi.calendarOccurrences(w.start.toISOString(), w.end.toISOString(), signal),
        ),
      );
      return mergeOccurrences(parts);
    },
    [range],
  );
  const byDay = useMemo(() => groupByDay(occurrences.data ?? []), [occurrences.data]);

  const go = (next: { view?: CalendarView; date?: Date }) =>
    setParams({ view: next.view ?? view, date: dayKey(next.date ?? anchor) }, { replace: true });

  const locale = getLocale();
  const title =
    view === 'week'
      ? `${new Intl.DateTimeFormat(locale, { day: 'numeric', month: 'short' }).format(
          range.start,
        )} - ${new Intl.DateTimeFormat(locale, {
          day: 'numeric',
          month: 'short',
          year: 'numeric',
        }).format(range.days[range.days.length - 1] ?? range.start)}`
      : new Intl.DateTimeFormat(locale, { month: 'long', year: 'numeric' }).format(anchor);
  const weekdayFormat = new Intl.DateTimeFormat(locale, { weekday: 'short' });
  const timeFormat = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' });

  const openOccurrence = (occurrence: Occurrence, day: Date) =>
    setEditing({ kind: 'edit', id: occurrence.id, day, occurrence });

  const eventButton = (occurrence: Occurrence, day: Date, compact: boolean) => (
    <button
      key={`${occurrence.id}:${occurrence.start}`}
      type="button"
      className={[
        'cf-wm-event',
        occurrence.all_day ? 'cf-wm-event--allday' : '',
        compact ? 'cf-wm-event--compact' : '',
      ]
        .filter(Boolean)
        .join(' ')}
      onClick={() => openOccurrence(occurrence, day)}
    >
      <span className="cf-wm-event__time">
        {occurrence.all_day
          ? t('webmail.calendar.allDayShort')
          : timeFormat.format(new Date(occurrence.start))}
      </span>
      <span className="cf-wm-event__title">
        {occurrence.title || t('webmail.calendar.untitled')}
      </span>
      {!compact && occurrence.location ? (
        <span className="cf-wm-event__location">{occurrence.location}</span>
      ) : null}
    </button>
  );

  const dayLabel = (day: Date) =>
    new Intl.DateTimeFormat(locale, { weekday: 'long', day: 'numeric', month: 'long' }).format(day);

  return (
    <div className="cf-wm-calendar">
      <div className="cf-wm-calendar__bar">
        <h1 className="cf-wm-listbar__heading cf-wm-calendar__title">{title}</h1>
        <div className="cf-wm-calendar__nav">
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconChevronLeft size={16} />}
            onClick={() => go({ date: shiftAnchor(view, anchor, -1) })}
          >
            {t('webmail.calendar.previous')}
          </Button>
          <Button size="sm" onClick={() => go({ date: new Date() })}>
            {t('webmail.calendar.today')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconChevronRight size={16} />}
            onClick={() => go({ date: shiftAnchor(view, anchor, 1) })}
          >
            {t('webmail.calendar.next')}
          </Button>
        </div>
        <Tabs
          label={t('webmail.calendar.views')}
          items={CALENDAR_VIEWS.map((id) => ({ id, label: t(`webmail.calendar.view.${id}`) }))}
          value={view}
          onChange={(next) => go({ view: next })}
        />
        <Button
          size="sm"
          variant="primary"
          icon={<IconPlus size={16} />}
          onClick={() => setEditing({ kind: 'new', day: anchor })}
        >
          {t('webmail.calendar.new')}
        </Button>
      </div>
      {occurrences.error ? (
        <div className="cf-form__error" role="alert">
          {davErrorMessage(occurrences.error)}{' '}
          <Button size="sm" onClick={occurrences.reload}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}
      <div aria-busy={occurrences.loading || undefined}>
        {!occurrences.data && !occurrences.error ? (
          <Skeleton lines={8} />
        ) : view === 'month' ? (
          <div className="cf-wm-month" aria-label={title}>
            <div className="cf-wm-month__row cf-wm-month__head" aria-hidden="true">
              {range.days.slice(0, 7).map((day) => (
                <span key={day.getDay()} className="cf-wm-month__weekday">
                  {weekdayFormat.format(day)}
                </span>
              ))}
            </div>
            <div className="cf-wm-month__grid">
              {range.days.map((day) => {
                const key = dayKey(day);
                const events = byDay.get(key) ?? [];
                const outside = day.getMonth() !== anchor.getMonth();
                return (
                  <div
                    key={key}
                    className={[
                      'cf-wm-month__cell',
                      outside ? 'cf-wm-month__cell--outside' : '',
                      key === todayKey ? 'cf-wm-month__cell--today' : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    <button
                      type="button"
                      className="cf-wm-month__day"
                      aria-label={t('webmail.calendar.newOn', { day: dayLabel(day) })}
                      onClick={() => setEditing({ kind: 'new', day })}
                    >
                      {day.getDate()}
                    </button>
                    {events.slice(0, MONTH_CELL_EVENTS).map((o) => eventButton(o, day, true))}
                    {events.length > MONTH_CELL_EVENTS ? (
                      <button
                        type="button"
                        className="cf-wm-month__more"
                        onClick={() => go({ view: 'week', date: day })}
                      >
                        {t('webmail.calendar.more', { n: events.length - MONTH_CELL_EVENTS })}
                      </button>
                    ) : null}
                  </div>
                );
              })}
            </div>
          </div>
        ) : view === 'week' ? (
          <div className="cf-wm-week">
            {range.days.map((day) => {
              const key = dayKey(day);
              const events = byDay.get(key) ?? [];
              return (
                <section
                  key={key}
                  className={['cf-wm-week__day', key === todayKey ? 'cf-wm-week__day--today' : '']
                    .filter(Boolean)
                    .join(' ')}
                  aria-label={dayLabel(day)}
                >
                  <button
                    type="button"
                    className="cf-wm-week__head"
                    aria-label={t('webmail.calendar.newOn', { day: dayLabel(day) })}
                    onClick={() => setEditing({ kind: 'new', day })}
                  >
                    <span>{weekdayFormat.format(day)}</span>
                    <strong>{day.getDate()}</strong>
                  </button>
                  <div className="cf-wm-week__events">
                    {events.map((o) => eventButton(o, day, false))}
                  </div>
                </section>
              );
            })}
          </div>
        ) : (
          <AgendaList
            days={range.days}
            byDay={byDay}
            dayLabel={dayLabel}
            render={(o, day) => eventButton(o, day, false)}
          />
        )}
      </div>
      {editing ? (
        <EventDialog
          eventId={editing.kind === 'edit' ? editing.id : undefined}
          occurrence={editing.kind === 'edit' ? editing.occurrence : undefined}
          day={editing.day}
          onClose={() => setEditing(null)}
          onChanged={occurrences.reload}
        />
      ) : null}
    </div>
  );
}

function AgendaList({
  days,
  byDay,
  dayLabel,
  render,
}: {
  days: Date[];
  byDay: Map<string, Occurrence[]>;
  dayLabel: (day: Date) => string;
  render: (occurrence: Occurrence, day: Date) => ReactNode;
}) {
  const withEvents = days.filter((day) => (byDay.get(dayKey(day)) ?? []).length > 0);
  if (!withEvents.length) {
    return (
      <EmptyState icon={<IconCalendar size={32} />} title={t('webmail.calendar.agendaEmpty')} />
    );
  }
  return (
    <ol className="cf-wm-agenda">
      {withEvents.map((day) => (
        <li key={dayKey(day)} className="cf-wm-agenda__day">
          <h2 className="cf-wm-agenda__date">{dayLabel(day)}</h2>
          <div className="cf-wm-agenda__events">
            {(byDay.get(dayKey(day)) ?? []).map((o) => render(o, day))}
          </div>
        </li>
      ))}
    </ol>
  );
}
