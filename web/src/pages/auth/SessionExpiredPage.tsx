import { useEffect } from 'react';
import { Link, Navigate } from 'react-router-dom';
import { useAuth } from '@/auth/useAuth';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AuthLayout } from './AuthLayout';

export default function SessionExpiredPage() {
  const { status, acknowledgeExpired } = useAuth();

  useEffect(() => {
    acknowledgeExpired();
  }, [acknowledgeExpired]);

  if (status === 'authenticated') return <Navigate to={paths.home} replace />;

  return (
    <AuthLayout title={t('auth.expired.title')} subtitle={t('auth.expired.description')}>
      <Link className="cf-btn cf-btn--primary cf-btn--block" to={paths.login}>
        {t('auth.expired.login')}
      </Link>
    </AuthLayout>
  );
}
