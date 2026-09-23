import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useBlocker } from 'react-router-dom';
import { ERROR_CODES, isApiError } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import {
  deliverabilityIssues,
  EDITOR_KIND_GRAPESJS_MJML,
  templatesApi,
  type CheckIssue,
  type CheckRequest,
  type EditorDocument,
  type TemplateAsset,
  type TemplateContent,
} from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  LoadingBlock,
  Tabs,
  useToast,
} from '@/design/components';
import {
  IconChevronLeft,
  IconGrid,
  IconImage,
  IconMonitor,
  IconMoon,
  IconRedo,
  IconSave,
  IconSend,
  IconSmartphone,
  IconUndo,
} from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AssetLibrary } from '../AssetLibrary';
import { contentFromDraft, type ContentErrors } from '../content';
import type { GalleryTemplate } from '../gallery';
import { BlocksPanel } from './BlocksPanel';
import { logoImage, type BlockId } from './blocks';
import { BrandPanel } from './BrandPanel';
import { brandTokens } from './brand';
import { DeliverabilityPanel } from './DeliverabilityPanel';
import { DocumentPanel, type DocumentDraft } from './DocumentPanel';
import type { AssetRequest, DeviceId, EditorEngine, EngineBlocks, EngineSelection } from './engine';
import { GalleryModal } from './GalleryModal';
import { htmlToText, writePreheader } from './mjmlSource';
import { applyGallery, emptyMjml, initialDocument, savedDraft, type EditorData } from './session';
import { useDeliverabilityCheck } from './useDeliverabilityCheck';
import { VariablesPanel } from './VariablesPanel';

type LeftTab = 'blocks' | 'layers';
type RightTab = 'styles' | 'traits' | 'document' | 'variables' | 'brand' | 'check';

interface BuiltDocument {
  mjml: string;
  html: string;
  warnings: string[];
}

const NO_SELECTION: EngineSelection = { editingText: false, hasLink: false };

/** El documento no se puede guardar: el motivo ya se muestra junto a su campo. */
class DocumentInvalid extends Error {}

export function EditorWorkspace({ data }: { data: EditorData }) {
  const { template, meta, base, brand } = data;
  const toast = useToast();
  const { can } = useAccess();
  const archived = template.status === 'archived';
  const canSave = can(...PERMISSIONS.templates.create) && !archived;
  const canPublish = can(...PERMISSIONS.templates.publish) && !archived;

  const tokens = useMemo(
    () => brandTokens(brand.kit, brand.logo?.url ?? null, meta.brand_fonts),
    [brand.kit, brand.logo, meta.brand_fonts],
  );
  const palette = useMemo(() => brand.kit?.colors ?? [], [brand.kit]);

  const canvasRef = useRef<HTMLDivElement>(null);
  const stylesRef = useRef<HTMLDivElement>(null);
  const traitsRef = useRef<HTMLDivElement>(null);
  const layersRef = useRef<HTMLDivElement>(null);
  const preheaderRef = useRef<HTMLInputElement>(null);

  const [engine, setEngine] = useState<EditorEngine | null>(null);
  const [engineError, setEngineError] = useState<unknown>(null);
  const [blocks, setBlocks] = useState<EngineBlocks | null>(null);
  const [assetRequest, setAssetRequest] = useState<AssetRequest | null>(null);
  const [selection, setSelection] = useState<EngineSelection>(NO_SELECTION);
  const [history, setHistory] = useState({ undo: false, redo: false });
  const [revision, setRevision] = useState(0);
  const [canvasDirty, setCanvasDirty] = useState(false);

  const [doc, setDoc] = useState<DocumentDraft>(() => initialDocument(base));
  const [docDirty, setDocDirty] = useState(false);
  const [errors, setErrors] = useState<ContentErrors>({});
  const [saved, setSaved] = useState<number | null>(() => savedDraft(base));
  const [compileWarnings, setCompileWarnings] = useState<string[]>([]);
  const [publishIssues, setPublishIssues] = useState<CheckIssue[] | null>(null);

  const [leftTab, setLeftTab] = useState<LeftTab>('blocks');
  const [rightTab, setRightTab] = useState<RightTab>('styles');
  const [device, setDevice] = useState<DeviceId>('desktop');
  const [dark, setDark] = useState(false);
  const [galleryOpen, setGalleryOpen] = useState(false);
  const [pendingGallery, setPendingGallery] = useState<{
    template: GalleryTemplate;
    mjml: string;
  } | null>(null);
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);

  const dirty = canvasDirty || docDirty;
  const handDrafted = Boolean(base && !base.editor);

  useEffect(() => {
    let cancelled = false;
    let created: EditorEngine | null = null;
    const container = canvasRef.current;
    const styles = stylesRef.current;
    const traits = traitsRef.current;
    const layers = layersRef.current;
    if (!container || !styles || !traits || !layers) return;
    import('./engine')
      .then(({ createEngine }) => {
        if (cancelled) return;
        const instance = createEngine({
          container,
          styles,
          traits,
          layers,
          brand: tokens,
          palette,
          onChange: () => {
            setRevision((r) => r + 1);
            setCanvasDirty(created?.isDirty() ?? false);
            setHistory({ undo: created?.canUndo() ?? false, redo: created?.canRedo() ?? false });
          },
          onSelection: setSelection,
          onBlocks: setBlocks,
          onAssets: setAssetRequest,
        });
        created = instance;
        const project = base?.editor?.project;
        if (project) instance.loadProject(project);
        else instance.loadMjml(emptyMjml(tokens));
        instance.markSaved();
        setEngine(instance);
        if (!project) setGalleryOpen(true);
      })
      .catch((err: unknown) => {
        if (!cancelled) setEngineError(err);
      });
    return () => {
      cancelled = true;
      created?.destroy();
    };
    // El lienzo se crea una vez por pantalla: el kit y la version de partida ya estan cargados.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty]);

  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty && currentLocation.pathname !== nextLocation.pathname,
  );

  const updateDoc = (next: DocumentDraft) => {
    setDoc(next);
    setDocDirty(true);
    setRevision((r) => r + 1);
  };

  const build = useCallback((): BuiltDocument | null => {
    if (!engine) return null;
    const mjml = writePreheader(engine.mjml(), doc.preheader);
    const compiled = engine.compile(mjml);
    setCompileWarnings(compiled.errors);
    return { mjml, html: compiled.html, warnings: compiled.errors };
  }, [engine, doc.preheader]);

  const buildCheck = useCallback((): CheckRequest | null => {
    const built = build();
    if (!built) return null;
    return {
      kind: template.kind,
      subject: doc.subject,
      html: built.html,
      text: doc.text.trim() ? doc.text : undefined,
    };
  }, [build, template.kind, doc.subject, doc.text]);

  const check = useDeliverabilityCheck(revision, buildCheck, engine !== null);

  /** Guarda el diseno como una version nueva en borrador; las anteriores se conservan. */
  const persist = async (): Promise<number> => {
    const built = build();
    if (!built || !engine) throw new DocumentInvalid(t('templates.editor.loadFailed'));
    const parsed = contentFromDraft(
      { subject: doc.subject, html: built.html, text: doc.text, variables: doc.variables },
      meta,
    );
    setErrors(parsed.errors);
    if (!parsed.content) {
      if (!parsed.errors.html) setRightTab('document');
      throw new DocumentInvalid(parsed.errors.html ?? t('templates.editor.fixDocument'));
    }
    const editor: EditorDocument = { kind: EDITOR_KIND_GRAPESJS_MJML, project: engine.project(), mjml: built.mjml };
    const editorBytes = new TextEncoder().encode(JSON.stringify(editor)).length;
    if (editorBytes > meta.limits.max_editor_bytes) {
      throw new DocumentInvalid(
        t('templates.editor.tooLarge', { n: meta.limits.max_editor_bytes }),
      );
    }
    const content: TemplateContent = { ...parsed.content, editor };
    const { data: created } = await templatesApi.createVersion(template.id, content);
    engine.markSaved();
    setCanvasDirty(false);
    setDocDirty(false);
    setSaved(created.version);
    setPublishIssues(null);
    return created.version;
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      const version = await persist();
      toast.success(t('templates.editor.saved', { n: version }));
    } catch (err) {
      setSaveError(err);
    } finally {
      setSaving(false);
    }
  };

  const publish = async () => {
    setSaveError(null);
    let version: number;
    try {
      version = saved !== null && !dirty ? saved : await persist();
    } catch (err) {
      if (!(err instanceof DocumentInvalid)) throw err;
      setSaveError(err);
      setPublishing(false);
      return;
    }
    try {
      await templatesApi.publish(template.id, version);
    } catch (err) {
      if (!isApiError(err) || !err.is(ERROR_CODES.DELIVERABILITY_FAILED)) throw err;
      const issues =
        deliverabilityIssues(err) ?? (await templatesApi.checkVersion(template.id, version)).issues;
      setPublishIssues(issues);
      setRightTab('check');
      setPublishing(false);
      toast.error(t('templates.check.publishBlocked'));
      return;
    }
    setSaved(null);
    setPublishIssues(null);
    setPublishing(false);
    toast.success(t('templates.versions.published', { n: version }));
  };

  const chooseGallery = (choice: GalleryTemplate, mjml: string) => {
    setGalleryOpen(false);
    if (engine && (dirty || base?.editor)) setPendingGallery({ template: choice, mjml });
    else applyChoice(choice, mjml);
  };

  const applyChoice = (choice: GalleryTemplate, mjml: string) => {
    if (!engine) return;
    const applied = applyGallery(doc, mjml, choice.subject, choice.variables);
    engine.loadMjml(applied.canvas);
    updateDoc(applied.doc);
    setCanvasDirty(true);
  };

  const insertAsset = (asset: TemplateAsset) => {
    if (assetRequest) {
      assetRequest.select(asset.url, asset.name);
      setAssetRequest(null);
      return;
    }
    const alt = asset.name.replace(/\.[a-z0-9]+$/i, '');
    engine?.insertMjml(
      `<mj-image src="${encodeURI(asset.url)}" alt="${alt.replace(/"/g, '')}" padding="10px 25px" />`,
    );
    setLibraryOpen(false);
  };

  const focusPreheader = () => {
    setRightTab('document');
    window.setTimeout(() => preheaderRef.current?.focus(), 0);
  };

  const rightTabs: { id: RightTab; label: string }[] = [
    { id: 'styles', label: t('templates.editor.tab.styles') },
    { id: 'traits', label: t('templates.editor.tab.traits') },
    { id: 'document', label: t('templates.editor.tab.document') },
    { id: 'variables', label: t('templates.editor.tab.variables') },
    { id: 'brand', label: t('templates.editor.tab.brand') },
    { id: 'check', label: t('templates.editor.tab.check') },
  ];

  const statusLabel =
    saved !== null
      ? t('templates.editor.editingDraft', { n: saved })
      : base
        ? t('templates.editor.newFrom', { n: base.version })
        : t('templates.editor.newVersion');

  return (
    <div className="cf-editor">
      <header className="cf-editor__bar">
        <Link to={paths.template(template.id)} className="cf-editor__back">
          <IconChevronLeft size={16} />
          {t('templates.editor.back')}
        </Link>
        <div className="cf-editor__title">
          <strong>{template.name}</strong>
          <span className="cf-text-sm cf-text-secondary">
            {statusLabel}
            {dirty ? ` · ${t('templates.editor.unsaved')}` : ''}
          </span>
        </div>
        <div className="cf-editor__tools">
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconUndo size={16} />}
            disabled={!history.undo}
            onClick={() => engine?.undo()}
          >
            {t('templates.editor.undo')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<IconRedo size={16} />}
            disabled={!history.redo}
            onClick={() => engine?.redo()}
          >
            {t('templates.editor.redo')}
          </Button>
          <span className="cf-editor__sep" aria-hidden="true" />
          <Button
            size="sm"
            variant={device === 'desktop' ? 'secondary' : 'ghost'}
            iconOnly
            aria-pressed={device === 'desktop'}
            icon={<IconMonitor size={16} />}
            onClick={() => {
              setDevice('desktop');
              engine?.setDevice('desktop');
            }}
          >
            {t('templates.editor.desktop')}
          </Button>
          <Button
            size="sm"
            variant={device === 'mobile' ? 'secondary' : 'ghost'}
            iconOnly
            aria-pressed={device === 'mobile'}
            icon={<IconSmartphone size={16} />}
            onClick={() => {
              setDevice('mobile');
              engine?.setDevice('mobile');
            }}
          >
            {t('templates.editor.mobile')}
          </Button>
          <Button
            size="sm"
            variant={dark ? 'secondary' : 'ghost'}
            iconOnly
            aria-pressed={dark}
            icon={<IconMoon size={16} />}
            onClick={() => {
              engine?.setDarkPreview(!dark);
              setDark(!dark);
            }}
          >
            {t('templates.editor.darkPreview')}
          </Button>
          <span className="cf-editor__sep" aria-hidden="true" />
          <Button
            size="sm"
            icon={<IconImage size={16} />}
            disabled={!engine || !can(...PERMISSIONS.templateAssets.read)}
            onClick={() => setLibraryOpen(true)}
          >
            {t('templates.editor.images')}
          </Button>
          <Button
            size="sm"
            icon={<IconGrid size={16} />}
            disabled={!engine}
            onClick={() => setGalleryOpen(true)}
          >
            {t('templates.gallery.title')}
          </Button>
          {canSave ? (
            <Button
              size="sm"
              icon={<IconSave size={16} />}
              loading={saving}
              disabled={!engine}
              onClick={() => void save()}
            >
              {t('templates.versions.saveDraft')}
            </Button>
          ) : null}
          {canPublish && canSave ? (
            <Button
              size="sm"
              variant="primary"
              icon={<IconSend size={16} />}
              disabled={!engine}
              onClick={() => setPublishing(true)}
            >
              {t('templates.versions.publish')}
            </Button>
          ) : null}
        </div>
      </header>

      {!canSave || handDrafted || saveError || engineError ? (
        <div className="cf-editor__notices">
          {!canSave ? (
            <Alert tone="info">
              {archived ? t('templates.archivedNotice') : t('templates.editor.readOnly')}
            </Alert>
          ) : null}
          {handDrafted ? (
            <Alert tone="info">
              {t('templates.editor.handDrafted', { n: base?.version ?? 0 })}
            </Alert>
          ) : null}
          {saveError ? (
            <Alert tone="danger" title={t('templates.editor.saveFailed')}>
              {saveError instanceof DocumentInvalid ? saveError.message : errorMessage(saveError)}
            </Alert>
          ) : null}
          {engineError ? (
            <Alert tone="danger" title={t('templates.editor.loadFailed')}>
              {errorMessage(engineError)}
            </Alert>
          ) : null}
        </div>
      ) : null}

      <div className="cf-editor__main">
        <aside className="cf-editor__panel cf-editor__panel--left">
          <Tabs
            items={[
              { id: 'blocks' as const, label: t('templates.editor.tab.blocks') },
              { id: 'layers' as const, label: t('templates.editor.tab.layers') },
            ]}
            value={leftTab}
            onChange={setLeftTab}
            label={t('templates.editor.panels')}
          />
          <div className="cf-editor__panel-body" hidden={leftTab !== 'blocks'}>
            <BlocksPanel
              blocks={blocks}
              onAppend={(id: BlockId) => engine?.appendBlock(id)}
              onPreheader={focusPreheader}
            />
          </div>
          <div className="cf-editor__panel-body" hidden={leftTab !== 'layers'} ref={layersRef} />
        </aside>

        <div className="cf-editor__canvas">
          {!engine && !engineError ? <LoadingBlock /> : null}
          <div ref={canvasRef} className="cf-editor__frame" />
        </div>

        <aside className="cf-editor__panel cf-editor__panel--right">
          <Tabs
            items={rightTabs}
            value={rightTab}
            onChange={setRightTab}
            label={t('templates.editor.panels')}
          />
          <div className="cf-editor__panel-body" hidden={rightTab !== 'styles'}>
            <span className="cf-text-sm cf-text-secondary">{t('templates.editor.stylesHint')}</span>
            <div ref={stylesRef} />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'traits'}>
            <span className="cf-text-sm cf-text-secondary">{t('templates.editor.traitsHint')}</span>
            <div ref={traitsRef} />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'document'}>
            <DocumentPanel
              value={doc}
              onChange={updateDoc}
              errors={errors}
              meta={meta}
              preheaderRef={preheaderRef}
              readOnly={!canSave}
              onGenerateText={() => {
                const built = build();
                if (built) updateDoc({ ...doc, text: htmlToText(built.html) });
              }}
            />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'variables'}>
            <VariablesPanel
              declared={doc.variables}
              reserved={meta.reserved_variables}
              selection={selection}
              onInsertText={(token) => engine?.insertText(token)}
              onAppendToLink={(token) => engine?.appendToLink(token)}
            />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'brand'}>
            <BrandPanel
              kit={brand.kit}
              unavailable={brand.unavailable}
              logo={brand.logo}
              readOnly={!canSave}
              onInsertLogo={() => {
                if (brand.logo) engine?.insertMjml(logoImage(tokens, brand.logo.url));
              }}
              onInsertFooter={() => engine?.appendBlock('legal-footer')}
            />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'check'}>
            <DeliverabilityPanel
              state={check}
              publishIssues={publishIssues}
              compileWarnings={compileWarnings}
            />
          </div>
        </aside>
      </div>

      {check.result ? (
        <div className="cf-editor__status" role="status">
          <Badge tone={check.result.passed ? 'success' : 'danger'}>
            {check.result.passed ? t('templates.check.passed') : t('templates.check.failed')}
          </Badge>
        </div>
      ) : null}

      {galleryOpen && engine ? (
        <GalleryModal
          brand={tokens}
          compile={engine.compile}
          onChoose={chooseGallery}
          onClose={() => setGalleryOpen(false)}
        />
      ) : null}
      <AssetLibrary
        open={libraryOpen || assetRequest !== null}
        onClose={() => {
          assetRequest?.close();
          setAssetRequest(null);
          setLibraryOpen(false);
        }}
        onPick={canSave ? insertAsset : undefined}
      />
      <ConfirmDialog
        open={pendingGallery !== null}
        title={t('templates.gallery.replaceTitle')}
        message={t('templates.gallery.replaceConfirm')}
        confirmLabel={t('templates.gallery.use')}
        onCancel={() => setPendingGallery(null)}
        onConfirm={async () => {
          if (pendingGallery) applyChoice(pendingGallery.template, pendingGallery.mjml);
          setPendingGallery(null);
        }}
      />
      <ConfirmDialog
        open={publishing}
        title={t('templates.versions.publish')}
        message={t('templates.editor.publishConfirm')}
        confirmLabel={t('templates.versions.publish')}
        onCancel={() => setPublishing(false)}
        onConfirm={publish}
      />
      <ConfirmDialog
        open={blocker.state === 'blocked'}
        title={t('templates.editor.leaveTitle')}
        message={t('templates.editor.leaveConfirm')}
        confirmLabel={t('templates.editor.leave')}
        danger
        onCancel={() => blocker.reset?.()}
        onConfirm={async () => blocker.proceed?.()}
      />
    </div>
  );
}
