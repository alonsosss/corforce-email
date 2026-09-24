import grapesjs, {
  type Asset,
  type AssetsCustomData,
  type Block,
  type BlocksCustomData,
  type Component,
  type Editor,
  type ProjectData,
} from 'grapesjs';
import esCore from 'grapesjs/locale/es.mjs';
import 'grapesjs/dist/css/grapes.min.css';
import '../../editor/engine.css';
import type { BrandTokens } from '../../editor/brand';
import { FORM_MARKER_ATTRIBUTE, webBlockById, WEB_BLOCKS, type WebBlockId } from './webBlocks';

// Unico modulo que conoce GrapesJS en modo web (sin MJML). La pantalla lo carga con import()
// para que el lienzo vaya en su propio chunk, y solo habla con el a traves de PageEngine. Sin
// complementos: nada que evalue codigo, y el lienzo no ejecuta scripts ni atributos on*.

export type PageDeviceId = 'desktop' | 'mobile';

export interface PageEngineBlocks {
  ids: WebBlockId[];
  dragStart: (id: WebBlockId, event: DragEvent) => void;
  dragStop: (cancel: boolean) => void;
}

/** Peticion del lienzo para elegir una imagen (al soltar una imagen o al pulsarla dos veces). */
export interface PageAssetRequest {
  select: (url: string, name: string) => void;
  close: () => void;
}

export interface PageEngineOptions {
  container: HTMLElement;
  styles: HTMLElement;
  traits: HTMLElement;
  layers: HTMLElement;
  brand: BrandTokens;
  palette: string[];
  /** Texto del marcador de un formulario en el lienzo, por su clave. */
  formLabel: (key: string) => string;
  onChange: () => void;
  onBlocks: (blocks: PageEngineBlocks) => void;
  onAssets: (request: PageAssetRequest | null) => void;
}

export interface PageEngine {
  loadProject: (project: Record<string, unknown>) => void;
  loadHtml: (html: string, css: string) => void;
  html: () => string;
  css: () => string;
  project: () => Record<string, unknown>;
  isDirty: () => boolean;
  markSaved: () => void;
  canUndo: () => boolean;
  canRedo: () => boolean;
  undo: () => void;
  redo: () => void;
  setDevice: (device: PageDeviceId) => void;
  appendBlock: (id: WebBlockId) => void;
  insertHtml: (html: string) => void;
  destroy: () => void;
}

const DEVICES = [
  { id: 'desktop', name: 'desktop', width: '' },
  { id: 'mobile', name: 'mobile', width: '375px', widthMedia: '480px' },
];

const FORM_TYPE = 'cf-subscription-form';

/** Aspecto del marcador solo en el lienzo: no se exporta con la pagina. */
const CANVAS_CSS =
  `[${FORM_MARKER_ATTRIBUTE}]{display:flex;align-items:center;justify-content:center;` +
  'min-height:120px;margin:16px auto;padding:24px;max-width:640px;border:2px dashed #9CA3AF;' +
  'border-radius:8px;background:#F9FAFB;color:#374151;' +
  'font:600 14px/1.4 Helvetica,Arial,sans-serif;text-align:center}';

/** El bloque de nivel superior que contiene la seleccion: los bloques nuevos van detras. */
function topLevel(editor: Editor, component: Component | undefined): Component | undefined {
  const wrapper = editor.getWrapper();
  let current = component;
  while (current && current.parent() && current.parent() !== wrapper) {
    current = current.parent();
  }
  return current && current.parent() === wrapper ? current : undefined;
}

export function createPageEngine(options: PageEngineOptions): PageEngine {
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
    canvasCss: CANVAS_CSS,
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
    parser: {
      optionsHtml: { allowScripts: false, allowUnsafeAttr: false, allowUnsafeAttrValue: false },
    },
  });

  editor.Components.addType(FORM_TYPE, {
    isComponent: (el) => el.tagName === 'DIV' && el.hasAttribute(FORM_MARKER_ATTRIBUTE),
    model: {
      defaults: {
        tagName: 'div',
        droppable: false,
        editable: false,
        stylable: false,
        traits: [],
        components: [],
      },
    },
    view: {
      onRender({ el, model }) {
        const key = String(model.getAttributes()[FORM_MARKER_ATTRIBUTE] ?? '');
        el.textContent = options.formLabel(key);
      },
    },
  });

  for (const block of WEB_BLOCKS) {
    editor.Blocks.add(block.id, {
      label: block.id,
      content: block.content(options.brand),
      activate: block.activate ?? false,
      select: true,
    });
  }

  editor.on('block:custom', (data: BlocksCustomData) => {
    options.onBlocks({
      ids: data.blocks.map((b: Block) => b.getId() as WebBlockId).filter((id) => webBlockById(id)),
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

  editor.on('update undo redo', options.onChange);

  const insert = (html: string) => {
    const wrapper = editor.getWrapper();
    if (!wrapper) return;
    const anchor = topLevel(editor, editor.getSelected());
    const added = wrapper.append(html, anchor ? { at: anchor.index() + 1 } : {});
    if (added[0]) editor.select(added[0]);
  };

  return {
    loadProject: (project) => {
      editor.loadProjectData(project as ProjectData);
      editor.UndoManager.clear();
      editor.clearDirtyCount();
    },
    loadHtml: (html, css) => {
      editor.setComponents(html);
      editor.setStyle(css);
      editor.UndoManager.clear();
    },
    html: () => editor.getHtml(),
    css: () => editor.getCss() ?? '',
    project: () => editor.getProjectData() as Record<string, unknown>,
    isDirty: () => editor.getDirtyCount() > 0,
    markSaved: () => editor.clearDirtyCount(),
    canUndo: () => editor.UndoManager.hasUndo(),
    canRedo: () => editor.UndoManager.hasRedo(),
    undo: () => editor.UndoManager.undo(),
    redo: () => editor.UndoManager.redo(),
    setDevice: (device) => editor.setDevice(device),
    appendBlock: (id) => {
      const block = webBlockById(id);
      if (block) insert(block.content(options.brand));
    },
    insertHtml: insert,
    destroy: () => editor.destroy(),
  };
}
