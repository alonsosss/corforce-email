import { Link, useLocation } from 'react-router-dom';
import { templatesMeta } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { Alert, ErrorState, LoadingBlock } from '@/design/components';
import { IconChevronLeft } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { EditorWorkspace } from './EditorWorkspace';
import { loadBrand } from './loadBrand';
import { readNewDraft, type EditorData } from './session';
import './editorPage.css';

/**
 * Editor de una plantilla que aun no existe: la abre el alta ("Desde la galeria" o "En
 * blanco") con el nombre, la descripcion y el tipo en el estado de la navegacion. La plantilla
 * se crea con su primer diseno al guardar.
 */
export default function NewTemplateEditorPage() {
  const location = useLocation();
  const { can } = useAccess();
  const canCreate = can(...PERMISSIONS.templates.create);
  const canReadKit = can(...PERMISSIONS.brandKit.read);
  const canReadAssets = can(...PERMISSIONS.templateAssets.read);

  const data = useQuery(async (): Promise<EditorData | null> => {
    const meta = await templatesMeta.get();
    const draft = readNewDraft(location.state, meta.kinds);
    if (!draft) return null;
    const brand = await loadBrand(canReadKit, canReadAssets);
    return { target: { mode: 'new', draft }, meta, base: null, brand };
  }, [location.state, canReadKit, canReadAssets]);

  const back = (
    <Link to={paths.templates} className="cf-page-header__back">
      <IconChevronLeft size={16} />
      {t('nav.templates')}
    </Link>
  );
  if (data.error) {
    return (
      <div className="cf-editor-state">
        {back}
        <ErrorState error={data.error} onRetry={data.reload} />
      </div>
    );
  }
  if (data.loading && !data.data) return <LoadingBlock />;
  if (!data.data || !canCreate) {
    return (
      <div className="cf-editor-state">
        {back}
        <Alert tone="info">
          {canCreate ? t('templates.editor.newMissing') : t('templates.editor.readOnly')}
        </Alert>
      </div>
    );
  }
  return <EditorWorkspace data={data.data} />;
}
