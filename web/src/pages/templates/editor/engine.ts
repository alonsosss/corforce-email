import grapesjs, {
  type Asset,
  type AssetsCustomData,
  type Block,
  type BlocksCustomData,
  type Component,
  type Editor,
  type ProjectData,
} from 'grapesjs';
import mjmlPlugin, { type PluginOptions as MjmlPluginOptions } from 'grapesjs-mjml';
import esCore from 'grapesjs/locale/es.mjs';
import esMjml from 'grapesjs-mjml/locale/es';
import mjml2html from 'mjml-browser';
import 'grapesjs/dist/css/grapes.min.css';
import './engine.css';
import { BLOCKS, type BlockDefinition, type BlockId } from './blocks';
import type { BrandTokens } from './brand';
import { compileMjml, MJML_OPTIONS, type CompileResult } from './mjml';
import { escapeMjml } from './mjmlSource';

// Unico modulo que conoce GrapesJS. La pantalla lo carga con import() para que el lienzo,
// el complemento MJML y el compilador vayan en su propio chunk, y solo habla con el a
// traves de EditorEngine.

export type DeviceId = 'desktop' | 'mobile';

export interface EngineSelection {
  /** Hay un texto en edicion: se puede insertar en el cursor. */
  editingText: boolean;
  /** El elemento seleccionado tiene enlace (boton, imagen, enlace de un texto). */
  hasLink: boolean;
}

export interface EngineOptions {
  container: HTMLElement;
  styles: HTMLElement;
  traits: HTMLElement;
  layers: HTMLElement;
  brand: BrandTokens;
  /** Colores del kit de marca para el selector de color. */
  palette: string[];
  onChange: () => void;
  onSelection: (selection: EngineSelection) => void;
  onBlocks: (blocks: EngineBlocks) => void;
  onAssets: (request: AssetRequest | null) => void;
}

export interface EngineBlocks {
  ids: BlockId[];
  dragStart: (id: BlockId, event: DragEvent) => void;
  dragStop: (cancel: boolean) => void;
}

/** Peticion del lienzo para elegir una imagen (al soltar una imagen o al pulsarla dos veces). */
export interface AssetRequest {
  select: (url: string, name: string) => void;
  close: () => void;
}

export interface EditorEngine {
  loadProject: (project: Record<string, unknown>) => void;
  loadMjml: (mjml: string) => void;
  /** MJML del lienzo (sin el preheader, que se anade al compilar). */
  mjml: () => string;
  project: () => Record<string, unknown>;
  /** Compila un documento MJML completo con el mismo compilador que usa el lienzo. */
  compile: (mjml: string) => CompileResult;
  /** Hay cambios en el lienzo desde la carga o desde el ultimo guardado. */
  isDirty: () => boolean;
  markSaved: () => void;
  canUndo: () => boolean;
  canRedo: () => boolean;
  undo: () => void;
  redo: () => void;
  setDevice: (device: DeviceId) => void;
  setDarkPreview: (on: boolean) => void;
  appendBlock: (id: BlockId) => void;
  insertMjml: (mjml: string) => void;
  insertText: (text: string) => boolean;
  appendToLink: (text: string) => boolean;
  destroy: () => void;
}

const DEVICES = [
  { id: 'desktop', name: 'desktop', width: '' },
  { id: 'mobile', name: 'mobile', width: '375px', widthMedia: '480px' },
];

const DARK_STYLE_ID = 'cf-dark-preview';
/**
 * Aproximacion de la inversion que aplican Gmail y Outlook en modo oscuro a los correos
 * sin estilos propios: invierte colores y vuelve a invertir las imagenes.
 */
const DARK_CSS =
  'html{filter:invert(1) hue-rotate(180deg);background:#fff}' +
  'img,[style*="background-image"]{filter:invert(1) hue-rotate(180deg)}';

const COLUMN_TYPES = ['mj-column', 'mj-hero'];

function blockById(id: string): BlockDefinition | undefined {
  return BLOCKS.find((b) => b.id === id);
}

function body(editor: Editor): Component | undefined {
  return editor.getWrapper()?.find('mj-body')[0];
}

function closestColumn(component: Component | undefined): Component | undefined {
  let current = component;
  while (current) {
    if (COLUMN_TYPES.includes(current.get('type') ?? '')) return current;
    current = current.parent();
  }
  return undefined;
}

function closestSection(component: Component | undefined): Component | undefined {
  let current = component;
  while (current) {
    const type = current.get('type');
    if (type === 'mj-section' || type === 'mj-wrapper') return current;
    current = current.parent();
  }
  return undefined;
}

function hasHref(component: Component | undefined): boolean {
  if (!component) return false;
  return Boolean(component.getTrait('href')) || 'href' in component.getAttributes();
}

export function createEngine(options: EngineOptions): EditorEngine {
  let dark = false;

  const editor = grapesjs.init({
    container: options.container,
    height: '100%',
    width: 'auto',
    fromElement: false,
    storageManager: false,
    noticeOnUnload: false,
    // Sin recursos de terceros: ni la hoja de iconos de un CDN ni la telemetria de GrapesJS,
    // que ademas bloquearia la CSP (style-src y connect-src solo admiten el propio origen).
    cssIcons: '',
    telemetry: false,
    panels: { defaults: [] },
    blockManager: { custom: true },
    assetManager: { custom: true, upload: false, embedAsBase64: false },
    styleManager: { appendTo: options.styles },
    traitManager: { appendTo: options.traits },
    layerManager: { appendTo: options.layers },
    deviceManager: { devices: DEVICES },
    colorPicker: {
      palette: options.palette.length ? [options.palette] : [],
      showPalette: options.palette.length > 0,
      showInput: true,
      preferredFormat: 'hex',
    },
    i18n: { locale: 'es', localeFallback: 'en', detectLocale: false, messages: { es: esCore } },
    // El lienzo no ejecuta nada del documento: ni scripts ni atributos on* ni javascript:.
    // Ademas hereda la CSP de la aplicacion, que no admite scripts en linea.
    parser: {
      optionsHtml: { allowScripts: false, allowUnsafeAttr: false, allowUnsafeAttrValue: false },
    },
    plugins: [
      (editor: Editor) =>
        mjmlPlugin(editor, {
          blocks: [],
          resetBlocks: true,
          resetDevices: false,
          overwriteExport: false,
          // Los tipos del complemento declaran el mjml 5 asincrono, pero en ejecucion llama al
          // parser y lee .html en el acto: es la firma sincrona de mjml-browser 4.
          mjmlParser: mjml2html as unknown as MjmlPluginOptions['mjmlParser'],
          fonts: MJML_OPTIONS.fonts,
          i18n: { es: esMjml },
          useCustomTheme: false,
        }),
    ],
  });

  for (const block of BLOCKS) {
    editor.Blocks.add(block.id, {
      label: block.id,
      content: block.content(options.brand),
      activate: block.activate ?? false,
      select: true,
    });
  }

  editor.on('block:custom', (data: BlocksCustomData) => {
    options.onBlocks({
      ids: data.blocks.map((b: Block) => b.getId() as BlockId).filter((id) => blockById(id)),
      dragStart: (id, event) => {
        const block = data.bm.get(id);
        if (block) data.dragStart(block, event);
      },
      dragStop: (cancel) => data.dragStop(cancel),
    });
  });

  editor.on('asset:custom', (data: AssetsCustomData) => {
    if (!data.open) {
      options.onAssets(null);
      return;
    }
    options.onAssets({
      select: (url, name) => {
        const asset: Asset = editor.AssetManager.add({ src: url, name, type: 'image' });
        data.select(asset, true);
        data.close();
      },
      close: () => data.close(),
    });
  });

  const emitSelection = () =>
    options.onSelection({
      editingText: Boolean(editor.getEditing()),
      hasLink: hasHref(editor.getSelected()),
    });
  editor.on('component:toggled rte:enable rte:disable', emitSelection);
  editor.on('component:update:attributes', emitSelection);
  editor.on('update undo redo', options.onChange);

  const applyDark = () => {
    const doc = editor.Canvas.getDocument();
    if (!doc) return;
    const existing = doc.getElementById(DARK_STYLE_ID);
    if (dark && !existing) {
      const style = doc.createElement('style');
      style.id = DARK_STYLE_ID;
      style.textContent = DARK_CSS;
      doc.head.appendChild(style);
    } else if (!dark && existing) {
      existing.remove();
    }
  };
  editor.on('canvas:frame:load:body', applyDark);

  const insertInto = (mjml: string, placement: BlockDefinition['placement']) => {
    const selected = editor.getSelected();
    if (placement === 'column') {
      const column = closestColumn(selected);
      if (column) {
        const added = column.append(mjml);
        if (added[0]) editor.select(added[0]);
        return;
      }
      const root = body(editor);
      const added = root?.append(`<mj-section><mj-column>${mjml}</mj-column></mj-section>`);
      if (added?.[0]) editor.select(added[0]);
      return;
    }
    const section = closestSection(selected);
    const root = body(editor);
    if (!root) return;
    const at = section ? section.index() + 1 : undefined;
    const added = root.append(mjml, at === undefined ? {} : { at });
    if (added[0]) editor.select(added[0]);
  };

  return {
    loadProject: (project) => {
      editor.loadProjectData(project as ProjectData);
      editor.UndoManager.clear();
      editor.clearDirtyCount();
    },
    loadMjml: (mjml) => {
      editor.setComponents(mjml);
      editor.UndoManager.clear();
    },
    isDirty: () => editor.getDirtyCount() > 0,
    markSaved: () => {
      editor.clearDirtyCount();
    },
    mjml: () => editor.getHtml().trim(),
    project: () => editor.getProjectData() as Record<string, unknown>,
    compile: compileMjml,
    canUndo: () => editor.UndoManager.hasUndo(),
    canRedo: () => editor.UndoManager.hasRedo(),
    undo: () => editor.UndoManager.undo(),
    redo: () => editor.UndoManager.redo(),
    setDevice: (device) => editor.setDevice(device),
    setDarkPreview: (on) => {
      dark = on;
      applyDark();
    },
    appendBlock: (id) => {
      const block = blockById(id);
      if (block) insertInto(block.content(options.brand), block.placement);
    },
    insertMjml: (mjml) => insertInto(mjml, 'column'),
    insertText: (text) => {
      const rte = editor.RichTextEditor.globalRte;
      if (!editor.getEditing() || !rte) return false;
      rte.insertHTML(escapeMjml(text));
      return true;
    },
    appendToLink: (text) => {
      const selected = editor.getSelected();
      if (!selected || !hasHref(selected)) return false;
      const current = String(selected.getAttributes().href ?? '');
      const base = current === '#' ? '' : current;
      selected.addAttributes({ href: `${base}${text}` });
      return true;
    },
    destroy: () => editor.destroy(),
  };
}
