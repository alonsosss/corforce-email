import {
  forwardRef,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ClipboardEvent,
  type ComponentType,
  type DragEvent,
} from 'react';
import { Button, FormField, Input, Modal } from '@/design/components';
import {
  IconBold,
  IconImage,
  IconItalic,
  IconLink,
  IconList,
  IconListOrdered,
  IconQuote,
  IconRemoveFormat,
  IconUnderline,
  type IconProps,
} from '@/design/icons';
import { formatBytes } from '@/lib/quota';
import { t, type MessageKey } from '@/i18n';
import { cleanFragment, cleanHtml, plainToHtml } from './richText';

export interface RichEditorHandle {
  focus: () => void;
}

export interface RichEditorProps {
  id: string;
  /** Contenido inicial; el editor no es controlado: los cambios llegan por onChange. */
  initialHtml: string;
  onChange: (html: string) => void;
  /** Id del elemento que nombra el editor. */
  labelledBy: string;
  disabled?: boolean;
  /**
   * Admite imagenes: conserva las https o incrustadas del contenido inicial y deja insertar
   * nuevas (boton, pegar o arrastrar). El servicio las convierte en partes cid: al enviar.
   */
  allowImages?: boolean;
  /** Tope de cada imagen insertada; sin el, el editor no inserta imagenes nuevas. */
  maxImageBytes?: number | null;
  minHeight?: string;
}

interface Command {
  id: string;
  label: MessageKey;
  icon: ComponentType<IconProps>;
  run: () => void;
}

// document.execCommand esta en desuso pero es la unica edicion con formato que traen todos
// los navegadores sin dependencias; donde no exista, el editor sigue aceptando texto.
function exec(command: string, value?: string): boolean {
  if (typeof document.execCommand !== 'function') return false;
  return document.execCommand(command, false, value);
}

const LINK_SCHEME = /^(https?:\/\/|mailto:)/i;

/** Tipos que se pueden insertar en el cuerpo: los mismos que el servicio acepta como cid:. */
const INSERTABLE_IMAGES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

function readAsDataUrl(file: File): Promise<string | null> {
  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : null);
    reader.onerror = () => resolve(null);
    reader.readAsDataURL(file);
  });
}

/** Enlace escrito por el usuario: sin esquema se entiende https; otros esquemas no valen. */
export function normalizeLink(raw: string): string | null {
  const value = raw.trim();
  if (!value) return null;
  if (LINK_SCHEME.test(value)) return value;
  if (/^[a-z][a-z0-9+.-]*:/i.test(value)) return null;
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) return `mailto:${value}`;
  return /^[^\s]+\.[^\s]+$/.test(value) ? `https://${value}` : null;
}

/**
 * Editor con formato basico sobre contenteditable. Lo pegado se limpia a formato basico
 * antes de entrar; el HTML resultante lo sanea otra vez el servicio al enviar.
 */
export const RichEditor = forwardRef<RichEditorHandle, RichEditorProps>(function RichEditor(
  {
    id,
    initialHtml,
    onChange,
    labelledBy,
    disabled = false,
    allowImages = false,
    maxImageBytes = null,
    minHeight,
  },
  ref,
) {
  const editor = useRef<HTMLDivElement>(null);
  const savedRange = useRef<Range | null>(null);
  const [linking, setLinking] = useState(false);
  const [link, setLink] = useState('');
  const [linkError, setLinkError] = useState<string | null>(null);
  const [imageError, setImageError] = useState<string | null>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const insertsImages = allowImages && maxImageBytes !== null && maxImageBytes > 0;

  useLayoutEffect(() => {
    editor.current?.replaceChildren(cleanFragment(initialHtml, { images: allowImages }));
    // Solo al montar: despues el contenido es del usuario.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useImperativeHandle(ref, () => ({
    focus: () => {
      const node = editor.current;
      if (!node) return;
      node.focus();
      const selection = window.getSelection();
      if (!selection) return;
      const range = document.createRange();
      range.setStart(node, 0);
      range.collapse(true);
      selection.removeAllRanges();
      selection.addRange(range);
    },
  }));

  const emit = () => {
    if (editor.current) onChange(editor.current.innerHTML);
  };

  const apply = (command: string, value?: string) => {
    editor.current?.focus();
    exec(command, value);
    emit();
  };

  const insertHtml = (html: string, images = false) => {
    if (!exec('insertHTML', html)) {
      const selection = window.getSelection();
      const node = editor.current;
      if (!selection || !node || !selection.rangeCount) return;
      const range = selection.getRangeAt(0);
      if (!node.contains(range.commonAncestorContainer)) return;
      range.deleteContents();
      range.insertNode(cleanFragment(html, { images }));
      range.collapse(false);
    }
  };

  // Lee cada imagen como data: y la inserta donde esta el cursor. Solo mapas de bits: el
  // servicio vuelve a comprobar el tipo por su contenido antes de adjuntarla.
  const insertImages = async (files: readonly File[]) => {
    setImageError(null);
    for (const file of files) {
      if (!INSERTABLE_IMAGES.has(file.type)) {
        setImageError(t('webmail.editor.imageType', { name: file.name }));
        continue;
      }
      if (maxImageBytes !== null && file.size > maxImageBytes) {
        setImageError(
          t('webmail.editor.imageTooLarge', { name: file.name, max: formatBytes(maxImageBytes) }),
        );
        continue;
      }
      const src = await readAsDataUrl(file);
      if (!src) {
        setImageError(t('webmail.editor.imageUnreadable', { name: file.name }));
        continue;
      }
      const img = document.createElement('img');
      img.setAttribute('src', src);
      img.setAttribute('alt', file.name);
      editor.current?.focus();
      insertHtml(img.outerHTML, true);
    }
    emit();
  };

  const imageFiles = (list: FileList | null): File[] =>
    Array.from(list ?? []).filter((file) => file.type.startsWith('image/'));

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    if (!insertsImages) return;
    const files = imageFiles(e.dataTransfer.files);
    if (!files.length) return;
    e.preventDefault();
    // El cursor va al punto donde se solto la imagen.
    const range = document.caretRangeFromPoint?.(e.clientX, e.clientY);
    const selection = window.getSelection();
    if (range && selection && editor.current?.contains(range.startContainer)) {
      selection.removeAllRanges();
      selection.addRange(range);
    }
    void insertImages(files);
  };

  const onPickImages = (e: ChangeEvent<HTMLInputElement>) => {
    const files = imageFiles(e.target.files);
    e.target.value = '';
    if (files.length) void insertImages(files);
  };

  const onPaste = (e: ClipboardEvent<HTMLDivElement>) => {
    e.preventDefault();
    const pasted = insertsImages ? imageFiles(e.clipboardData.files) : [];
    if (pasted.length) {
      void insertImages(pasted);
      return;
    }
    const html = e.clipboardData.getData('text/html');
    const text = e.clipboardData.getData('text/plain');
    insertHtml(html ? cleanHtml(html) : plainToHtml(text));
    emit();
  };

  const openLink = () => {
    const selection = window.getSelection();
    const node = editor.current;
    savedRange.current =
      selection && selection.rangeCount && node?.contains(selection.getRangeAt(0).startContainer)
        ? selection.getRangeAt(0).cloneRange()
        : null;
    setLink('');
    setLinkError(null);
    setLinking(true);
  };

  const insertLink = () => {
    const href = normalizeLink(link);
    if (!href) {
      setLinkError(t('webmail.editor.linkInvalid'));
      return;
    }
    setLinking(false);
    const node = editor.current;
    if (!node) return;
    node.focus();
    const selection = window.getSelection();
    if (selection && savedRange.current) {
      selection.removeAllRanges();
      selection.addRange(savedRange.current);
    }
    if (selection && selection.rangeCount && !selection.isCollapsed) {
      exec('createLink', href);
    } else {
      const anchor = document.createElement('a');
      anchor.setAttribute('href', href);
      anchor.textContent = link.trim();
      insertHtml(anchor.outerHTML);
    }
    emit();
  };

  const commands: Command[] = [
    { id: 'bold', label: 'webmail.editor.bold', icon: IconBold, run: () => apply('bold') },
    { id: 'italic', label: 'webmail.editor.italic', icon: IconItalic, run: () => apply('italic') },
    {
      id: 'underline',
      label: 'webmail.editor.underline',
      icon: IconUnderline,
      run: () => apply('underline'),
    },
    {
      id: 'ul',
      label: 'webmail.editor.bullets',
      icon: IconList,
      run: () => apply('insertUnorderedList'),
    },
    {
      id: 'ol',
      label: 'webmail.editor.numbers',
      icon: IconListOrdered,
      run: () => apply('insertOrderedList'),
    },
    {
      id: 'quote',
      label: 'webmail.editor.quote',
      icon: IconQuote,
      run: () => apply('formatBlock', 'blockquote'),
    },
    { id: 'link', label: 'webmail.editor.link', icon: IconLink, run: openLink },
    ...(insertsImages
      ? [
          {
            id: 'image',
            label: 'webmail.editor.image' as MessageKey,
            icon: IconImage,
            run: () => imageInput.current?.click(),
          },
        ]
      : []),
    {
      id: 'clear',
      label: 'webmail.editor.clear',
      icon: IconRemoveFormat,
      run: () => {
        apply('removeFormat');
        apply('unlink');
      },
    },
  ];

  return (
    <div className="cf-wm-editor">
      <div
        className="cf-wm-editor__toolbar"
        role="toolbar"
        aria-label={t('webmail.editor.toolbar')}
        aria-controls={id}
      >
        {commands.map(({ id: key, label, icon: Icon, run }) => (
          <Button
            key={key}
            size="sm"
            variant="ghost"
            iconOnly
            title={t(label)}
            icon={<Icon size={16} />}
            disabled={disabled}
            // Sin perder la seleccion del editor al pulsar.
            onMouseDown={(e) => e.preventDefault()}
            onClick={run}
          >
            {t(label)}
          </Button>
        ))}
      </div>
      <div
        ref={editor}
        id={id}
        className="cf-wm-editor__area"
        role="textbox"
        aria-multiline="true"
        aria-labelledby={labelledBy}
        aria-disabled={disabled || undefined}
        contentEditable={!disabled}
        suppressContentEditableWarning
        tabIndex={0}
        style={minHeight ? { minHeight } : undefined}
        onInput={emit}
        onPaste={onPaste}
        onDrop={onDrop}
      />
      {insertsImages ? (
        <input
          ref={imageInput}
          type="file"
          accept={[...INSERTABLE_IMAGES].join(',')}
          multiple
          hidden
          tabIndex={-1}
          aria-hidden="true"
          onChange={onPickImages}
        />
      ) : null}
      {imageError ? (
        <p className="cf-field__error cf-wm-editor__error" role="alert">
          {imageError}
        </p>
      ) : null}
      <Modal
        open={linking}
        title={t('webmail.editor.linkTitle')}
        onClose={() => setLinking(false)}
        footer={
          <>
            <Button onClick={() => setLinking(false)}>{t('common.cancel')}</Button>
            <Button variant="primary" onClick={insertLink}>
              {t('webmail.editor.linkInsert')}
            </Button>
          </>
        }
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            // El editor vive dentro del formulario de la redaccion: el envio no debe subir.
            e.stopPropagation();
            insertLink();
          }}
        >
          <FormField
            label={t('webmail.editor.linkUrl')}
            htmlFor={`${id}-link`}
            error={linkError}
            hint={t('webmail.editor.linkHint')}
          >
            <Input
              id={`${id}-link`}
              value={link}
              invalid={Boolean(linkError)}
              onChange={(e) => {
                setLink(e.target.value);
                setLinkError(null);
              }}
            />
          </FormField>
        </form>
      </Modal>
    </div>
  );
});
