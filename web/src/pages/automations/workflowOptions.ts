import { campaignsApi } from '@/api/campaigns';
import { contactsApi } from '@/api/contacts';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { segmentsApi, type SegmentCatalog } from '@/api/segments';
import { templatesApi, type Template, type TemplateKind } from '@/api/templates';

export interface NamedOption {
  id: string;
  name: string;
}

/** Lo que el editor necesita para nombrar plantillas, listas y campanas. null sin permiso. */
export interface WorkflowOptions {
  /** Plantillas activas del tipo que exigen los pasos de envio. */
  templates: Template[] | null;
  lists: NamedOption[] | null;
  campaigns: NamedOption[] | null;
  /** Segmentos para las ramas por segmento. */
  segments: NamedOption[] | null;
  /** Catalogo de segmentos de contacts: atributos declarados y operadores por tipo, para las
   * ramas por atributo y el disparador por fecha. */
  catalog: SegmentCatalog | null;
}

export interface WorkflowOptionAccess {
  templates: boolean;
  lists: boolean;
  campaigns: boolean;
  segments: boolean;
}

const firstPage = { page: 1, per_page: PICKER_PAGE_SIZE };

const named = (items: { id: string; name: string }[]): NamedOption[] =>
  items.map(({ id, name }) => ({ id, name }));

/** Plantillas activas de un tipo (el que exige cada uso segun GET /automations/meta). */
export async function loadActiveTemplates(kind: TemplateKind): Promise<Template[]> {
  return (await templatesApi.list({ ...firstPage, kind, status: 'active' })).items;
}

export async function loadWorkflowOptions(
  access: WorkflowOptionAccess,
  templateKind: TemplateKind,
): Promise<WorkflowOptions> {
  const [templates, lists, campaigns, segments, catalog] = await Promise.all([
    access.templates ? loadActiveTemplates(templateKind) : Promise.resolve(null),
    access.lists
      ? contactsApi.listLists(firstPage).then((p) => named(p.items))
      : Promise.resolve(null),
    access.campaigns
      ? campaignsApi.list(firstPage).then((p) => named(p.items))
      : Promise.resolve(null),
    access.segments
      ? segmentsApi.list(firstPage).then((p) => named(p.items))
      : Promise.resolve(null),
    access.segments ? segmentsApi.meta() : Promise.resolve(null),
  ]);
  return { templates, lists, campaigns, segments, catalog };
}

export function optionName(options: NamedOption[] | null, id: string): string {
  return options?.find((o) => o.id === id)?.name ?? id;
}

/** Opciones de un selector, con el valor actual aunque no este en la primera pagina. */
export function namedSelectOptions(
  options: NamedOption[],
  current: string,
): { value: string; label: string }[] {
  const out = options.map((o) => ({ value: o.id, label: o.name }));
  if (current && !out.some((o) => o.value === current)) {
    out.push({ value: current, label: current });
  }
  return out;
}
