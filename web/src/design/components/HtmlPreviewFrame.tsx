import { useMemo, useState } from 'react';
import { prepareUntrustedHtml } from '@/lib/untrustedHtml';
import { IconImage } from '../icons';
import { Button } from './Button';
import { t } from '@/i18n';

export interface HtmlPreviewFrameProps {
  html: string;
  title: string;
  height?: number;
}

/**
 * Unico sitio donde se pinta HTML que no es de la aplicacion (plantillas, pies de pagina,
 * avisos): un iframe con sandbox vacio, sin scripts, formularios, ventanas, navegacion ni
 * mismo origen, y el documento en srcdoc. Ese HTML nunca entra en el DOM de la pagina.
 * Las imagenes remotas se bloquean hasta que quien mira las pide en esta vista.
 */
export function HtmlPreviewFrame({ html, title, height = 360 }: HtmlPreviewFrameProps) {
  const [showRemote, setShowRemote] = useState(false);
  const prepared = useMemo(
    () => prepareUntrustedHtml(html, { allowRemoteImages: showRemote }),
    [html, showRemote],
  );

  return (
    <div className="cf-preview">
      {prepared.remoteImages > 0 ? (
        <div className="cf-preview__bar" role="status">
          <span className="cf-inline">
            <IconImage size={16} />
            {showRemote
              ? t('preview.remoteShown', { n: prepared.remoteImages })
              : t('preview.remoteBlocked', { n: prepared.remoteImages })}
          </span>
          <Button size="sm" aria-pressed={showRemote} onClick={() => setShowRemote((v) => !v)}>
            {showRemote ? t('preview.hideRemote') : t('preview.showRemote')}
          </Button>
        </div>
      ) : null}
      <iframe
        className="cf-preview-frame"
        title={title}
        sandbox=""
        referrerPolicy="no-referrer"
        srcDoc={prepared.html}
        style={{ height }}
      />
    </div>
  );
}
