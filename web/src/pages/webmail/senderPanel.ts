import type { Contact, MailAddress, MessageEnvelope, Occurrence } from '@/api/webmail';

/** Correos anteriores que se muestran en la ficha; el resto, con "Ver todos". */
export const PREVIOUS_SHOWN = 5;

const MIN_NAME_CHARS = 3;

function normalize(value: string): string {
  return value.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase().replace(/\s+/g, ' ').trim();
}

/** El contacto personal cuya lista de direcciones incluye la del remitente. */
export function contactFor(contacts: readonly Contact[], email: string): Contact | null {
  const wanted = email.trim().toLowerCase();
  return (
    contacts.find((c) => c.emails.some((e) => e.value.trim().toLowerCase() === wanted)) ?? null
  );
}

/**
 * Las reuniones que mencionan al remitente por su direccion o su nombre completo en el titulo o el
 * lugar. El calendario personal no guarda invitados: es lo que se puede saber sin inventar datos.
 */
export function meetingsWith(
  occurrences: readonly Occurrence[],
  sender: MailAddress,
  now: Date,
): Occurrence[] {
  const email = sender.email.trim().toLowerCase();
  const name = normalize(sender.name);
  return occurrences
    .filter((o) => new Date(o.end).getTime() >= now.getTime())
    .filter((o) => {
      const text = normalize(`${o.title} ${o.location}`);
      return (
        (email !== '' && text.includes(email)) ||
        (name.length >= MIN_NAME_CHARS && text.includes(name))
      );
    })
    .sort((a, b) => new Date(a.start).getTime() - new Date(b.start).getTime());
}

/** Los correos anteriores del remitente, sin el mensaje que se esta leyendo. */
export function previousMessages(
  items: readonly MessageEnvelope[],
  folderName: string,
  current: { folder: string; uid: number },
): MessageEnvelope[] {
  return items
    .filter((m) => !(folderName === current.folder && m.uid === current.uid))
    .slice(0, PREVIOUS_SHOWN);
}
