import type { Contact, ContactInput, ContactValue, MailAddress } from '@/api/webmail';
import { getLocale, t } from '@/i18n';
import { initialsOf } from '../format';

export function emptyContact(): ContactInput {
  return {
    name: '',
    given_name: '',
    family_name: '',
    emails: [{ value: '', type: 'work' }],
    phones: [],
    organization: '',
    title: '',
    notes: '',
    birthday: '',
  };
}

export function toContactInput(contact: Contact): ContactInput {
  return {
    name: contact.name,
    given_name: contact.given_name,
    family_name: contact.family_name,
    emails: contact.emails.length ? contact.emails : [{ value: '', type: 'work' }],
    phones: contact.phones,
    organization: contact.organization,
    title: contact.title,
    notes: contact.notes,
    birthday: contact.birthday ?? '',
  };
}

/** Ficha nueva con el remitente de un mensaje. */
export function contactFromSender(sender: MailAddress): ContactInput {
  const name = sender.name.trim();
  return { ...emptyContact(), name, emails: [{ value: sender.email, type: 'other' }] };
}

export function contactName(
  contact: Pick<Contact, 'name' | 'given_name' | 'family_name' | 'emails'>,
): string {
  return (
    contact.name.trim() ||
    `${contact.given_name} ${contact.family_name}`.trim() ||
    contact.emails[0]?.value ||
    t('webmail.contacts.unnamed')
  );
}

/** Iniciales para el avatar de la lista: dos letras como mucho. */
export function contactInitials(contact: Contact): string {
  return initialsOf(contactName(contact));
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
// vCard admite el cumpleanos sin ano (--MM-DD); el servicio lo devuelve asi.
const BIRTHDAY = /^(\d{4}|--)-?(\d{2})-(\d{2})$/;

/** Cumpleanos legible; sin ano solo el dia y el mes. */
export function formatBirthday(value: string): string {
  const match = BIRTHDAY.exec(value);
  if (!match) return value;
  const [, year, month, day] = match;
  const date = new Date(
    Date.UTC(year === '--' ? 2000 : Number(year), Number(month) - 1, Number(day)),
  );
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(getLocale(), {
    day: 'numeric',
    month: 'long',
    year: year === '--' ? undefined : 'numeric',
    timeZone: 'UTC',
  }).format(date);
}

function withoutEmpty(values: readonly ContactValue[]): ContactValue[] {
  return values.map((v) => ({ ...v, value: v.value.trim() })).filter((v) => v.value !== '');
}

/** Lo que se envia: textos recortados y sin filas vacias. */
export function cleanContactInput(input: ContactInput): ContactInput {
  return {
    name: input.name.trim(),
    given_name: input.given_name.trim(),
    family_name: input.family_name.trim(),
    emails: withoutEmpty(input.emails),
    phones: withoutEmpty(input.phones),
    organization: input.organization.trim(),
    title: input.title.trim(),
    notes: input.notes.trim(),
    birthday: input.birthday,
  };
}

/**
 * Validacion previa; el servicio valida de nuevo (422 con details.field). Una ficha necesita
 * al menos un nombre o una direccion.
 */
export function contactProblems(input: ContactInput): Record<string, string> {
  const problems: Record<string, string> = {};
  const clean = cleanContactInput(input);
  if (!clean.name && !clean.given_name && !clean.family_name && clean.emails.length === 0) {
    problems.name = t('webmail.contacts.nameOrEmail');
  }
  input.emails.forEach((email, i) => {
    const value = email.value.trim();
    if (value && !EMAIL.test(value))
      problems[`emails[${i}].value`] = t('webmail.contacts.emailInvalid');
  });
  if (input.birthday && !BIRTHDAY.test(input.birthday)) {
    problems.birthday = t('webmail.contacts.birthdayInvalid');
  }
  return problems;
}
