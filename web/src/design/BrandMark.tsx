import { IconMail } from './icons';
import { t } from '@/i18n';

/** Marca del producto: la usan la barra lateral y las pantallas de acceso. */
export function BrandMark({ withTagline = false }: { withTagline?: boolean }) {
  return (
    <>
      <span className="cf-sidebar__mark" aria-hidden="true">
        <IconMail size={16} strokeWidth={2} />
      </span>
      <span>
        <span className="cf-sidebar__name">{t('app.name')}</span>
        {withTagline ? (
          <>
            <br />
            <span className="cf-sidebar__tagline">{t('app.tagline')}</span>
          </>
        ) : null}
      </span>
    </>
  );
}
