import type { MessagePart, WebmailMeta } from '@/api/webmail';
import { Button } from '@/design/components';
import { IconPaperclip, IconX } from '@/design/icons';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import { displayFilename } from './format';

/**
 * Adjuntos: los del mensaje de origen, que el servicio toma del buzon, y los ficheros que
 * se suben. Numero y tamano maximos llegan en la meta del servicio.
 */
export function AttachmentPicker({
  files,
  serverParts,
  onFiles,
  onServerParts,
  limits,
  error,
  disabled,
}: {
  files: File[];
  serverParts: readonly MessagePart[];
  onFiles: (files: File[]) => void;
  onServerParts: (parts: MessagePart[]) => void;
  limits: WebmailMeta['limits'] | null;
  error: string | null;
  disabled: boolean;
}) {
  const count = files.length + serverParts.length;
  const total =
    files.reduce((sum, file) => sum + file.size, 0) +
    serverParts.reduce((sum, part) => sum + part.size, 0);
  const unnamed = t('webmail.attachments.unnamed');
  const hint = [
    count
      ? t('webmail.compose.attachmentsTotal', { n: count, size: formatBytes(total) })
      : t('webmail.compose.attachmentsHint'),
    limits
      ? t('webmail.compose.attachmentsLimits', {
          n: limits.max_attachments,
          size: formatBytes(limits.max_message_bytes),
        })
      : '',
  ]
    .filter(Boolean)
    .join(' ');
  const item = (key: string, name: string, size: number, onRemove: () => void) => (
    <li key={key} className="cf-wm-attachment">
      <IconPaperclip size={16} />
      <span className="cf-wm-attachment__name">{name}</span>
      <span className="cf-text-sm cf-text-muted">{formatBytes(size)}</span>
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        icon={<IconX size={14} />}
        disabled={disabled}
        onClick={onRemove}
      >
        {t('webmail.compose.removeAttachment', { name })}
      </Button>
    </li>
  );
  return (
    <div className="cf-field">
      <label className="cf-field__label" htmlFor="compose-files">
        {t('webmail.compose.attachments')}
      </label>
      <input
        id="compose-files"
        type="file"
        multiple
        className="cf-input cf-input--file"
        disabled={disabled}
        aria-describedby="compose-files-hint"
        onChange={(e) => {
          const picked = Array.from(e.target.files ?? []);
          if (picked.length) onFiles([...files, ...picked]);
          e.target.value = '';
        }}
      />
      {count ? (
        <ul className="cf-wm-attachments">
          {serverParts.map((part) =>
            item(`s:${part.part}`, displayFilename(part.filename, unnamed), part.size, () =>
              onServerParts(serverParts.filter((p) => p.part !== part.part)),
            ),
          )}
          {files.map((file, index) =>
            item(
              `f:${index}:${file.name}:${file.size}`,
              displayFilename(file.name, unnamed),
              file.size,
              () => onFiles(files.filter((_, i) => i !== index)),
            ),
          )}
        </ul>
      ) : null}
      <span id="compose-files-hint" className="cf-field__hint">
        {hint}
      </span>
      {error ? (
        <div className="cf-form__error" role="alert">
          {error}
        </div>
      ) : null}
    </div>
  );
}
