import { useMemo } from 'react';
import { Badge, Button, HtmlPreviewFrame, Modal } from '@/design/components';
import { t, tEnum } from '@/i18n';
import { GALLERY, type GalleryTemplate } from '../gallery';
import type { BrandTokens } from './brand';
import type { CompileResult } from './mjml';

export interface GalleryModalProps {
  brand: BrandTokens;
  compile: (mjml: string) => CompileResult;
  onChoose: (template: GalleryTemplate, mjml: string) => void;
  onClose: () => void;
}

/** Plantillas predisenadas generadas con el kit de marca, con su vista previa compilada. */
export function GalleryModal({ brand, compile, onChoose, onClose }: GalleryModalProps) {
  const items = useMemo(
    () =>
      GALLERY.map((template) => {
        const mjml = template.build(brand);
        return { template, mjml, html: compile(mjml).html };
      }),
    [brand, compile],
  );

  return (
    <Modal
      open
      size="lg"
      title={t('templates.gallery.title')}
      onClose={onClose}
      footer={<Button onClick={onClose}>{t('common.close')}</Button>}
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <span className="cf-text-sm cf-text-secondary">{t('templates.gallery.hint')}</span>
        <ul className="cf-gallery">
          {items.map(({ template, mjml, html }) => (
            <li key={template.id} className="cf-gallery__item">
              <HtmlPreviewFrame html={html} title={t(template.name)} height={260} />
              <div className="cf-gallery__meta">
                <div className="cf-inline">
                  <strong>{t(template.name)}</strong>
                  <Badge tone="neutral">{tEnum('templates.kind', template.kind)}</Badge>
                </div>
                <span className="cf-text-sm cf-text-secondary">{t(template.description)}</span>
                <div>
                  <Button size="sm" variant="primary" onClick={() => onChoose(template, mjml)}>
                    {t('templates.gallery.use')}
                  </Button>
                </div>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </Modal>
  );
}
