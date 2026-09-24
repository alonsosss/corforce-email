import {
  forwardRef,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type ComponentType,
} from 'react';
import { Button, FormField, Input, Modal } from '@/design/components';
import {
  IconBold,
  IconItalic,
  IconLink,
  IconList,
  IconListOrdered,
  IconQuote,
  IconRemoveFormat,
  IconUnderline,
  type IconProps,
} from '@/design/icons';
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
  /** Admite imagenes https o incrustadas al cargar el contenido (la firma). */
  allowImages?: boolean;
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
  { id, initialHtml, onChange, labelledBy, disabled = false, allowImages = false, minHeight },
  ref,
) {
  const editor = useRef<HTMLDivElement>(null);
  const savedRange = useRef<Range | null>(null);
  const [linking, setLinking] = useState(false);
  const [link, setLink] = useState('');
  const [linkError, setLinkError] = useState<string | null>(null);

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

  const insertHtml = (html: string) => {
    if (!exec('insertHTML', html)) {
      const selection = window.getSelection();
      const node = editor.current;
      if (!selection || !node || !selection.rangeCount) return;
      const range = selection.getRangeAt(0);
      if (!node.contains(range.commonAncestorContainer)) return;
      range.deleteContents();
      range.insertNode(cleanFragment(html));
      range.collapse(false);
    }
  };

  const onPaste = (e: ClipboardEvent<HTMLDivElement>) => {
    e.preventDefault();
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
      />
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
