import { describe, expect, it } from 'vitest';
import type { AttributeDefinition } from '@/api/contacts';
import type { FormLimits, FormsMeta } from '@/api/forms';
import { t } from '@/i18n';
import {
  draftFromForm,
  emptyFormDraft,
  fieldCatalog,
  moveField,
  normalizeOrigin,
  removeField,
  validateFormDraft,
  type FormDraft,
} from './formDraft';

const META: Pick<FormsMeta, 'builtin_fields'> = {
  builtin_fields: [
    { key: 'email', type: 'email' },
    { key: 'first_name', type: 'string' },
    { key: 'last_name', type: 'string' },
  ],
};

const LIMITS: FormLimits = {
  max_fields: 4,
  max_name_length: 40,
  max_label_length: 20,
  max_placeholder_length: 20,
  max_title_length: 60,
  max_description_length: 200,
  max_submit_label_length: 20,
  max_consent_text_length: 200,
  max_success_message_length: 200,
  max_allowed_origins: 2,
  max_redirect_url_length: 60,
  max_stats_days: 365,
};

function attribute(key: string, required: boolean): AttributeDefinition {
  return {
    id: `attr-${key}`,
    tenant_id: 't1',
    key,
    label: key === 'company' ? 'Empresa' : '',
    type: 'string',
    required,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
  };
}

const CATALOG = fieldCatalog(META, [attribute('company', true), attribute('city', false)]);

function validDraft(): FormDraft {
  const draft = emptyFormDraft(CATALOG);
  return {
    ...draft,
    name: 'Boletin',
    listId: 'list-1',
    texts: {
      ...draft.texts,
      consent_text: 'Acepto recibir el boletin.',
      success_message: 'Revisa tu correo para confirmar.',
    },
  };
}

describe('borrador de un formulario de suscripcion', () => {
  it('empieza con el correo obligatorio y los atributos obligatorios ya puestos', () => {
    const draft = emptyFormDraft(CATALOG);
    expect(draft.fields.map((f) => [f.key, f.required])).toEqual([
      ['email', true],
      ['company', true],
    ]);
    expect(draft.fields[1]?.label).toBe('Empresa');
  });

  it('arma el cuerpo con los textos recortados y sin redireccion como null', () => {
    const draft = validDraft();
    draft.name = '  Boletin  ';
    draft.allowedOrigins = ['https://www.tienda.test'];
    const { request, errors } = validateFormDraft(draft, LIMITS, CATALOG);
    expect(errors).toEqual({});
    expect(request).toMatchObject({
      name: 'Boletin',
      status: 'active',
      list_id: 'list-1',
      redirect_url: null,
      allowed_origins: ['https://www.tienda.test'],
    });
    expect(request?.fields.map((f) => f.key)).toEqual(['email', 'company']);
  });

  it('exige lista, consentimiento y mensaje de confirmacion aunque haya redireccion', () => {
    const draft = validDraft();
    draft.listId = '';
    draft.redirectUrl = 'https://tienda.test/gracias';
    draft.texts = { ...draft.texts, consent_text: '', success_message: '' };
    const { request, errors } = validateFormDraft(draft, LIMITS, CATALOG);
    expect(request).toBeNull();
    expect(errors.listId).toBe(t('validation.required'));
    expect(errors.consentText).toBe(t('validation.required'));
    expect(errors.successMessage).toBe(t('validation.required'));
  });

  it('no deja quitar el correo ni dejar opcional un atributo obligatorio', () => {
    const draft = validDraft();
    expect(removeField(draft.fields, 'email')).toHaveLength(draft.fields.length);

    const optional = {
      ...draft,
      fields: draft.fields.map((f) => (f.key === 'company' ? { ...f, required: false } : f)),
    };
    const result = validateFormDraft(optional, LIMITS, CATALOG);
    expect(result.request).toBeNull();
    expect(result.errors.fields).toBe(
      t('contacts.forms.error.mandatoryMissing', { keys: 'company' }),
    );

    const withoutEmail = { ...draft, fields: draft.fields.filter((f) => f.key !== 'email') };
    expect(validateFormDraft(withoutEmail, LIMITS, CATALOG).errors.fields).toBe(
      t('contacts.forms.error.emailRequired'),
    );
  });

  it('rechaza campos que no son fijos ni atributos declarados, repetidos o de mas', () => {
    const draft = validDraft();
    const unknown = {
      ...draft,
      fields: [
        ...draft.fields,
        { key: 'phone', label: 'Telefono', required: false, placeholder: '' },
      ],
    };
    expect(validateFormDraft(unknown, LIMITS, CATALOG).errors.fields).toBe(
      t('contacts.forms.error.unknownField', { keys: 'phone' }),
    );

    const first = draft.fields[0];
    if (!first) throw new Error('sin campos');
    const repeated = { ...draft, fields: [...draft.fields, first] };
    expect(validateFormDraft(repeated, LIMITS, CATALOG).errors.fields).toBe(
      t('contacts.forms.error.duplicateField'),
    );

    const extra = ['first_name', 'last_name', 'city'].map((key) => ({
      key,
      label: key,
      required: false,
      placeholder: '',
    }));
    const tooMany = { ...draft, fields: [...draft.fields, ...extra] };
    expect(validateFormDraft(tooMany, LIMITS, CATALOG).errors.fields).toBe(
      t('contacts.forms.error.tooManyFields', { n: LIMITS.max_fields }),
    );
  });

  it('exige etiqueta en cada campo', () => {
    const draft = validDraft();
    draft.fields = draft.fields.map((f) => (f.key === 'company' ? { ...f, label: '  ' } : f));
    expect(validateFormDraft(draft, LIMITS, CATALOG).errors.fieldLabels).toEqual({
      company: t('validation.required'),
    });
  });

  it('solo admite redireccion https', () => {
    const draft = validDraft();
    draft.redirectUrl = 'http://tienda.test/gracias';
    expect(validateFormDraft(draft, LIMITS, CATALOG).errors.redirectUrl).toBe(
      t('contacts.forms.error.redirectHttps'),
    );
  });

  it('valida los origenes autorizados y su tope', () => {
    const draft = validDraft();
    draft.allowedOrigins = ['https://a.test', 'https://b.test', 'https://c.test'];
    expect(validateFormDraft(draft, LIMITS, CATALOG).errors.allowedOrigins).toBe(
      t('contacts.forms.error.tooManyOrigins', { n: LIMITS.max_allowed_origins }),
    );
    draft.allowedOrigins = ['https://a.test/ruta'];
    expect(validateFormDraft(draft, LIMITS, CATALOG).errors.allowedOrigins).toBe(
      t('contacts.forms.error.origin', { origins: 'https://a.test/ruta' }),
    );
  });

  it('mueve los campos sin salirse de la lista', () => {
    const draft = validDraft();
    expect(moveField(draft.fields, 1, -1).map((f) => f.key)).toEqual(['company', 'email']);
    expect(moveField(draft.fields, 0, -1).map((f) => f.key)).toEqual(['email', 'company']);
  });

  it('recupera un formulario guardado sin correo poniendolo al principio', () => {
    const draft = draftFromForm({
      id: 'f1',
      tenant_id: 't1',
      name: 'Viejo',
      status: 'disabled',
      list_id: 'l1',
      fields: [{ key: 'first_name', label: 'Nombre', required: false, placeholder: '' }],
      texts: {
        title: '',
        description: '',
        submit_label: '',
        consent_text: 'Acepto',
        success_message: 'Gracias',
      },
      redirect_url: null,
      allowed_origins: [],
      embed: { key: 'k', iframe_url: '', script_url: '', definition_url: '', submit_url: '' },
      created_by: 'u1',
      created_at: '2026-09-01T00:00:00Z',
      updated_at: '2026-09-01T00:00:00Z',
    });
    expect(draft.fields.map((f) => f.key)).toEqual(['email', 'first_name']);
    expect(draft.status).toBe('disabled');
  });
});

describe('origenes autorizados', () => {
  it.each([
    ['https://www.tienda.test', 'https://www.tienda.test'],
    ['https://www.tienda.test/', 'https://www.tienda.test'],
    ['https://tienda.test:8443', 'https://tienda.test:8443'],
    [' https://TIENDA.test ', 'https://tienda.test'],
  ])('%s se normaliza a %s', (raw, expected) => {
    expect(normalizeOrigin(raw)).toBe(expected);
  });

  it.each([
    'http://tienda.test',
    'https://tienda.test/ruta',
    'https://tienda.test?x=1',
    'https://user:pass@tienda.test',
    'tienda.test',
    '',
  ])('%s no es un origen valido', (raw) => {
    expect(normalizeOrigin(raw)).toBeNull();
  });
});
