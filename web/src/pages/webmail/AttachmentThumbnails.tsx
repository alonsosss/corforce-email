import { useEffect, useRef, useState, type RefObject } from 'react';
import { webmailApi, type MailMessage, type MessagePart } from '@/api/webmail';
import { Button, Modal } from '@/design/components';
import { IconDownload, IconImage } from '@/design/icons';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import { isBitmapImageType } from './inlineImages';

/**
 * Adjunto que se muestra en miniatura: un mapa de bits declarado como tal y dentro del tope de
 * descarga que sirve la meta. Sin la meta no hay tope conocido y no se ofrece ninguna.
 */
export function canThumbnail(part: MessagePart, maxBytes: number | null): boolean {
  return maxBytes !== null && part.size <= maxBytes && isBitmapImageType(part.content_type);
}

type Thumbnail = { status: 'pending' } | { status: 'ready'; url: string } | { status: 'failed' };

function useVisible(ref: RefObject<HTMLElement>): boolean {
  const [visible, setVisible] = useState(() => typeof IntersectionObserver === 'undefined');
  useEffect(() => {
    const node = ref.current;
    if (visible || !node) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { rootMargin: '200px' },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [ref, visible]);
  return visible;
}

/**
 * Los bytes se piden con la sesion del buzon solo cuando la miniatura llega a verse. Se pintan
 * como URL de objeto con el tipo que devuelve el servicio, que se vuelve a comprobar: lo que no
 * sea un mapa de bits (un SVG, por ejemplo) no se pinta nunca.
 */
function useThumbnail(message: MailMessage, part: string, visible: boolean): Thumbnail {
  const [thumbnail, setThumbnail] = useState<Thumbnail>({ status: 'pending' });
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let url: string | null = null;
    webmailApi.downloadPart(message.folder, message.uid, part, controller.signal).then(
      ({ blob, contentType }) => {
        if (controller.signal.aborted) return;
        if (!isBitmapImageType(contentType)) {
          setThumbnail({ status: 'failed' });
          return;
        }
        url = URL.createObjectURL(new Blob([blob], { type: contentType }));
        setThumbnail({ status: 'ready', url });
      },
      () => {
        if (!controller.signal.aborted) setThumbnail({ status: 'failed' });
      },
    );
    return () => {
      controller.abort();
      if (url) URL.revokeObjectURL(url);
    };
  }, [message.folder, message.uid, part, visible]);
  return thumbnail;
}

export interface AttachmentThumbnailProps {
  message: MailMessage;
  part: MessagePart;
  name: string;
  downloading: boolean;
  onDownload: () => void;
}

export function AttachmentThumbnail({
  message,
  part,
  name,
  downloading,
  onDownload,
}: AttachmentThumbnailProps) {
  const ref = useRef<HTMLLIElement>(null);
  const visible = useVisible(ref);
  const thumbnail = useThumbnail(message, part.part, visible);
  const [viewing, setViewing] = useState(false);
  const ready = thumbnail.status === 'ready' ? thumbnail : null;

  return (
    <li ref={ref} className="cf-wm-thumb">
      <button
        type="button"
        className="cf-wm-thumb__preview"
        disabled={!ready}
        aria-label={t('webmail.attachments.view', { name })}
        onClick={() => setViewing(true)}
      >
        {ready ? (
          <img src={ready.url} alt="" />
        ) : (
          <span
            role={thumbnail.status === 'failed' ? 'img' : undefined}
            aria-label={
              thumbnail.status === 'failed'
                ? t('webmail.attachments.previewUnavailable')
                : undefined
            }
          >
            <IconImage size={24} />
          </span>
        )}
      </button>
      <div className="cf-wm-thumb__meta">
        <span className="cf-wm-thumb__name">{name}</span>
        <span className="cf-text-sm cf-text-muted">{formatBytes(part.size)}</span>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon={<IconDownload size={14} />}
          loading={downloading}
          onClick={onDownload}
        >
          {t('webmail.attachments.downloadName', { name })}
        </Button>
      </div>
      {ready ? (
        <Modal
          open={viewing}
          size="lg"
          title={name}
          onClose={() => setViewing(false)}
          footer={
            <Button icon={<IconDownload size={16} />} loading={downloading} onClick={onDownload}>
              {t('webmail.attachments.download')}
            </Button>
          }
        >
          <img className="cf-wm-viewer__image" src={ready.url} alt={name} />
        </Modal>
      ) : null}
    </li>
  );
}
