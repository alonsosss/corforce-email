import { Link, useParams } from 'react-router-dom';
import { templatesApi, templatesMeta } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { ErrorState, LoadingBlock } from '@/design/components';
import { IconChevronLeft } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { EditorWorkspace } from './EditorWorkspace';
import { loadBrand } from './loadBrand';
import { baseVersionNumber, type EditorData } from './session';
import './editorPage.css';

export default function TemplateEditorPage() {
  const { id = '' } = useParams();
  const { can } = useAccess();
  const canReadKit = can(...PERMISSIONS.brandKit.read);
  const canReadAssets = can(...PERMISSIONS.templateAssets.read);

  const data = useQuery(async (): Promise<EditorData> => {
    const [template, meta] = await Promise.all([
      templatesApi.get(id).then((res) => res.data),
      templatesMeta.get(),
    ]);
    const number = baseVersionNumber(template);
    const [base, brand] = await Promise.all([
      number ? templatesApi.getVersion(id, number).then((res) => res.data) : null,
      loadBrand(canReadKit, canReadAssets),
    ]);
    return { target: { mode: 'existing', template }, meta, base, brand };
  }, [id, canReadKit, canReadAssets]);

  if (data.error) {
    return (
      <div className="cf-editor-state">
        <Link to={paths.template(id)} className="cf-page-header__back">
          <IconChevronLeft size={16} />
          {t('templates.editor.back')}
        </Link>
        <ErrorState error={data.error} title={t('templates.notFound')} onRetry={data.reload} />
      </div>
    );
  }
  if (!data.data) return <LoadingBlock />;
  return <EditorWorkspace key={id} data={data.data} />;
}
