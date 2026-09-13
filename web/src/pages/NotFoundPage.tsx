import { Link } from 'react-router-dom';
import { EmptyState } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';

export default function NotFoundPage() {
  return (
    <EmptyState
      title={t('page.notFound.title')}
      description={t('page.notFound.description')}
      action={
        <Link className="cf-btn cf-btn--secondary" to={paths.home}>
          {t('page.goHome')}
        </Link>
      }
    />
  );
}
