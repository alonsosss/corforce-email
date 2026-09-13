import { Link } from 'react-router-dom';
import { EmptyState } from '@/design/components';
import { IconLock } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';

export default function ForbiddenPage() {
  return (
    <EmptyState
      icon={<IconLock size={32} />}
      title={t('page.forbidden.title')}
      description={t('page.forbidden.description')}
      action={
        <Link className="cf-btn cf-btn--secondary" to={paths.home}>
          {t('page.goHome')}
        </Link>
      }
    />
  );
}
