import { useMemo, useState } from 'react';
import {
  contactsApi,
  contactsMeta,
  type AttributeDefinition,
  type AttributeValue,
  type ConsentApiMethod,
  type Contact,
  type ContactsMeta,
  type CreateContactRequest,
  type UpdateContactRequest,
} from '@/api/contacts';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Checkbox, ChipsInput, FormField, Input, Select } from '@/design/components';
import { browserTimezones } from '@/lib/datetime';
import { changed, isEmptyPatch } from '@/lib/patch';
import { rules, validateField } from '@/lib/validate';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { AttributeInput, attributeLabel } from './AttributeInput';
import { attributeToDraft, buildAttributes } from './attributeValue';

export interface ContactFormProps {
  /** null en el alta. */
  contact: Contact | null;
  onClose: () => void;
  onSaved: (contact: Contact) => void;
}

const TIMEZONES_LIST_ID = 'contact-timezones';

function normalizeTag(raw: string): string | null {
  const tag = raw.trim().toLowerCase();
  return tag ? tag : null;
}

export function ContactForm(props: ContactFormProps) {
  const { can } = useAccess();
  const canReadAttributes = can(...PERMISSIONS.contactAttributes.read);
  const loaded = useQuery(async () => {
    const [definitions, meta] = await Promise.all([
      canReadAttributes ? contactsApi.listAttributes() : Promise.resolve([]),
      contactsMeta.get(),
    ]);
    return { definitions, meta };
  }, [canReadAttributes]);
  const title = props.contact ? t('contacts.form.editTitle') : t('contacts.form.createTitle');
  return (
    <ResourceGate resource={loaded} modal={{ title, onClose: props.onClose }}>
      {(data) => <Form {...props} definitions={data.definitions} meta={data.meta} title={title} />}
    </ResourceGate>
  );
}

function Form({
  contact,
  definitions,
  meta,
  title,
  onClose,
  onSaved,
}: ContactFormProps & {
  definitions: AttributeDefinition[];
  meta: ContactsMeta;
  title: string;
}) {
  const [email, setEmail] = useState('');
  const [firstName, setFirstName] = useState(contact?.first_name ?? '');
  const [lastName, setLastName] = useState(contact?.last_name ?? '');
  const [locale, setLocale] = useState(contact?.locale ?? '');
  const [timezone, setTimezone] = useState(contact?.timezone ?? '');
  const [tags, setTags] = useState<string[]>(contact?.tags ?? []);
  const [drafts, setDrafts] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      definitions.map((d) => [d.key, attributeToDraft(contact?.attributes?.[d.key])]),
    ),
  );
  const [consentOn, setConsentOn] = useState(false);
  const [consentMethod, setConsentMethod] = useState<ConsentApiMethod | ''>(
    meta.api_consent_methods[0] ?? '',
  );
  const [consentSource, setConsentSource] = useState('');
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const timezones = useMemo(browserTimezones, []);

  const action = useAction(async (body: CreateContactRequest | UpdateContactRequest | null) => {
    if (!contact) {
      const { data } = await contactsApi.create(body as CreateContactRequest);
      onSaved(data);
      return;
    }
    if (!body) {
      onClose();
      return;
    }
    const { data } = await contactsApi.update(contact.id, body as UpdateContactRequest);
    onSaved(data);
  });

  const submit = async () => {
    const built = buildAttributes(definitions, drafts, contact ? (contact.attributes ?? {}) : null);
    const next: Record<string, string | undefined> = {
      ...built.errors,
      email: contact ? undefined : (validateField(email, rules.required, rules.email) ?? undefined),
      consentSource:
        !contact && consentOn && !consentSource.trim() ? t('validation.required') : undefined,
      consentMethod: !contact && consentOn && !consentMethod ? t('validation.required') : undefined,
    };
    setErrors(next);
    if (Object.values(next).some(Boolean)) return;

    if (!contact) {
      const attributes = Object.fromEntries(
        Object.entries(built.attributes).filter(
          (entry): entry is [string, AttributeValue] => entry[1] !== null,
        ),
      );
      await action.run({
        email: email.trim(),
        first_name: firstName.trim() || undefined,
        last_name: lastName.trim() || undefined,
        locale: locale.trim() || undefined,
        timezone: timezone.trim() || undefined,
        tags: tags.length ? tags : undefined,
        attributes: Object.keys(attributes).length ? attributes : undefined,
        consent:
          consentOn && consentMethod
            ? { status: 'granted', method: consentMethod, source: consentSource.trim() }
            : undefined,
      });
      return;
    }
    const body: UpdateContactRequest = {
      first_name: changed(firstName.trim(), contact.first_name),
      last_name: changed(lastName.trim(), contact.last_name),
      locale: changed(locale.trim(), contact.locale ?? ''),
      timezone: changed(timezone.trim(), contact.timezone ?? ''),
      tags: tags.join('\n') === (contact.tags ?? []).join('\n') ? undefined : tags,
      attributes: Object.keys(built.attributes).length ? built.attributes : undefined,
    };
    await action.run(isEmptyPatch(body) ? null : body);
  };

  return (
    <FormModal
      id="contact-form"
      title={title}
      submitLabel={contact ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      size="lg"
    >
      {contact ? (
        <FormField
          label={t('common.email')}
          htmlFor="contact-email"
          hint={t('contacts.form.emailImmutable')}
        >
          <Input id="contact-email" className="cf-mono" value={contact.email} readOnly />
        </FormField>
      ) : (
        <FormField label={t('common.email')} htmlFor="contact-email" required error={errors.email}>
          <Input
            id="contact-email"
            type="email"
            className="cf-mono"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            invalid={Boolean(errors.email)}
            autoComplete="off"
          />
        </FormField>
      )}
      <div className="cf-form__row">
        <FormField label={t('contacts.form.firstName')} htmlFor="contact-first-name">
          <Input
            id="contact-first-name"
            value={firstName}
            onChange={(e) => setFirstName(e.target.value)}
          />
        </FormField>
        <FormField label={t('contacts.form.lastName')} htmlFor="contact-last-name">
          <Input
            id="contact-last-name"
            value={lastName}
            onChange={(e) => setLastName(e.target.value)}
          />
        </FormField>
      </div>
      <div className="cf-form__row">
        <FormField
          label={t('contacts.form.locale')}
          htmlFor="contact-locale"
          hint={t('contacts.form.localeHint')}
        >
          <Input id="contact-locale" value={locale} onChange={(e) => setLocale(e.target.value)} />
        </FormField>
        <FormField
          label={t('contacts.form.timezone')}
          htmlFor="contact-timezone"
          hint={t('contacts.form.timezoneHint')}
        >
          <Input
            id="contact-timezone"
            list={timezones.length ? TIMEZONES_LIST_ID : undefined}
            value={timezone}
            onChange={(e) => setTimezone(e.target.value)}
          />
        </FormField>
      </div>
      {timezones.length ? (
        <datalist id={TIMEZONES_LIST_ID}>
          {timezones.map((zone) => (
            <option key={zone} value={zone} />
          ))}
        </datalist>
      ) : null}
      <FormField
        label={t('contacts.column.tags')}
        htmlFor="contact-tags"
        hint={t('contacts.form.tagsHint')}
      >
        <ChipsInput
          id="contact-tags"
          values={tags}
          onChange={setTags}
          normalize={normalizeTag}
          removeLabel={(value) => t('common.removeValue', { value })}
          rejectedLabel={(list) => t('validation.invalidValues', { list: list.join(', ') })}
        />
      </FormField>
      {definitions.length ? (
        <>
          <div className="cf-form__section">{t('contacts.attributes.title')}</div>
          <div className="cf-form__row">
            {definitions.map((def) => (
              <FormField
                key={def.key}
                label={attributeLabel(def)}
                htmlFor={`contact-attr-${def.key}`}
                required={def.required}
                error={errors[def.key]}
                hint={tEnum('contacts.attributeType', def.type)}
              >
                <AttributeInput
                  id={`contact-attr-${def.key}`}
                  definition={def}
                  value={drafts[def.key] ?? ''}
                  onChange={(value) => setDrafts((d) => ({ ...d, [def.key]: value }))}
                  invalid={Boolean(errors[def.key])}
                />
              </FormField>
            ))}
          </div>
        </>
      ) : null}
      {!contact ? (
        <>
          <div className="cf-form__section">{t('contacts.consent.title')}</div>
          <Checkbox
            label={t('contacts.form.declareConsent')}
            checked={consentOn}
            onChange={(e) => setConsentOn(e.target.checked)}
          />
          {consentOn ? (
            <div className="cf-form__row">
              <FormField
                label={t('contacts.consent.method')}
                htmlFor="contact-consent-method"
                required
                error={errors.consentMethod}
              >
                <Select
                  id="contact-consent-method"
                  placeholder={t('common.select')}
                  invalid={Boolean(errors.consentMethod)}
                  options={meta.api_consent_methods.map((m) => ({
                    value: m,
                    label: tEnum('contacts.consentMethod', m),
                  }))}
                  value={consentMethod}
                  onChange={(e) => setConsentMethod(e.target.value as ConsentApiMethod)}
                />
              </FormField>
              <FormField
                label={t('contacts.consent.source')}
                htmlFor="contact-consent-source"
                required
                error={errors.consentSource}
                hint={t('contacts.consent.sourceHint')}
              >
                <Input
                  id="contact-consent-source"
                  value={consentSource}
                  onChange={(e) => setConsentSource(e.target.value)}
                  invalid={Boolean(errors.consentSource)}
                />
              </FormField>
            </div>
          ) : null}
        </>
      ) : null}
    </FormModal>
  );
}
