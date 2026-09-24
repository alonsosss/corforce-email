import { Link, useParams } from 'react-router-dom';
import { formsApi } from '@/api/forms';
import { PICKER_PAGE_SIZE } from '@/api/paging';
import { pagesApi, pagesMeta } from '@/api/pages';
import { templatesMeta } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { ErrorState, LoadingBlock } from '@/design/components';
import { IconChevronLeft } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { loadBrand } from '../../editor/loadBrand';
import { basePageVersion } from './pageContent';
import { PageEditorWorkspace, type PageEditorData } from './PageEditorWorkspace';
import '../../editor/editorPage.css';

export default function PageEditorPage() {
  const { id = '' } = useParams();
  const { can } = useAccess();
  const canReadKit = can(...PERMISSIONS.brandKit.read);
  const canReadAssets = can(...PERMISSIONS.templateAssets.read);
  const canReadForms = can(...PERMISSIONS.subscriptionForms.read);

  const data = useQuery(async (): Promise<PageEditorData> => {
    const [detail, meta] = await Promise.all([pagesApi.get(id), pagesMeta.get()]);
    const number = basePageVersion(detail);
    const [base, brand, fonts, forms] = await Promise.all([
      number ? pagesApi.getVersion(id, number) : null,
      loadBrand(canReadKit, canReadAssets),
      templatesMeta.get().then((m) => m.brand_fonts ?? []),
      canReadForms
        ? formsApi.list({ page: 1, per_page: PICKER_PAGE_SIZE }).then((p) => p.items)
        : null,
    ]);
    return { detail, meta, base, brand, fonts, forms };
  }, [id, canReadKit, canReadAssets, canReadForms]);

  if (data.error) {
    return (
      <div className="cf-editor-state">
        <Link to={paths.landingPage(id)} className="cf-page-header__back">
          <IconChevronLeft size={16} />
          {t('templates.pages.editor.back')}
        </Link>
        <ErrorState
          error={data.error}
          title={t('templates.pages.notFound')}
          onRetry={data.reload}
        />
      </div>
    );
  }
  if (!data.data) return <LoadingBlock />;
  return <PageEditorWorkspace key={id} data={data.data} />;
}
