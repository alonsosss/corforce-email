import type { TemplateVariable, VariableType } from '@/api/templates';
import { t } from '@/i18n';

// Vista previa de la galeria: las plantillas llevan acciones de plantilla ({{.first_name}},
// {{if .x}}...{{end}}) que el servidor resuelve al enviar. En la miniatura se sustituyen por
// valores de ejemplo para que se vea el correo y no el codigo. Solo para la vista previa: lo que
// se guarda y se envia es la plantilla sin tocar.

const IF_BLOCK = /\{\{\s*if\s+\.([a-z][a-z0-9_]*)\s*\}\}([\s\S]*?)(?:\{\{\s*else\s*\}\}([\s\S]*?))?\{\{\s*end\s*\}\}/g;
const FIELD = /\{\{\s*\.([a-z][a-z0-9_]*)\s*\}\}/g;

function sampleFor(name: string, type: VariableType | undefined): string {
  if (name === 'first_name') return t('templates.gallery.sampleFirstName');
  if (name === 'recipient_email' || type === 'email') return t('templates.gallery.sampleEmail');
  if (name === 'unsubscribe_url' || name === 'view_in_browser_url' || type === 'url') return '#';
  if (type === 'number') return '1024';
  return t('templates.gallery.sampleValue');
}

/** Sustituye las acciones de plantilla del HTML compilado por valores de ejemplo. */
export function samplePreview(html: string, variables: readonly TemplateVariable[]): string {
  const types = new Map(variables.map((v) => [v.name, v.type] as const));
  const withBranches = html.replace(IF_BLOCK, (_m, _name: string, then: string) => then);
  return withBranches.replace(FIELD, (_m, name: string) => sampleFor(name, types.get(name)));
}
