import { webmailApi, type BusyInterval } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useQuery } from '@/hooks/useQuery';
import { Badge, Skeleton } from '@/design/components';
import { IconUsers } from '@/design/icons';
import { getLocale, t } from '@/i18n';
import { addDays, dayKey, instantInZone, overlapping, parseDay } from './calendar';

export interface AvailabilityPanelProps {
  /** Direcciones de los invitados. Solo las de la empresa tienen datos. */
  attendees: readonly string[];
  /** Dia (AAAA-MM-DD) en la zona del evento. */
  date: string;
  zone: string;
  /** El hueco elegido; null si el formulario aun no lo tiene. */
  start: Date | null;
  end: Date | null;
}

function timeFormatter(zone: string): Intl.DateTimeFormat {
  try {
    return new Intl.DateTimeFormat(getLocale(), {
      timeZone: zone,
      hour: '2-digit',
      minute: '2-digit',
    });
  } catch {
    return new Intl.DateTimeFormat(getLocale(), { hour: '2-digit', minute: '2-digit' });
  }
}

/**
 * Disponibilidad de los invitados el dia del evento: solo libre u ocupado (inicio y fin), nunca el
 * contenido. La sirve el webmail desde mail-dav, acotada a los buzones de la misma empresa.
 */
export function AvailabilityPanel({ attendees, date, zone, start, end }: AvailabilityPanelProps) {
  const dayStart = parseDay(date) ? instantInZone(date, '00:00', zone) : null;
  const key = attendees.join(',');
  const availability = useQuery(
    (signal) => {
      if (!dayStart || !attendees.length) return Promise.resolve([]);
      const day = parseDay(date);
      const next = day ? instantInZone(dayKey(addDays(day, 1)), '00:00', zone) : null;
      return webmailApi.availability(
        attendees,
        dayStart.toISOString(),
        (next ?? dayStart).toISOString(),
        signal,
      );
    },
    [key, date, zone],
  );
  if (!attendees.length) return null;
  const format = timeFormatter(zone);
  const range = (b: BusyInterval) =>
    `${format.format(new Date(b.start))} - ${format.format(new Date(b.end))}`;

  return (
    <section className="cf-wm-availability" aria-label={t('webmail.calendar.availability')}>
      <h3 className="cf-wm-availability__title">
        <IconUsers size={16} /> {t('webmail.calendar.availability')}
      </h3>
      {availability.error ? (
        <p className="cf-field__hint" role="alert">
          {errorMessage(availability.error)}
        </p>
      ) : !availability.data ? (
        <Skeleton lines={2} />
      ) : (
        <ul className="cf-wm-availability__list">
          {availability.data.map((item) => {
            const conflicts = start && end ? overlapping(item.busy, start, end) : [];
            return (
              <li key={item.address} className="cf-wm-availability__item">
                <span className="cf-wm-availability__address">{item.address}</span>
                {!item.known ? (
                  <Badge>{t('webmail.calendar.availability.unknown')}</Badge>
                ) : conflicts.length ? (
                  <Badge tone="danger">{t('webmail.calendar.availability.busyThen')}</Badge>
                ) : (
                  <Badge tone="success">{t('webmail.calendar.availability.freeThen')}</Badge>
                )}
                {item.known && item.busy.length ? (
                  <span className="cf-wm-availability__busy">
                    {t('webmail.calendar.availability.busy', {
                      ranges: item.busy.map(range).join(', '),
                    })}
                  </span>
                ) : null}
                {item.partial ? (
                  <span className="cf-field__hint">
                    {t('webmail.calendar.availability.partial')}
                  </span>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
