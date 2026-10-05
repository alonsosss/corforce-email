import { useMemo, useState } from 'react';
import {
  webmailApi,
  webmailImageProxyUrl,
  type MailMessage,
  type MessagePart,
} from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { Alert, Button, HtmlPreviewFrame, useToast } from '@/design/components';
import { IconDownload, IconImage, IconPaperclip, IconQuote } from '@/design/icons';
import { useResource } from '@/hooks/useResource';
import { saveBlob } from '@/lib/download';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import { webmailMeta } from '@/webmail/catalogs';
import { AttachmentThumbnail, canThumbnail } from './AttachmentThumbnails';
import { htmlToPlainText } from './compose';
import { displayFilename } from './format';
import { referencedInlineParts } from './inlineImages';
import { splitQuotedHtml, splitQuotedText } from './quotedHistory';

const FRAME_HEIGHT = '60vh';
const FRAME_MIN_PX = 120;
const FRAME_LINE_PX = 24;
const FRAME_PADDING_PX = 48;
const CHARS_PER_LINE = 90;

/**
 * Alto del marco segun lo que hay que leer: el iframe aislado no deja medir su contenido, asi
 * que un mensaje de solo texto se estima por lineas y uno con imagenes o tablas (boletines,
 * firmas maquetadas) se queda con el alto de siempre. Quedarse corto solo hace scroll dentro.
 */
export function estimateFrameHeight(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  if (doc.body.querySelector('img, table, video, picture, svg')) return FRAME_HEIGHT;
  const lines = htmlToPlainText(html)
    .split('\n')
    .reduce((sum, line) => sum + Math.max(1, Math.ceil(line.length / CHARS_PER_LINE)), 0);
  const px = Math.max(FRAME_MIN_PX, lines * FRAME_LINE_PX + FRAME_PADDING_PX);
  return `min(${FRAME_HEIGHT}, ${px}px)`;
}

export interface MessageBodyProps {
  message: MailMessage;
  /** Quien lee pidio las imagenes remotas de este mensaje. */
  remoteAllowed: boolean;
  remoteLoading: boolean;
  onAllowRemote: () => void;
  /** Imagenes en linea ya descargadas (useInlineImages): URL de la parte -> data:. */
  inlineImages: ReadonlyMap<string, string>;
}

/**
 * Cuerpo del mensaje. El HTML (ya saneado por el servicio) solo se pinta en el iframe
 * aislado de HtmlPreviewFrame, que lo vuelve a limpiar; el texto va en un <pre> y React lo
 * escapa. Las imagenes remotas siguen bloqueadas hasta que quien lee las pide.
 */
export function MessageBody({
  message,
  remoteAllowed,
  remoteLoading,
  onAllowRemote,
  inlineImages,
}: MessageBodyProps) {
  const [asText, setAsText] = useState(false);
  const [showQuoted, setShowQuoted] = useState(false);
  const refs = useMemo(() => referencedInlineParts(message), [message]);
  const inlineParts = new Set(refs.values());
  const attachments = message.attachments.filter((part) => !inlineParts.has(part));
  const hasHtml = message.html.trim() !== '';
  const hasText = message.text.trim() !== '';
  const plain = hasText ? message.text : hasHtml ? htmlToPlainText(message.html) : '';
  const htmlSplit = useMemo(() => splitQuotedHtml(message.html), [message.html]);
  const textSplit = useMemo(() => splitQuotedText(plain), [plain]);
  const showingHtml = hasHtml && !asText;
  const split = showingHtml ? htmlSplit : textSplit;
  const folded = split !== null && !showQuoted;
  const html = folded && htmlSplit ? htmlSplit.main : message.html;
  const frameHeight = useMemo(() => estimateFrameHeight(html), [html]);

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
      {message.text_truncated || message.html_truncated ? (
        <Alert tone="info">{t('webmail.reader.truncated')}</Alert>
      ) : null}
      {showingHtml ? (
        <>
          {message.remote_images.present ? (
            <div className="cf-preview__bar" role="status">
              <span className="cf-inline">
                <IconImage size={16} />
                {t(
                  message.remote_images.blocked
                    ? 'webmail.reader.remoteBlocked'
                    : 'webmail.reader.remoteShown',
                )}
              </span>
              {message.remote_images.blocked ? (
                <Button size="sm" loading={remoteLoading} onClick={onAllowRemote}>
                  {t('webmail.reader.showRemote')}
                </Button>
              ) : null}
            </div>
          ) : null}
          <HtmlPreviewFrame
            html={html}
            title={t('webmail.reader.bodyTitle', {
              subject: message.subject || t('webmail.noSubject'),
            })}
            height={frameHeight}
            allowRemoteImages={remoteAllowed && !message.remote_images.blocked}
            inlineImages={inlineImages}
            remoteImageProxy={webmailImageProxyUrl()}
            allowLinks
          />
        </>
      ) : plain ? (
        <pre className="cf-wm-text">{folded && textSplit ? textSplit.main : plain}</pre>
      ) : (
        <p className="cf-text-muted">{t('webmail.reader.empty')}</p>
      )}
      {split ? (
        <div>
          <Button
            size="sm"
            variant="ghost"
            className="cf-wm-compose__quoted"
            icon={<IconQuote size={14} />}
            aria-expanded={showQuoted}
            onClick={() => setShowQuoted((v) => !v)}
          >
            {t(showQuoted ? 'webmail.reader.hideQuoted' : 'webmail.reader.showQuoted')}
          </Button>
        </div>
      ) : null}
      {hasHtml ? (
        <div>
          <Button
            size="sm"
            variant="ghost"
            aria-pressed={asText}
            onClick={() => setAsText((v) => !v)}
          >
            {t(asText ? 'webmail.reader.showHtml' : 'webmail.reader.showText')}
          </Button>
        </div>
      ) : null}
      {attachments.length ? <AttachmentList message={message} parts={attachments} /> : null}
    </div>
  );
}

/**
 * Adjuntos: la descarga siempre se guarda como application/octet-stream; el navegador nunca
 * la abre ni la interpreta. Las imagenes de mapa de bits ademas se ven en miniatura.
 */
function AttachmentList({ message, parts }: { message: MailMessage; parts: MessagePart[] }) {
  const toast = useToast();
  const meta = useResource(webmailMeta);
  const [busy, setBusy] = useState<string | null>(null);
  const maxThumbnailBytes = meta.data?.limits.max_download_bytes ?? null;
  const images = parts.filter((part) => canThumbnail(part, maxThumbnailBytes));
  const files = parts.filter((part) => !images.includes(part));
  const nameOf = (part: MessagePart) =>
    displayFilename(part.filename, t('webmail.attachments.unnamed'));

  const download = async (part: MessagePart, name: string) => {
    setBusy(part.part);
    try {
      const { blob, filename } = await webmailApi.downloadPart(
        message.folder,
        message.uid,
        part.part,
      );
      saveBlob(blob, displayFilename(filename ?? '', name));
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section
      className="cf-stack"
      style={{ gap: 'var(--cf-space-2)' }}
      aria-labelledby="wm-attachments"
    >
      <h3 id="wm-attachments" className="cf-wm-section-title">
        {t('webmail.attachments.title', { n: parts.length })}
      </h3>
      {images.length ? (
        <ul className="cf-wm-thumbs">
          {images.map((part) => {
            const name = nameOf(part);
            return (
              <AttachmentThumbnail
                key={part.part}
                message={message}
                part={part}
                name={name}
                downloading={busy === part.part}
                onDownload={() => void download(part, name)}
              />
            );
          })}
        </ul>
      ) : null}
      {files.length ? (
        <ul className="cf-wm-attachments">
          {files.map((part) => {
            const name = nameOf(part);
            return (
              <li key={part.part} className="cf-wm-attachment">
                <IconPaperclip size={16} />
                <span className="cf-wm-attachment__name">{name}</span>
                <span className="cf-text-sm cf-text-muted">{formatBytes(part.size)}</span>
                <Button
                  size="sm"
                  icon={<IconDownload size={14} />}
                  loading={busy === part.part}
                  aria-label={t('webmail.attachments.downloadName', { name })}
                  onClick={() => void download(part, name)}
                >
                  {t('webmail.attachments.download')}
                </Button>
              </li>
            );
          })}
        </ul>
      ) : null}
    </section>
  );
}
