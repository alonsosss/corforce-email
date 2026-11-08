import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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
  /**
   * El marco crece hasta el alto de su contenido y la pagina es la unica que se desplaza.
   * Exige allow-same-origin para medirlo; sigue sin allow-scripts, y la CSP del documento
   * (default-src 'none') tambien los bloquea, asi que el HTML ajeno no puede ejecutar nada.
   * Solo para correo recibido; `height` hace de alto minimo mientras se mide.
   */
  autoHeight?: boolean;
}

const LINKS_SANDBOX = 'allow-popups allow-popups-to-escape-sandbox';
const MEASURE_SANDBOX = 'allow-same-origin';

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
  autoHeight = false,
}: HtmlPreviewFrameProps) {
  const frameRef = useRef<HTMLIFrameElement>(null);
  const observer = useRef<ResizeObserver | null>(null);
  const [measured, setMeasured] = useState<number | null>(null);
  const sandbox = [allowLinks ? LINKS_SANDBOX : '', autoHeight ? MEASURE_SANDBOX : '']
    .filter(Boolean)
    .join(' ');

  const measure = useCallback(() => {
    const doc = frameRef.current?.contentDocument;
    if (!doc?.documentElement) return;
    const px = Math.ceil(doc.documentElement.scrollHeight);
    if (Number.isFinite(px) && px > 0) setMeasured(px);
  }, []);

  const attach = useCallback(() => {
    observer.current?.disconnect();
    measure();
    const doc = frameRef.current?.contentDocument;
    if (!doc?.body || typeof ResizeObserver === 'undefined') return;
    observer.current = new ResizeObserver(measure);
    observer.current.observe(doc.body);
  }, [measure]);

  useEffect(() => () => observer.current?.disconnect(), []);

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
        hideVerticalScroll: autoHeight,
      }),
    [html, allow, inlineImages, allowLinks, remoteImageProxy, autoHeight],
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
        ref={frameRef}
        className="cf-preview-frame"
        title={title}
        sandbox={sandbox}
        referrerPolicy="no-referrer"
        srcDoc={prepared.html}
        onLoad={autoHeight ? attach : undefined}
        style={autoHeight && measured !== null ? { height: measured } : { height }}
      />
    </div>
  );
}
