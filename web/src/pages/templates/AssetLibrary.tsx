import { useCallback, useEffect, useRef, useState } from 'react';
import { ERROR_CODES, errorCode } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { templatesApi, templatesMeta, type TemplateAsset } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import {
  Alert,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Modal,
  Skeleton,
} from '@/design/components';
import { IconCrop, IconImage, IconTrash, IconUpload } from '@/design/icons';
import { useResource } from '@/hooks/useResource';
import { formatBytes } from '@/lib/quota';
import { t } from '@/i18n';
import { ImageEditor } from './editor/ImageEditor';
import './templateMedia.css';

export interface AssetLibraryProps {
  open: boolean;
  onClose: () => void;
  /** Sin onPick la biblioteca solo gestiona (subir, editar, retirar). */
  onPick?: (asset: TemplateAsset) => void;
  title?: string;
}

interface ListState {
  items: TemplateAsset[];
  cursor: string | null;
  loading: boolean;
  error: unknown;
}

const EMPTY: ListState = { items: [], cursor: null, loading: true, error: null };

/**
 * Error de subida con el motivo del servicio: sin antivirus configurado (503) no se admite
 * ninguna imagen, e infectada (422) se rechaza; el resto (formato, tamano, dimensiones)
 * trae el mensaje del validador.
 */
function uploadError(err: unknown): string {
  if (errorCode(err) === ERROR_CODES.SCANNER_UNAVAILABLE)
    return t('templates.assets.scannerUnavailable');
  if (errorCode(err) === ERROR_CODES.ASSET_REJECTED) return t('templates.assets.rejected');
  return errorMessage(err);
}

/** Biblioteca de imagenes de la empresa sobre GET/POST/DELETE /templates/assets. */
export function AssetLibrary({ open, onClose, onPick, title }: AssetLibraryProps) {
  const { can } = useAccess();
  const canUpload = can(...PERMISSIONS.templateAssets.create);
  const canDelete = can(...PERMISSIONS.templateAssets.delete);
  const meta = useResource(templatesMeta);
  const fileRef = useRef<HTMLInputElement>(null);
  const [list, setList] = useState<ListState>(EMPTY);
  const [uploading, setUploading] = useState(false);
  const [uploadFailure, setUploadFailure] = useState<string | null>(null);
  const [editing, setEditing] = useState<TemplateAsset | null>(null);
  const [removing, setRemoving] = useState<TemplateAsset | null>(null);

  const load = useCallback(async (cursor: string | null) => {
    setList((prev) => ({ ...prev, loading: true, error: null }));
    try {
      const page = await templatesApi.listAssets({ cursor });
      setList((prev) => ({
        items: cursor ? [...prev.items, ...page.items] : page.items,
        cursor: page.nextCursor,
        loading: false,
        error: null,
      }));
    } catch (err) {
      setList((prev) => ({ ...prev, loading: false, error: err }));
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    setList(EMPTY);
    void load(null);
  }, [open, load]);

  const prepend = (asset: TemplateAsset) =>
    setList((prev) => ({
      ...prev,
      items: [asset, ...prev.items.filter((item) => item.id !== asset.id)],
    }));

  const upload = async (files: FileList | null) => {
    const file = files?.[0];
    if (!file) return;
    setUploading(true);
    setUploadFailure(null);
    try {
      prepend(await templatesApi.uploadAsset(file, file.name));
    } catch (err) {
      setUploadFailure(uploadError(err));
    } finally {
      setUploading(false);
      if (fileRef.current) fileRef.current.value = '';
    }
  };

  if (!open) return null;

  return (
    <>
      <Modal
        open={!editing && !removing}
        size="lg"
        title={title ?? t('templates.assets.title')}
        onClose={onClose}
        footer={<Button onClick={onClose}>{t('common.close')}</Button>}
      >
        <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
          <div className="cf-toolbar">
            <span className="cf-text-sm cf-text-secondary">
              {t('templates.assets.hint')}
              {meta.data
                ? ` ${t('templates.assets.limits', {
                    size: formatBytes(meta.data.limits.max_asset_bytes),
                    max: meta.data.limits.max_asset_dimension,
                  })}`
                : ''}
            </span>
            {canUpload ? (
              <div className="cf-toolbar__actions">
                <input
                  ref={fileRef}
                  type="file"
                  accept={meta.data?.asset_content_types.join(',')}
                  className="cf-visually-hidden"
                  aria-label={t('templates.assets.upload')}
                  onChange={(e) => void upload(e.target.files)}
                />
                <Button
                  variant="primary"
                  icon={<IconUpload size={16} />}
                  loading={uploading}
                  onClick={() => fileRef.current?.click()}
                >
                  {t('templates.assets.upload')}
                </Button>
              </div>
            ) : null}
          </div>
          {uploadFailure ? (
            <Alert tone="danger" title={t('templates.assets.uploadFailed')}>
              {uploadFailure}
            </Alert>
          ) : null}
          {list.error && !list.items.length ? (
            <ErrorState error={list.error} onRetry={() => void load(null)} />
          ) : list.loading && !list.items.length ? (
            <Skeleton lines={4} />
          ) : !list.items.length ? (
            <EmptyState icon={<IconImage size={32} />} title={t('templates.assets.empty')} />
          ) : (
            <ul className="cf-asset-grid" aria-label={t('templates.assets.title')}>
              {list.items.map((asset) => (
                <li key={asset.id} className="cf-asset">
                  <div className="cf-asset__thumb">
                    <img src={asset.url} alt={asset.name} loading="lazy" />
                  </div>
                  <div className="cf-asset__meta">
                    <span className="cf-asset__name" title={asset.name}>
                      {asset.name}
                    </span>
                    <span className="cf-text-sm cf-text-secondary">
                      {asset.width} x {asset.height} · {formatBytes(asset.size_bytes)}
                    </span>
                  </div>
                  <div className="cf-asset__actions">
                    {onPick ? (
                      <Button size="sm" variant="primary" onClick={() => onPick(asset)}>
                        {t('templates.assets.pick')}
                      </Button>
                    ) : null}
                    {canUpload ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        iconOnly
                        icon={<IconCrop size={16} />}
                        onClick={() => setEditing(asset)}
                      >
                        {t('templates.assets.edit')}
                      </Button>
                    ) : null}
                    {canDelete ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        iconOnly
                        icon={<IconTrash size={16} />}
                        onClick={() => setRemoving(asset)}
                      >
                        {t('templates.assets.remove')}
                      </Button>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>
          )}
          {list.error && list.items.length ? (
            <Alert tone="danger">{errorMessage(list.error)}</Alert>
          ) : null}
          {list.cursor ? (
            <div>
              <Button loading={list.loading} onClick={() => void load(list.cursor)}>
                {t('templates.assets.more')}
              </Button>
            </div>
          ) : null}
        </div>
      </Modal>
      {editing ? (
        <ImageEditor
          asset={editing}
          onClose={() => setEditing(null)}
          onSaved={(asset) => {
            prepend(asset);
            setEditing(null);
          }}
        />
      ) : null}
      <ConfirmDialog
        open={removing !== null}
        title={t('templates.assets.remove')}
        message={t('templates.assets.removeConfirm', { name: removing?.name ?? '' })}
        confirmLabel={t('templates.assets.remove')}
        danger
        onCancel={() => setRemoving(null)}
        onConfirm={async () => {
          if (!removing) return;
          await templatesApi.deleteAsset(removing.id);
          setList((prev) => ({
            ...prev,
            items: prev.items.filter((item) => item.id !== removing.id),
          }));
          setRemoving(null);
        }}
      />
    </>
  );
}
