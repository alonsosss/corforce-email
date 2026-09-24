import { useRef, useState } from 'react';
import { largeFilesApi, type LargeFile, type LargeFileListing } from '@/api/largeFiles';
import { errorMessage } from '@/api/messages';
import { Button, Input, Select } from '@/design/components';
import { IconLink } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { t } from '@/i18n';
import { formatBytes } from '@/lib/quota';
import { displayFilename } from './format';
import { expiryOptions, largeFileProblem } from './largeFiles';

/**
 * Ficheros grandes por enlace en la redaccion: sube el fichero a mail-files y entrega su enlace para
 * el cuerpo del mensaje. Tambien ofrece convertir en enlace los adjuntos que no caben en el mensaje.
 * Sin la funcion disponible en el servidor no se muestra.
 */
export function LargeFilePicker({
  suggest,
  onShared,
  disabled,
}: {
  /** Adjuntos que se pueden convertir en enlace (los que hacen que el mensaje no quepa). */
  suggest: readonly File[];
  /** El fichero ya esta publicado; source es el adjunto convertido, si lo era. */
  onShared: (file: LargeFile, source?: File) => void;
  disabled: boolean;
}) {
  const listing = useQuery((signal) => largeFilesApi.list(signal), []);
  if (!listing.data?.enabled) return null;
  return (
    <LargeFileForm
      listing={listing.data}
      suggest={suggest}
      disabled={disabled}
      onShared={(file, source) => {
        listing.reload();
        onShared(file, source);
      }}
    />
  );
}

function LargeFileForm({
  listing,
  suggest,
  onShared,
  disabled,
}: {
  listing: LargeFileListing;
  suggest: readonly File[];
  onShared: (file: LargeFile, source?: File) => void;
  disabled: boolean;
}) {
  const { limits } = listing;
  const input = useRef<HTMLInputElement>(null);
  const [expiry, setExpiry] = useState(String(limits.default_expiry_days));
  const [downloads, setDownloads] = useState(String(limits.default_max_downloads));
  const [uploading, setUploading] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const unnamed = t('webmail.attachments.unnamed');

  const maxDownloads = Number.parseInt(downloads, 10);
  const downloadsValid =
    Number.isInteger(maxDownloads) && maxDownloads >= 1 && maxDownloads <= limits.max_downloads;

  const share = async (file: File, source?: File) => {
    setNotice(null);
    const problem = largeFileProblem(file, listing);
    if (problem) {
      setError(problem);
      return;
    }
    setError(null);
    setUploading(displayFilename(file.name, unnamed));
    try {
      const shared = await largeFilesApi.upload(file, {
        expiresInDays: Number.parseInt(expiry, 10),
        maxDownloads,
      });
      onShared(shared, source);
      setNotice(t('webmail.largeFiles.compose.added', { name: shared.name }));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setUploading(null);
    }
  };

  const busy = disabled || uploading !== null;
  return (
    <div className="cf-field cf-wm-largefiles">
      <span className="cf-field__label" id="compose-largefiles-label">
        {t('webmail.largeFiles.compose.title')}
      </span>
      <div
        className="cf-wm-largefiles__row"
        role="group"
        aria-labelledby="compose-largefiles-label"
      >
        <label className="cf-wm-largefiles__option">
          <span className="cf-text-sm">{t('webmail.largeFiles.compose.expiry')}</span>
          <Select
            options={expiryOptions(limits.default_expiry_days, limits.max_expiry_days)}
            value={expiry}
            disabled={busy}
            onChange={(e) => setExpiry(e.target.value)}
          />
        </label>
        <label className="cf-wm-largefiles__option">
          <span className="cf-text-sm">{t('webmail.largeFiles.compose.maxDownloads')}</span>
          <Input
            type="number"
            inputMode="numeric"
            min={1}
            max={limits.max_downloads}
            value={downloads}
            disabled={busy}
            aria-invalid={!downloadsValid}
            onChange={(e) => setDownloads(e.target.value)}
          />
        </label>
        <Button
          icon={<IconLink size={16} />}
          loading={uploading !== null}
          disabled={busy || !downloadsValid}
          onClick={() => input.current?.click()}
        >
          {t('webmail.largeFiles.compose.pick')}
        </Button>
        <input
          ref={input}
          type="file"
          hidden
          aria-hidden="true"
          tabIndex={-1}
          onChange={(e) => {
            const picked = e.target.files?.[0];
            e.target.value = '';
            if (picked) void share(picked);
          }}
        />
      </div>
      <span className="cf-field__hint">
        {t('webmail.largeFiles.compose.hint', { size: formatBytes(limits.max_file_bytes) })}
      </span>
      {suggest.length > 0 ? (
        <div className="cf-wm-largefiles__suggest">
          <span className="cf-text-sm">{t('webmail.largeFiles.compose.convertHint')}</span>
          {suggest.map((file, index) => {
            const name = displayFilename(file.name, unnamed);
            return (
              <Button
                key={`${index}:${file.name}:${file.size}`}
                size="sm"
                variant="ghost"
                icon={<IconLink size={14} />}
                disabled={busy || !downloadsValid}
                onClick={() => void share(file, file)}
              >
                {t('webmail.largeFiles.compose.convert', { name })}
              </Button>
            );
          })}
        </div>
      ) : null}
      <span role="status" className="cf-text-sm cf-text-muted">
        {uploading
          ? t('webmail.largeFiles.compose.uploading', { name: uploading })
          : (notice ?? '')}
      </span>
      {error ? (
        <div className="cf-form__error" role="alert">
          {error}
        </div>
      ) : null}
    </div>
  );
}
