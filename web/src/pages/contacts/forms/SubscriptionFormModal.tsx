import { useMemo, useState } from 'react';
import { contactsApi, type AttributeDefinition, type ContactList } from '@/api/contacts';
import {
  formsApi,
  formsMeta,
  type FormField,
  type FormsMeta,
  type FormStatus,
  type SubscriptionForm,
} from '@/api/forms';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  Checkbox,
  ChipsInput,
  ErrorState,
  FormField as Field,
  Input,
  Modal,
  Select,
  Skeleton,
  Textarea,
} from '@/design/components';
import { IconChevronDown, IconChevronUp, IconPlus, IconTrash } from '@/design/icons';
import { changed } from '@/lib/patch';
import { t, tEnum } from '@/i18n';
import { FormModal } from '@/pages/shared/FormModal';
import type { ResourceFormProps } from '@/pages/shared/ResourceTab';
import {
  availableOptions,
  draftFromForm,
  EMAIL_FIELD_KEY,
  emptyFormDraft,
  fieldCatalog,
  fieldFromOption,
  isLockedRequired,
  missingMandatory,
  moveField,
  normalizeOrigin,
  removeField,
  validateFormDraft,
  withSavedFields,
  type FieldOption,
  type FormDraft,
  type FormDraftErrors,
} from './formDraft';
import './forms.css';

interface EditorData {
  meta: FormsMeta;
  attributes: AttributeDefinition[] | null;
  lists: ContactList[] | null;
}

/** Alta y edicion de un formulario de suscripcion. */
export function SubscriptionFormModal(props: ResourceFormProps<SubscriptionForm>) {
  const { can } = useAccess();
  const canReadAttributes = can(...PERMISSIONS.contactAttributes.read);
  const canReadLists = can(...PERMISSIONS.contactLists.read);
  const data = useQuery(async (): Promise<EditorData> => {
    const [meta, attributes, lists] = await Promise.all([
      formsMeta.get(),
      canReadAttributes ? contactsApi.listAttributes() : Promise.resolve(null),
      canReadLists
        ? contactsApi.listLists({ page: 1, per_page: PICKER_PAGE_SIZE }).then((p) => p.items)
        : Promise.resolve(null),
    ]);
    return { meta, attributes, lists };
  }, [canReadAttributes, canReadLists]);

  const title = props.item ? t('contacts.forms.editTitle') : t('contacts.forms.createTitle');
  if (!data.data) {
    return (
      <Modal open title={title} onClose={props.onClose}>
        {data.error ? (
          <ErrorState error={data.error} onRetry={data.reload} />
        ) : (
          <Skeleton lines={6} />
        )}
      </Modal>
    );
  }
  return <FormEditor {...props} {...data.data} />;
}

function FormEditor({
  item,
  onClose,
  onSaved,
  meta,
  attributes,
  lists,
}: ResourceFormProps<SubscriptionForm> & EditorData) {
  const catalog = useMemo(() => {
    const base = fieldCatalog(meta, attributes ?? []);
    return item && !attributes ? withSavedFields(base, item.fields) : base;
  }, [meta, attributes, item]);
  const [draft, setDraft] = useState<FormDraft>(() => {
    const initial = item ? draftFromForm(item) : emptyFormDraft(catalog);
    return {
      ...initial,
      fields: initial.fields.map((f) =>
        isLockedRequired(catalog, f.key) ? { ...f, required: true } : f,
      ),
    };
  });
  const [errors, setErrors] = useState<FormDraftErrors>({});
  const [adding, setAdding] = useState('');
  const { limits } = meta;

  const action = useAction(async () => {
    const { request } = validateFormDraft(draft, limits, catalog);
    if (!request) return;
    if (item) {
      await formsApi.update(item.id, {
        ...request,
        name: changed(request.name, item.name),
        status: changed(request.status, item.status),
      });
    } else {
      await formsApi.create(request);
    }
    onSaved();
  });

  const submit = async () => {
    const result = validateFormDraft(draft, limits, catalog);
    setErrors(result.errors);
    if (result.request) await action.run();
  };

  const setTexts = (patch: Partial<FormDraft['texts']>) =>
    setDraft((d) => ({ ...d, texts: { ...d.texts, ...patch } }));
  const setField = (key: string, patch: Partial<FormField>) =>
    setDraft((d) => ({
      ...d,
      fields: d.fields.map((f) => (f.key === key ? { ...f, ...patch } : f)),
    }));

  const remaining = availableOptions(catalog, draft.fields);
  const addField = () => {
    const option = remaining.find((o) => o.key === adding);
    if (!option) return;
    setDraft((d) => ({ ...d, fields: [...d.fields, fieldFromOption(option)] }));
    setAdding('');
  };
  const missing = missingMandatory(catalog, draft.fields);
  const listOptions = (lists ?? []).map((l) => ({ value: l.id, label: l.name }));
  if (lists && draft.listId && !lists.some((l) => l.id === draft.listId)) {
    listOptions.push({ value: draft.listId, label: draft.listId });
  }

  return (
    <FormModal
      id="subscription-form"
      title={item ? t('contacts.forms.editTitle') : t('contacts.forms.createTitle')}
      submitLabel={item ? t('common.save') : t('common.create')}
      busy={action.busy}
      error={action.error}
      onClose={onClose}
      onSubmit={submit}
      submitDisabled={lists === null}
      size="lg"
    >
      <Alert tone="info">{t('contacts.forms.doubleOptIn')}</Alert>
      <div className="cf-form__row">
        <Field label={t('common.name')} htmlFor="form-name" required error={errors.name}>
          <Input
            id="form-name"
            value={draft.name}
            maxLength={limits.max_name_length}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            invalid={Boolean(errors.name)}
          />
        </Field>
        <Field label={t('common.status')} htmlFor="form-status">
          <Select
            id="form-status"
            options={meta.statuses.map((s) => ({
              value: s,
              label: tEnum('contacts.forms.status', s),
            }))}
            value={draft.status}
            onChange={(e) => setDraft({ ...draft, status: e.target.value as FormStatus })}
          />
        </Field>
      </div>
      {lists === null ? (
        <Alert tone="warning">{t('contacts.forms.noListPermission')}</Alert>
      ) : (
        <Field
          label={t('contacts.forms.list')}
          htmlFor="form-list"
          required
          error={errors.listId}
          hint={lists.length === 0 ? t('contacts.lists.none') : t('contacts.forms.listHint')}
        >
          <Select
            id="form-list"
            placeholder={t('common.select')}
            options={listOptions}
            value={draft.listId}
            onChange={(e) => setDraft({ ...draft, listId: e.target.value })}
            invalid={Boolean(errors.listId)}
          />
        </Field>
      )}

      <div className="cf-form__section">{t('contacts.forms.fields')}</div>
      <span className="cf-text-sm cf-text-secondary">{t('contacts.forms.fieldsHint')}</span>
      {attributes === null ? (
        <Alert tone="info">{t('contacts.forms.noAttributePermission')}</Alert>
      ) : null}
      {missing.length > 0 ? (
        <Alert tone="warning">
          {t('contacts.forms.error.mandatoryMissing', { keys: missing.join(', ') })}
        </Alert>
      ) : null}
      <ol className="cf-form-fields" aria-label={t('contacts.forms.fields')}>
        {draft.fields.map((field, index) => (
          <FieldRow
            key={field.key}
            field={field}
            option={catalog.find((o) => o.key === field.key)}
            index={index}
            count={draft.fields.length}
            lockedRequired={isLockedRequired(catalog, field.key)}
            labelError={errors.fieldLabels?.[field.key]}
            placeholderError={errors.fieldPlaceholders?.[field.key]}
            limits={{ label: limits.max_label_length, placeholder: limits.max_placeholder_length }}
            onChange={(patch) => setField(field.key, patch)}
            onMove={(delta) =>
              setDraft((d) => ({ ...d, fields: moveField(d.fields, index, delta) }))
            }
            onRemove={() => setDraft((d) => ({ ...d, fields: removeField(d.fields, field.key) }))}
          />
        ))}
      </ol>
      {errors.fields ? (
        <span className="cf-field__error" role="alert">
          {errors.fields}
        </span>
      ) : null}
      {remaining.length > 0 && draft.fields.length < limits.max_fields ? (
        <div className="cf-form-fields__add">
          <Field label={t('contacts.forms.addField')} htmlFor="form-add-field">
            <Select
              id="form-add-field"
              placeholder={t('common.select')}
              options={remaining.map((o) => ({ value: o.key, label: optionLabel(o) }))}
              value={adding}
              onChange={(e) => setAdding(e.target.value)}
            />
          </Field>
          <Button icon={<IconPlus size={16} />} disabled={!adding} onClick={addField}>
            {t('contacts.forms.add')}
          </Button>
        </div>
      ) : null}

      <div className="cf-form__section">{t('contacts.forms.texts')}</div>
      <Field label={t('contacts.forms.textTitle')} htmlFor="form-title" error={errors.title}>
        <Input
          id="form-title"
          value={draft.texts.title}
          maxLength={limits.max_title_length}
          onChange={(e) => setTexts({ title: e.target.value })}
          invalid={Boolean(errors.title)}
        />
      </Field>
      <Field label={t('common.description')} htmlFor="form-description" error={errors.description}>
        <Textarea
          id="form-description"
          rows={2}
          value={draft.texts.description}
          maxLength={limits.max_description_length}
          onChange={(e) => setTexts({ description: e.target.value })}
        />
      </Field>
      <Field
        label={t('contacts.forms.submitLabel')}
        htmlFor="form-submit-label"
        error={errors.submitLabel}
        hint={t('contacts.forms.submitLabelHint')}
      >
        <Input
          id="form-submit-label"
          value={draft.texts.submit_label}
          maxLength={limits.max_submit_label_length}
          onChange={(e) => setTexts({ submit_label: e.target.value })}
        />
      </Field>
      <Field
        label={t('contacts.forms.consentText')}
        htmlFor="form-consent"
        required
        error={errors.consentText}
        hint={t('contacts.forms.consentHint')}
      >
        <Textarea
          id="form-consent"
          rows={3}
          value={draft.texts.consent_text}
          maxLength={limits.max_consent_text_length}
          onChange={(e) => setTexts({ consent_text: e.target.value })}
          invalid={Boolean(errors.consentText)}
        />
      </Field>

      <div className="cf-form__section">{t('contacts.forms.afterSubmit')}</div>
      <Field
        label={t('contacts.forms.redirectUrl')}
        htmlFor="form-redirect"
        error={errors.redirectUrl}
        hint={t('contacts.forms.redirectHint')}
      >
        <Input
          id="form-redirect"
          type="url"
          inputMode="url"
          spellCheck={false}
          value={draft.redirectUrl}
          maxLength={limits.max_redirect_url_length}
          onChange={(e) => setDraft({ ...draft, redirectUrl: e.target.value })}
          invalid={Boolean(errors.redirectUrl)}
        />
      </Field>
      <Field
        label={t('contacts.forms.successMessage')}
        htmlFor="form-success"
        required
        error={errors.successMessage}
        hint={t('contacts.forms.successHint')}
      >
        <Textarea
          id="form-success"
          rows={2}
          value={draft.texts.success_message}
          maxLength={limits.max_success_message_length}
          onChange={(e) => setTexts({ success_message: e.target.value })}
          invalid={Boolean(errors.successMessage)}
        />
      </Field>

      <div className="cf-form__section">{t('contacts.forms.allowedOrigins')}</div>
      <Field
        label={t('contacts.forms.allowedOrigins')}
        htmlFor="form-origins"
        error={errors.allowedOrigins}
        hint={t('contacts.forms.originsHint', { n: limits.max_allowed_origins })}
      >
        <ChipsInput
          id="form-origins"
          values={draft.allowedOrigins}
          onChange={(allowedOrigins) => setDraft({ ...draft, allowedOrigins })}
          normalize={normalizeOrigin}
          placeholder={t('contacts.forms.originPlaceholder')}
          invalid={Boolean(errors.allowedOrigins)}
          removeLabel={(value) => t('contacts.forms.removeOrigin', { origin: value })}
          rejectedLabel={(rejected) =>
            t('contacts.forms.error.origin', { origins: rejected.join(', ') })
          }
        />
      </Field>
    </FormModal>
  );
}

function optionLabel(option: FieldOption): string {
  const type = tEnum('contacts.forms.fieldType', option.type);
  return option.label === option.key
    ? `${option.key} (${type})`
    : `${option.label} (${option.key}, ${type})`;
}

interface FieldRowProps {
  field: FormField;
  option: FieldOption | undefined;
  index: number;
  count: number;
  lockedRequired: boolean;
  labelError?: string;
  placeholderError?: string;
  limits: { label: number; placeholder: number };
  onChange: (patch: Partial<FormField>) => void;
  onMove: (delta: -1 | 1) => void;
  onRemove: () => void;
}

function FieldRow({
  field,
  option,
  index,
  count,
  lockedRequired,
  labelError,
  placeholderError,
  limits,
  onChange,
  onMove,
  onRemove,
}: FieldRowProps) {
  const id = `form-field-${index}`;
  const isEmail = field.key === EMAIL_FIELD_KEY;
  return (
    <li className="cf-form-fields__item">
      <div className="cf-form-fields__head">
        <code className="cf-mono">{field.key}</code>
        {option ? (
          <span className="cf-text-sm cf-text-muted">
            {tEnum('contacts.forms.fieldType', option.type)}
          </span>
        ) : null}
        <div className="cf-table__actions">
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconChevronUp size={14} />}
            disabled={index === 0}
            onClick={() => onMove(-1)}
          >
            {t('contacts.forms.moveUp', { key: field.key })}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconChevronDown size={14} />}
            disabled={index === count - 1}
            onClick={() => onMove(1)}
          >
            {t('contacts.forms.moveDown', { key: field.key })}
          </Button>
          {!isEmail ? (
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon={<IconTrash size={14} />}
              onClick={onRemove}
            >
              {t('contacts.forms.removeField', { key: field.key })}
            </Button>
          ) : null}
        </div>
      </div>
      <div className="cf-form__row">
        <Field
          label={t('contacts.forms.fieldLabel')}
          htmlFor={`${id}-label`}
          required
          error={labelError}
        >
          <Input
            id={`${id}-label`}
            value={field.label}
            maxLength={limits.label}
            onChange={(e) => onChange({ label: e.target.value })}
            invalid={Boolean(labelError)}
          />
        </Field>
        <Field
          label={t('contacts.forms.fieldPlaceholder')}
          htmlFor={`${id}-placeholder`}
          error={placeholderError}
        >
          <Input
            id={`${id}-placeholder`}
            value={field.placeholder}
            maxLength={limits.placeholder}
            onChange={(e) => onChange({ placeholder: e.target.value })}
          />
        </Field>
      </div>
      <Checkbox
        label={
          isEmail
            ? t('contacts.forms.emailAlwaysRequired')
            : lockedRequired
              ? t('contacts.forms.attributeRequired')
              : t('contacts.forms.fieldRequired')
        }
        checked={isEmail || lockedRequired || field.required}
        disabled={isEmail || lockedRequired}
        onChange={(e) => onChange({ required: e.target.checked })}
      />
    </li>
  );
}
