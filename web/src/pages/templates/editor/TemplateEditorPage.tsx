import { Link, useParams } from 'react-router-dom';
import { errorMessage } from '@/api/messages';
import { templatesApi, templatesMeta } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { ErrorState, LoadingBlock } from '@/design/components';
import { IconChevronLeft } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { findAsset } from '../assets';
import { EditorWorkspace } from './EditorWorkspace';
import { baseVersionNumber, type BrandContext, type EditorData } from './session';
import './editorPage.css';

async function loadBrand(canReadKit: boolean, canReadAssets: boolean): Promise<BrandContext> {
  if (!canReadKit) {
    return { kit: null, logo: null, unavailable: t('templates.brandKit.noPermission') };
  }
  try {
    const kit = await templatesApi.brandKit();
    const logo = kit.logo_asset_id && canReadAssets ? await findAsset(kit.logo_asset_id) : null;
    return { kit, logo, unavailable: null };
  } catch (err) {
    return { kit: null, logo: null, unavailable: errorMessage(err) };
  }
}

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
    return { template, meta, base, brand };
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
  return <EditorWorkspace key={data.data.template.id} data={data.data} />;
}
