import { AuthLayout } from '@/pages/auth/AuthLayout';
import { t } from '@/i18n';

/**
 * Lo unico que ve un navegador automatizado: por que no entra, una referencia para pedir revision
 * y como seguir. Sin enlaces a la aplicacion ni datos de la sesion.
 */
export function AutomationBlockedPage({ reference }: { reference: string }) {
  return (
    <AuthLayout
      title={t('security.automation.title')}
      subtitle={t('security.automation.description')}
    >
      <p className="cf-text-sm cf-text-secondary">{t('security.automation.help')}</p>
      <p className="cf-text-sm">
        {t('security.automation.reference')} <code className="cf-mono">{reference}</code>
      </p>
      <a className="cf-btn cf-btn--primary cf-btn--block" href="/">
        {t('security.automation.retry')}
      </a>
    </AuthLayout>
  );
}
