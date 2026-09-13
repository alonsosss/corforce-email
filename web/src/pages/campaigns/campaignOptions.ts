import type { Template } from '@/api/templates';
import { templatesApi } from '@/api/templates';
import { contactsApi } from '@/api/contacts';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { segmentsApi } from '@/api/segments';
import type { AudienceOptions, NamedOption } from './AudiencePicker';

export interface CampaignOptions extends AudienceOptions {
  /** Plantillas de marketing activas; null si el rol no puede leer plantillas. */
  templates: Template[] | null;
}

export interface OptionAccess {
  templates: boolean;
  lists: boolean;
  segments: boolean;
}

const named = (items: { id: string; name: string }[]): NamedOption[] =>
  items.map(({ id, name }) => ({ id, name }));

/** Lo que el formulario y el detalle necesitan para nombrar plantilla, listas y segmentos. */
export async function loadCampaignOptions(access: OptionAccess): Promise<CampaignOptions> {
  const page = { page: 1, per_page: PICKER_PAGE_SIZE };
  const [templates, lists, segments] = await Promise.all([
    access.templates
      ? templatesApi.list({ ...page, kind: 'marketing', status: 'active' }).then((p) => p.items)
      : Promise.resolve(null),
    access.lists ? contactsApi.listLists(page).then((p) => named(p.items)) : Promise.resolve(null),
    access.segments ? segmentsApi.list(page).then((p) => named(p.items)) : Promise.resolve(null),
  ]);
  return { templates, lists, segments };
}

export function nameOf(options: NamedOption[] | null, id: string): string {
  return options?.find((o) => o.id === id)?.name ?? id;
}
