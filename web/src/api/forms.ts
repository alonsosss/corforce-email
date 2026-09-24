import { api } from './client';
import { endpoints } from './endpoints';
import { fetchPage } from './paging';
import { cachedResource } from './resource';
import type { AttributeType } from './contacts';
import type { Page, PageQuery } from './types';

// Formularios de suscripcion de services/contacts (docs/Plan_Marketing_Avanzado.md, 2-F). Los
// campos admitidos, los topes y la proteccion anti-abuso llegan en GET /contacts/forms/meta.

export type FormStatus = 'active' | 'disabled';

export interface FormField {
  /** email, first_name, last_name o la clave de un atributo declarado. */
  key: string;
  label: string;
  required: boolean;
  placeholder: string;
}

export interface FormTexts {
  title: string;
  description: string;
  submit_label: string;
  /** Texto que la persona acepta: queda como evidencia del consentimiento. */
  consent_text: string;
  success_message: string;
}

/** Direcciones publicas del formulario, calculadas por el servicio. */
export interface FormEmbed {
  key: string;
  iframe_url: string;
  script_url: string;
  /** GET publico con la definicion; CORS solo para allowed_origins. */
  definition_url: string;
  /** POST publico JSON {token, fields, consent, homepage}. */
  submit_url: string;
}

export interface SubscriptionForm {
  id: string;
  tenant_id: string;
  name: string;
  status: FormStatus;
  list_id: string;
  fields: FormField[];
  texts: FormTexts;
  redirect_url: string | null;
  allowed_origins: string[];
  embed: FormEmbed;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface CreateFormRequest {
  name: string;
  status?: FormStatus;
  list_id: string;
  fields: FormField[];
  texts: FormTexts;
  redirect_url?: string | null;
  allowed_origins?: string[];
}

/** PATCH: un campo ausente no cambia; redirect_url null quita la redireccion. */
export type UpdateFormRequest = Partial<CreateFormRequest>;

export interface FormStatsDay {
  date: string;
  submitted: number;
  confirmed: number;
}

/** submitted son los envios validos; confirmed, los que confirmaron el doble opt-in. */
export interface FormStats {
  from: string;
  to: string;
  submitted: number;
  confirmation_sent: number;
  already_subscribed: number;
  not_reachable: number;
  confirmed: number;
  daily: FormStatsDay[];
}

export interface BuiltinFormField {
  key: string;
  type: AttributeType | 'email';
}

export interface FormLimits {
  max_fields: number;
  max_name_length: number;
  max_label_length: number;
  max_placeholder_length: number;
  max_title_length: number;
  max_description_length: number;
  max_submit_label_length: number;
  max_consent_text_length: number;
  max_success_message_length: number;
  max_allowed_origins: number;
  max_redirect_url_length: number;
  /** Tope de dias de GET /contacts/forms/{id}/stats. */
  max_stats_days: number;
}

export interface FormsMeta {
  builtin_fields: BuiltinFormField[];
  statuses: FormStatus[];
  limits: FormLimits;
  min_fill_seconds: number;
  token_ttl_seconds: number;
}

function toForm(data: SubscriptionForm): SubscriptionForm {
  return { ...data, fields: data.fields ?? [], allowed_origins: data.allowed_origins ?? [] };
}

function toStats(data: FormStats): FormStats {
  return { ...data, daily: data.daily ?? [] };
}

export const formsApi = {
  list: async (query: PageQuery): Promise<Page<SubscriptionForm>> => {
    const page = await fetchPage<SubscriptionForm>(endpoints.contacts.forms.collection, {
      ...query,
    });
    return { ...page, items: page.items.map(toForm) };
  },
  get: async (id: string): Promise<SubscriptionForm> =>
    toForm((await api.get<SubscriptionForm>(endpoints.contacts.forms.byId(id))).data),
  create: async (input: CreateFormRequest): Promise<SubscriptionForm> =>
    toForm(
      (await api.post<SubscriptionForm>(endpoints.contacts.forms.collection, { body: input })).data,
    ),
  update: async (id: string, input: UpdateFormRequest): Promise<SubscriptionForm> =>
    toForm(
      (await api.patch<SubscriptionForm>(endpoints.contacts.forms.byId(id), { body: input })).data,
    ),
  remove: (id: string) => api.delete<null>(endpoints.contacts.forms.byId(id)),
  stats: async (id: string, days: number): Promise<FormStats> =>
    toStats(
      (await api.get<FormStats>(endpoints.contacts.forms.stats(id), { params: { days } })).data,
    ),
  meta: async (): Promise<FormsMeta> =>
    (await api.get<FormsMeta>(endpoints.contacts.forms.meta)).data,
};

/** Catalogo de formularios compartido por todas las pantallas de la sesion. */
export const formsMeta = cachedResource(formsApi.meta);
