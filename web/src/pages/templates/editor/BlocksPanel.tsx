import type { ComponentType } from 'react';
import {
  IconButtonShape,
  IconColumns,
  IconEye,
  IconFooter,
  IconHeading,
  IconImage,
  IconMinus,
  IconMoveVertical,
  IconShare,
  IconSquare,
  IconTag,
  IconType,
  IconVideo,
  type IconProps,
} from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import { BLOCKS, type BlockCategory, type BlockId } from './blocks';
import type { EngineBlocks } from './engine';

const ICONS: Record<BlockId, ComponentType<IconProps>> = {
  section: IconSquare,
  'columns-1': IconSquare,
  'columns-2': IconColumns,
  'columns-3': IconColumns,
  'columns-4': IconColumns,
  heading: IconHeading,
  text: IconType,
  image: IconImage,
  button: IconButtonShape,
  divider: IconMinus,
  spacer: IconMoveVertical,
  social: IconShare,
  video: IconVideo,
  discount: IconTag,
  'legal-footer': IconFooter,
};

const CATEGORIES: { id: BlockCategory; label: MessageKey }[] = [
  { id: 'layout', label: 'templates.editor.category.layout' },
  { id: 'content', label: 'templates.editor.category.content' },
  { id: 'brand', label: 'templates.editor.category.brand' },
];

export interface BlocksPanelProps {
  blocks: EngineBlocks | null;
  onAppend: (id: BlockId) => void;
  /** El preheader no es un bloque del lienzo: es un ajuste del documento. */
  onPreheader: () => void;
}

/** Bloques arrastrables al lienzo; pulsar uno lo anade junto a la seleccion. */
export function BlocksPanel({ blocks, onAppend, onPreheader }: BlocksPanelProps) {
  const available = new Set(blocks?.ids ?? []);
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <span className="cf-text-sm cf-text-secondary">{t('templates.editor.blocksHint')}</span>
      {CATEGORIES.map((category) => (
        <div key={category.id} className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
          <span className="cf-form__section">{t(category.label)}</span>
          <div className="cf-block-grid">
            {BLOCKS.filter((b) => b.category === category.id && available.has(b.id)).map(
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
            {category.id === 'brand' ? (
              <button type="button" className="cf-block-tile" onClick={onPreheader}>
                <IconEye size={20} />
                <span>{t('templates.editor.block.preheader')}</span>
              </button>
            ) : null}
          </div>
        </div>
      ))}
    </div>
  );
}
