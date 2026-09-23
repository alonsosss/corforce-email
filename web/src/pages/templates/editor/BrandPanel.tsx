import { Link } from 'react-router-dom';
import type { BrandKit, TemplateAsset } from '@/api/templates';
import { Alert, Button } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';

export interface BrandPanelProps {
  /** null: sin permiso para leer el kit o no se pudo cargar (lo explica `unavailable`). */
  kit: BrandKit | null;
  unavailable: string | null;
  logo: TemplateAsset | null;
  onInsertLogo: () => void;
  onInsertFooter: () => void;
  readOnly: boolean;
}

/** Kit de marca en el editor: paleta del selector de color, logo y pie legal insertables. */
export function BrandPanel({
  kit,
  unavailable,
  logo,
  onInsertLogo,
  onInsertFooter,
  readOnly,
}: BrandPanelProps) {
  if (!kit) {
    return <Alert tone="warning">{unavailable ?? t('templates.brandKit.unavailable')}</Alert>;
  }
  const missingAddress = !kit.footer.address.trim();
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <span className="cf-text-sm cf-text-secondary">{t('templates.editor.brandHint')}</span>
      {kit.updated_at === null ? <Alert tone="info">{t('templates.brandKit.empty')}</Alert> : null}
      <div className="cf-field">
        <span className="cf-field__label">{t('templates.brandKit.colors')}</span>
        {kit.colors.length ? (
          <div className="cf-swatches">
            {kit.colors.map((color) => (
              <span key={color} className="cf-swatch" title={color}>
                <span
                  className="cf-swatch__chip"
                  style={{ background: color }}
                  aria-hidden="true"
                />
                <code className="cf-mono cf-text-sm">{color}</code>
              </span>
            ))}
          </div>
        ) : (
          <span className="cf-text-sm cf-text-muted">{t('common.none')}</span>
        )}
      </div>
      <div className="cf-field">
        <span className="cf-field__label">{t('templates.brandKit.fonts')}</span>
        <span className="cf-text-sm">
          {kit.fonts.length ? kit.fonts.join(', ') : t('common.none')}
        </span>
      </div>
      <div className="cf-field">
        <span className="cf-field__label">{t('templates.brandKit.logo')}</span>
        {logo ? (
          <div className="cf-brand-logo">
            <img src={logo.url} alt={logo.name} />
          </div>
        ) : (
          <span className="cf-text-sm cf-text-muted">{t('templates.brandKit.noLogo')}</span>
        )}
        {logo && !readOnly ? (
          <div>
            <Button size="sm" onClick={onInsertLogo}>
              {t('templates.editor.insertLogo')}
            </Button>
          </div>
        ) : null}
      </div>
      {missingAddress ? (
        <Alert tone="warning">{t('templates.brandKit.missingAddress')}</Alert>
      ) : null}
      {!readOnly ? (
        <div>
          <Button size="sm" onClick={onInsertFooter}>
            {t('templates.editor.insertFooter')}
          </Button>
        </div>
      ) : null}
      <Link to={paths.brandKit} className="cf-text-sm">
        {t('templates.editor.editBrandKit')}
      </Link>
    </div>
  );
}
