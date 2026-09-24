import type { ComponentType } from 'react';
import {
  IconButtonShape,
  IconClipboard,
  IconColumns,
  IconHeading,
  IconImage,
  IconMinus,
  IconMoveVertical,
  IconSquare,
  IconType,
  type IconProps,
} from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import type { PageEngineBlocks } from './webEngine';
import { WEB_BLOCKS, type WebBlockCategory, type WebBlockId } from './webBlocks';

const ICONS: Record<WebBlockId, ComponentType<IconProps>> = {
  section: IconSquare,
  'columns-2': IconColumns,
  'columns-3': IconColumns,
  heading: IconHeading,
  text: IconType,
  image: IconImage,
  button: IconButtonShape,
  divider: IconMinus,
  spacer: IconMoveVertical,
};

const CATEGORIES: { id: WebBlockCategory; label: MessageKey }[] = [
  { id: 'layout', label: 'templates.editor.category.layout' },
  { id: 'content', label: 'templates.editor.category.content' },
];

export interface PageBlocksPanelProps {
  blocks: PageEngineBlocks | null;
  onAppend: (id: WebBlockId) => void;
  /** Sin permiso para ver los formularios el bloque no se ofrece. */
  onInsertForm: (() => void) | null;
}

/** Bloques arrastrables al lienzo; pulsar uno lo anade detras de la seleccion. */
export function PageBlocksPanel({ blocks, onAppend, onInsertForm }: PageBlocksPanelProps) {
  const available = new Set(blocks?.ids ?? []);
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <span className="cf-text-sm cf-text-secondary">{t('templates.pages.editor.blocksHint')}</span>
      {CATEGORIES.map((category) => (
        <div key={category.id} className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
          <span className="cf-form__section">{t(category.label)}</span>
          <div className="cf-block-grid">
            {WEB_BLOCKS.filter((b) => b.category === category.id && available.has(b.id)).map(
              (block) => {
                const Icon = ICONS[block.id];
                return (
                  <button
                    key={block.id}
                    type="button"
                    className="cf-block-tile"
                    draggable
                    onDragStart={(e) => blocks?.dragStart(block.id, e.nativeEvent)}
                    onDragEnd={() => blocks?.dragStop(false)}
                    onClick={() => onAppend(block.id)}
                  >
                    <Icon size={20} />
                    <span>{t(block.label)}</span>
                  </button>
                );
              },
            )}
          </div>
        </div>
      ))}
      <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
        <span className="cf-form__section">{t('templates.pages.editor.category.capture')}</span>
        {onInsertForm ? (
          <div className="cf-block-grid">
            <button type="button" className="cf-block-tile" onClick={onInsertForm}>
              <IconClipboard size={20} />
              <span>{t('templates.pages.editor.block.form')}</span>
            </button>
          </div>
        ) : (
          <span className="cf-text-sm cf-text-muted">
            {t('templates.pages.editor.formNoPermission')}
          </span>
        )}
      </div>
    </div>
  );
}
