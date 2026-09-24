import { useState } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { davErrorField, davErrorMessage } from '../davErrors';
import {
  CONTACT_VALUE_TYPES,
  webmailApi,
  type Contact,
  type ContactInput,
  type ContactValue,
} from '@/api/webmail';
import {
  Alert,
  Button,
  FormField,
  Input,
  Modal,
  Select,
  Textarea,
  useToast,
} from '@/design/components';
import { IconPlus, IconX } from '@/design/icons';
import { t } from '@/i18n';
import { cleanContactInput, contactProblems, emptyContact, toContactInput } from './contacts';

export interface ContactFormDialogProps {
  /** Ficha que se edita; sin ella se crea una nueva con `seed`. */
  contact?: Contact;
  seed?: ContactInput;
  onClose: () => void;
  onSaved: (contact: Contact) => void;
}

// Campos del formulario que pueden llevar un error del servicio (details.field).
const FORM_FIELDS =
  /^(name|given_name|family_name|organization|title|birthday|notes|emails|phones|(emails|phones)\[\d+\]\.value)$/;

const TYPE_OPTIONS = () =>
  CONTACT_VALUE_TYPES.map((type) => ({ value: type, label: t(`webmail.contacts.type.${type}`) }));

export function ContactFormDialog({ contact, seed, onClose, onSaved }: ContactFormDialogProps) {
  const toast = useToast();
  const [form, setForm] = useState<ContactInput>(() =>
    contact ? toContactInput(contact) : (seed ?? emptyContact()),
  );
  const [etag, setEtag] = useState(contact?.etag);
  const [problems, setProblems] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [conflict, setConflict] = useState(false);

  const update = (patch: Partial<ContactInput>) => {
    setForm((current) => ({ ...current, ...patch }));
    setProblems({});
  };

  const submit = async () => {
    const found = contactProblems(form);
    setProblems(found);
    if (Object.keys(found).length) return;
    setBusy(true);
    setError(null);
    setConflict(false);
    try {
      const input = cleanContactInput(form);
      const saved = contact
        ? await webmailApi.updateContact(contact.id, input, etag)
        : await webmailApi.createContact(input);
      toast.success(t(contact ? 'webmail.contacts.updated' : 'webmail.contacts.created'));
      onSaved(saved);
    } catch (err) {
      setBusy(false);
      if (errorCode(err) === ERROR_CODES.PRECONDITION_FAILED) {
        setConflict(true);
        return;
      }
      const field = davErrorField(err);
      if (field && FORM_FIELDS.test(field)) setProblems({ [field]: errorMessage(err) });
      else setError(err);
    }
  };

  // Tras un 412 se trae la version guardada: lo escrito se pierde, pero no se pisa el
  // cambio del otro dispositivo sin verlo.
  const reloadLatest = async () => {
    if (!contact) return;
    setBusy(true);
    try {
      const latest = await webmailApi.contact(contact.id);
      setForm(toContactInput(latest));
      setEtag(latest.etag);
      setConflict(false);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const valueList = (kind: 'emails' | 'phones', label: string, inputType: 'email' | 'tel') => {
    const values: ContactValue[] = form[kind];
    const set = (next: ContactValue[]) => update({ [kind]: next });
    return (
      <fieldset className="cf-wm-fieldset">
        <legend className="cf-field__label">{label}</legend>
        {values.map((item, i) => {
          const key = `${kind}[${i}].value`;
          return (
            <div key={i} className="cf-wm-rule-row">
              <div className="cf-wm-rule-row__value">
                <Input
                  id={`wm-contact-${kind}-${i}`}
                  name={`${kind}[${i}].value`}
                  type={inputType}
                  aria-label={label}
                  value={item.value}
                  invalid={Boolean(problems[key])}
                  onChange={(e) =>
                    set(values.map((v, j) => (j === i ? { ...v, value: e.target.value } : v)))
                  }
                />
                {problems[key] ? (
                  <span className="cf-field__error" role="alert">
                    {problems[key]}
                  </span>
                ) : null}
              </div>
              <Select
                id={`wm-contact-${kind}-${i}-type`}
                name={`${kind}[${i}].type`}
                aria-label={t('webmail.contacts.valueType')}
                value={item.type}
                options={TYPE_OPTIONS()}
                onChange={(e) => {
                  const type = CONTACT_VALUE_TYPES.find((v) => v === e.target.value) ?? item.type;
                  set(values.map((v, j) => (j === i ? { ...v, type } : v)));
                }}
              />
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon={<IconX size={16} />}
                onClick={() => set(values.filter((_, j) => j !== i))}
              >
                {t('webmail.contacts.removeValue', { value: item.value || label })}
              </Button>
            </div>
          );
        })}
        {problems[kind] ? (
          <span className="cf-field__error" role="alert">
            {problems[kind]}
          </span>
        ) : null}
        <div>
          <Button
            size="sm"
            variant="ghost"
            icon={<IconPlus size={16} />}
            onClick={() =>
              set([...values, { value: '', type: kind === 'phones' ? 'mobile' : 'work' }])
            }
          >
            {t(kind === 'emails' ? 'webmail.contacts.addEmail' : 'webmail.contacts.addPhone')}
          </Button>
        </div>
      </fieldset>
    );
  };

  return (
    <Modal
      open
      size="lg"
      title={t(contact ? 'webmail.contacts.editTitle' : 'webmail.contacts.newTitle')}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
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
        {conflict ? (
          <Alert tone="warning" title={t('webmail.contacts.conflictTitle')}>
            <p>{t('webmail.contacts.conflict')}</p>
            <Button size="sm" onClick={() => void reloadLatest()} loading={busy}>
              {t('webmail.contacts.loadLatest')}
            </Button>
          </Alert>
        ) : null}
        <FormField
          label={t('webmail.contacts.name')}
          htmlFor="wm-contact-name"
          error={problems.name}
        >
          <Input
            id="wm-contact-name"
            value={form.name}
            invalid={Boolean(problems.name)}
            onChange={(e) => update({ name: e.target.value })}
          />
        </FormField>
        <div className="cf-form__row">
          <FormField
            label={t('webmail.contacts.givenName')}
            htmlFor="wm-contact-given"
            error={problems.given_name}
          >
            <Input
              id="wm-contact-given"
              value={form.given_name}
              onChange={(e) => update({ given_name: e.target.value })}
            />
          </FormField>
          <FormField
            label={t('webmail.contacts.familyName')}
            htmlFor="wm-contact-family"
            error={problems.family_name}
          >
            <Input
              id="wm-contact-family"
              value={form.family_name}
              onChange={(e) => update({ family_name: e.target.value })}
            />
          </FormField>
        </div>
        {valueList('emails', t('webmail.contacts.emails'), 'email')}
        {valueList('phones', t('webmail.contacts.phones'), 'tel')}
        <div className="cf-form__row">
          <FormField
            label={t('webmail.contacts.organization')}
            htmlFor="wm-contact-org"
            error={problems.organization}
          >
            <Input
              id="wm-contact-org"
              value={form.organization}
              onChange={(e) => update({ organization: e.target.value })}
            />
          </FormField>
          <FormField
            label={t('webmail.contacts.jobTitle')}
            htmlFor="wm-contact-title"
            error={problems.title}
          >
            <Input
              id="wm-contact-title"
              value={form.title}
              onChange={(e) => update({ title: e.target.value })}
            />
          </FormField>
        </div>
        <FormField
          label={t('webmail.contacts.birthday')}
          htmlFor="wm-contact-birthday"
          error={problems.birthday}
        >
          <Input
            id="wm-contact-birthday"
            type={!form.birthday || /^\d{4}-/.test(form.birthday) ? 'date' : 'text'}
            value={form.birthday}
            invalid={Boolean(problems.birthday)}
            onChange={(e) => update({ birthday: e.target.value })}
          />
        </FormField>
        <FormField
          label={t('webmail.contacts.notes')}
          htmlFor="wm-contact-notes"
          error={problems.notes}
        >
          <Textarea
            id="wm-contact-notes"
            rows={3}
            value={form.notes}
            onChange={(e) => update({ notes: e.target.value })}
          />
        </FormField>
        {error ? (
          <div className="cf-form__error" role="alert">
            {davErrorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
