import { useMemo, useState } from 'react';
import { prepareUntrustedHtml } from '@/lib/untrustedHtml';
import { IconImage } from '../icons';
import { Button } from './Button';
import { t } from '@/i18n';

export interface HtmlPreviewFrameProps {
  html: string;
  title: string;
  /** Alto del marco: pixeles o cualquier longitud CSS. */
  height?: number | string;
  /**
   * Decision sobre las imagenes remotas tomada fuera del marco (el webmail: el servicio ya
   * las bloquea y quien lee las pide por mensaje). Sin ella, el propio marco ofrece
   * mostrarlas en esta vista.
   */
  allowRemoteImages?: boolean;
  /** Imagenes propias del documento ya resueltas a data: (las cid: de un correo). */
  inlineImages?: ReadonlyMap<string, string>;
  /**
   * Deja abrir los enlaces en una pestana nueva (allow-popups). El marco sigue sin
   * scripts, formularios, navegacion de la pagina ni mismo origen. Solo para correo
   * recibido, cuyos enlaces ya saneo el servicio (http, https y mailto, noopener).
   */
  allowLinks?: boolean;
  /** Proxy de imagenes del mismo origen: mostrar las remotas solo admite las que pasan por el. */
  remoteImageProxy?: string;
}

const LINKS_SANDBOX = 'allow-popups allow-popups-to-escape-sandbox';

/**
 * Unico sitio donde se pinta HTML que no es de la aplicacion (plantillas, pies de pagina,
 * avisos, correo): un iframe con sandbox sin scripts, formularios, navegacion de la pagina
 * ni mismo origen, y el documento en srcdoc. Ese HTML nunca entra en el DOM de la pagina.
 * Las imagenes remotas se bloquean hasta que quien mira las pide.
 */
export function HtmlPreviewFrame({
  html,
  title,
  height = 360,
  allowRemoteImages,
  inlineImages,
  allowLinks = false,
  remoteImageProxy,
}: HtmlPreviewFrameProps) {
  const [showRemote, setShowRemote] = useState(false);
  const controlled = allowRemoteImages !== undefined;
  const allow = controlled ? allowRemoteImages : showRemote;
  const prepared = useMemo(
    () =>
      prepareUntrustedHtml(html, {
        allowRemoteImages: allow,
        inlineImages,
        openLinksInNewTab: allowLinks,
        remoteImageProxy,
      }),
    [html, allow, inlineImages, allowLinks, remoteImageProxy],
  );

  return (
    <div className="cf-preview">
      {!controlled && prepared.remoteImages > 0 ? (
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
        sandbox={allowLinks ? LINKS_SANDBOX : ''}
        referrerPolicy="no-referrer"
        srcDoc={prepared.html}
        style={{ height }}
      />
    </div>
  );
}
