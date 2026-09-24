import type { LandingPage } from '@/api/pages';
import { CopyButton } from '@/design/components';
import { t } from '@/i18n';
import { isPublished } from './pageStatus';

/** URL publica de la pagina: enlace solo si hay una version publicada que servir. */
export function PublicUrl({
  page,
  copyable = false,
}: {
  page: Pick<LandingPage, 'public_url' | 'current_version'>;
  copyable?: boolean;
}) {
  if (!isPublished(page)) {
    return <span className="cf-text-muted">{t('templates.pages.notServed')}</span>;
  }
  return (
    <span className="cf-inline">
      <a
        href={page.public_url}
        target="_blank"
        rel="noopener noreferrer"
        className="cf-mono cf-break"
        onClick={(e) => e.stopPropagation()}
      >
        {page.public_url}
      </a>
      {copyable ? <CopyButton value={page.public_url} /> : null}
    </span>
  );
}
