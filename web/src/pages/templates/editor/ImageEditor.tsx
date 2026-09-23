import { useEffect, useRef, useState } from 'react';
import Cropper from 'cropperjs';
import 'cropperjs/dist/cropper.css';
import { errorMessage } from '@/api/messages';
import { templatesApi, type TemplateAsset } from '@/api/templates';
import { Alert, Button, FormField, Input, Modal, Select, Skeleton } from '@/design/components';
import { IconFlipHorizontal, IconFlipVertical, IconRotateCcw, IconRotateCw } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { applyFilter, cssFilter, IMAGE_FILTERS, type ImageFilter } from './imageFilters';

type Ratio = 'free' | 'square' | 'wide' | 'banner';

const RATIOS: Record<Ratio, number> = { free: NaN, square: 1, wide: 16 / 9, banner: 3 };

export interface ImageEditorProps {
  asset: TemplateAsset;
  onClose: () => void;
  /** La edicion se guarda siempre como una imagen nueva: la original la usan correos ya enviados. */
  onSaved: (asset: TemplateAsset) => void;
}

function outputType(contentType: string): { type: string; ext: string } {
  return contentType === 'image/jpeg'
    ? { type: 'image/jpeg', ext: 'jpg' }
    : { type: 'image/png', ext: 'png' };
}

function editedName(name: string, ext: string): string {
  const base = name.replace(/\.[a-z0-9]+$/i, '') || 'imagen';
  return `${base}-editada.${ext}`;
}

function toBlob(canvas: HTMLCanvasElement, type: string): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error('toBlob'))), type, 0.92);
  });
}

/**
 * Recortar, girar, voltear, redimensionar y filtrar con cropperjs. La imagen se pide con
 * fetch y se pinta desde un blob: asi el lienzo no queda contaminado aunque el almacen
 * sirva las imagenes desde otro origen.
 */
export function ImageEditor({ asset, onClose, onSaved }: ImageEditorProps) {
  const imageRef = useRef<HTMLImageElement>(null);
  const cropperRef = useRef<Cropper | null>(null);
  const [source, setSource] = useState<string | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [ratio, setRatio] = useState<Ratio>('free');
  const [filter, setFilter] = useState<ImageFilter>('none');
  const [width, setWidth] = useState('');
  const [widthError, setWidthError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);

  useEffect(() => {
    const controller = new AbortController();
    let objectUrl: string | null = null;
    fetch(asset.url, { signal: controller.signal, credentials: 'omit' })
      .then((res) => {
        if (!res.ok) throw new Error(String(res.status));
        return res.blob();
      })
      .then((blob) => {
        objectUrl = URL.createObjectURL(blob);
        setSource(objectUrl);
      })
      .catch(() => {
        if (!controller.signal.aborted) setLoadError(true);
      });
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [asset.url]);

  useEffect(() => {
    const image = imageRef.current;
    if (!source || !image) return;
    const cropper = new Cropper(image, {
      viewMode: 1,
      autoCropArea: 1,
      background: false,
      responsive: true,
      crop: (event) => setWidth(String(Math.round(event.detail.width))),
    });
    cropperRef.current = cropper;
    return () => {
      cropper.destroy();
      cropperRef.current = null;
    };
  }, [source]);

  const flip = (axis: 'x' | 'y') => {
    const cropper = cropperRef.current;
    if (!cropper) return;
    const data = cropper.getData();
    if (axis === 'x') cropper.scaleX(-(data.scaleX || 1));
    else cropper.scaleY(-(data.scaleY || 1));
  };

  const changeRatio = (next: Ratio) => {
    setRatio(next);
    cropperRef.current?.setAspectRatio(RATIOS[next]);
  };

  const save = async () => {
    const cropper = cropperRef.current;
    if (!cropper) return;
    const target = Number(width);
    if (!Number.isInteger(target) || target < 1) {
      setWidthError(t('templates.imageEditor.widthInvalid'));
      return;
    }
    setWidthError(null);
    setBusy(true);
    setSaveError(null);
    try {
      const crop = cropper.getData(true);
      const height = Math.max(1, Math.round((crop.height * target) / Math.max(1, crop.width)));
      const canvas = cropper.getCroppedCanvas({
        width: target,
        height,
        imageSmoothingEnabled: true,
        imageSmoothingQuality: 'high',
      });
      if (filter !== 'none') {
        const ctx = canvas.getContext('2d');
        if (ctx) {
          const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height);
          applyFilter(pixels.data, filter);
          ctx.putImageData(pixels, 0, 0);
        }
      }
      const { type, ext } = outputType(asset.content_type);
      const blob = await toBlob(canvas, type);
      const saved = await templatesApi.uploadAsset(blob, editedName(asset.name, ext));
      onSaved(saved);
    } catch (err) {
      setSaveError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      size="lg"
      title={t('templates.imageEditor.title')}
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} disabled={!source} onClick={() => void save()}>
            {t('templates.imageEditor.saveAsNew')}
          </Button>
        </>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <span className="cf-text-sm cf-text-secondary">{t('templates.imageEditor.hint')}</span>
        {loadError ? (
          <Alert tone="danger">{t('templates.imageEditor.loadFailed')}</Alert>
        ) : !source ? (
          <Skeleton height="320px" />
        ) : (
          <div className="cf-image-editor__stage" style={{ filter: cssFilter(filter) }}>
            <img ref={imageRef} src={source} alt={asset.name} />
          </div>
        )}
        <div className="cf-image-editor__tools">
          <Button
            size="sm"
            icon={<IconRotateCcw size={16} />}
            onClick={() => cropperRef.current?.rotate(-90)}
            disabled={!source}
          >
            {t('templates.imageEditor.rotateLeft')}
          </Button>
          <Button
            size="sm"
            icon={<IconRotateCw size={16} />}
            onClick={() => cropperRef.current?.rotate(90)}
            disabled={!source}
          >
            {t('templates.imageEditor.rotateRight')}
          </Button>
          <Button
            size="sm"
            icon={<IconFlipHorizontal size={16} />}
            onClick={() => flip('x')}
            disabled={!source}
          >
            {t('templates.imageEditor.flipHorizontal')}
          </Button>
          <Button
            size="sm"
            icon={<IconFlipVertical size={16} />}
            onClick={() => flip('y')}
            disabled={!source}
          >
            {t('templates.imageEditor.flipVertical')}
          </Button>
        </div>
        <div className="cf-form__row">
          <FormField label={t('templates.imageEditor.ratio')} htmlFor="image-editor-ratio">
            <Select
              id="image-editor-ratio"
              value={ratio}
              onChange={(e) => changeRatio(e.target.value as Ratio)}
              options={(Object.keys(RATIOS) as Ratio[]).map((value) => ({
                value,
                label: tEnum('templates.imageEditor.ratioOption', value),
              }))}
            />
          </FormField>
          <FormField label={t('templates.imageEditor.filter')} htmlFor="image-editor-filter">
            <Select
              id="image-editor-filter"
              value={filter}
              onChange={(e) => setFilter(e.target.value as ImageFilter)}
              options={IMAGE_FILTERS.map((value) => ({
                value,
                label: tEnum('templates.imageEditor.filterOption', value),
              }))}
            />
          </FormField>
          <FormField
            label={t('templates.imageEditor.width')}
            htmlFor="image-editor-width"
            hint={t('templates.imageEditor.widthHint')}
            error={widthError}
          >
            <Input
              id="image-editor-width"
              inputMode="numeric"
              value={width}
              onChange={(e) => setWidth(e.target.value.replace(/\D/g, ''))}
              invalid={Boolean(widthError)}
            />
          </FormField>
        </div>
        {saveError ? <Alert tone="danger">{errorMessage(saveError)}</Alert> : null}
      </div>
    </Modal>
  );
}
