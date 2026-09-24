import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useBlocker } from 'react-router-dom';
import { errorMessage } from '@/api/messages';
import type { SubscriptionForm } from '@/api/forms';
import {
  EDITOR_KIND_GRAPESJS_WEB,
  pagesApi,
  type PageDetail,
  type PagesMeta,
  type PageVersion,
} from '@/api/pages';
import type { BrandFont, TemplateAsset } from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import {
  Alert,
  Button,
  ConfirmDialog,
  FormField,
  Input,
  LoadingBlock,
  Tabs,
  Textarea,
  useToast,
} from '@/design/components';
import {
  IconChevronLeft,
  IconImage,
  IconMonitor,
  IconRedo,
  IconSave,
  IconSend,
  IconSmartphone,
  IconUndo,
} from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { AssetLibrary } from '../../AssetLibrary';
import { brandTokens } from '../../editor/brand';
import type { BrandContext } from '../../editor/session';
import { FormPickerModal } from './FormPickerModal';
import { PageBlocksPanel } from './PageBlocksPanel';
import {
  buildPageContent,
  initialPageDocument,
  savedPageDraft,
  type PageDocument,
  type PageDocumentErrors,
} from './pageContent';
import { formMarker, imageTag } from './webBlocks';
import type { PageAssetRequest, PageDeviceId, PageEngine, PageEngineBlocks } from './webEngine';

export interface PageEditorData {
  detail: PageDetail;
  meta: PagesMeta;
  /** Version de partida: el borrador mas reciente o, si no hay, la publicada. */
  base: PageVersion | null;
  brand: BrandContext;
  fonts: BrandFont[];
  /** null sin permiso para leer los formularios de suscripcion. */
  forms: SubscriptionForm[] | null;
}

type LeftTab = 'blocks' | 'layers';
type RightTab = 'styles' | 'traits' | 'document';

/** El documento no se puede guardar: el motivo ya se muestra junto a su campo. */
class DocumentInvalid extends Error {}

/** Texto del marcador de un formulario en el lienzo. */
export function formMarkerLabel(forms: readonly SubscriptionForm[] | null, key: string): string {
  const form = forms?.find((f) => f.embed.key === key);
  return form
    ? t('templates.pages.editor.formMarker', { name: form.name })
    : t('templates.pages.editor.formMarkerUnknown');
}

export function PageEditorWorkspace({ data }: { data: PageEditorData }) {
  const { detail, meta, base, brand, fonts, forms } = data;
  const { page } = detail;
  const archived = page.status === 'archived';
  const toast = useToast();
  const { can } = useAccess();
  const canSave = can(...PERMISSIONS.landingPages.create) && !archived;
  const canPublish = can(...PERMISSIONS.landingPages.publish) && !archived;

  const tokens = useMemo(
    () => brandTokens(brand.kit, brand.logo?.url ?? null, fonts),
    [brand.kit, brand.logo, fonts],
  );
  const palette = useMemo(() => brand.kit?.colors ?? [], [brand.kit]);

  const canvasRef = useRef<HTMLDivElement>(null);
  const stylesRef = useRef<HTMLDivElement>(null);
  const traitsRef = useRef<HTMLDivElement>(null);
  const layersRef = useRef<HTMLDivElement>(null);

  const [engine, setEngine] = useState<PageEngine | null>(null);
  const [engineError, setEngineError] = useState<unknown>(null);
  const [blocks, setBlocks] = useState<PageEngineBlocks | null>(null);
  const [assetRequest, setAssetRequest] = useState<PageAssetRequest | null>(null);
  const [history, setHistory] = useState({ undo: false, redo: false });
  const [canvasDirty, setCanvasDirty] = useState(false);

  const [doc, setDoc] = useState<PageDocument>(() => initialPageDocument(base, page.name));
  const [docDirty, setDocDirty] = useState(false);
  const [errors, setErrors] = useState<PageDocumentErrors>({});
  const [saved, setSaved] = useState<number | null>(() => savedPageDraft(base));

  const [leftTab, setLeftTab] = useState<LeftTab>('blocks');
  const [rightTab, setRightTab] = useState<RightTab>('styles');
  const [device, setDevice] = useState<PageDeviceId>('desktop');
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [pickingForm, setPickingForm] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);

  const dirty = canvasDirty || docDirty;
  const foreignBase = Boolean(base && base.editor?.kind !== EDITOR_KIND_GRAPESJS_WEB);

  useEffect(() => {
    let cancelled = false;
    let created: PageEngine | null = null;
    const container = canvasRef.current;
    const styles = stylesRef.current;
    const traits = traitsRef.current;
    const layers = layersRef.current;
    if (!container || !styles || !traits || !layers) return;
    import('./webEngine')
      .then(({ createPageEngine }) => {
        if (cancelled) return;
        const instance = createPageEngine({
          container,
          styles,
          traits,
          layers,
          brand: tokens,
          palette,
          formLabel: (key) => formMarkerLabel(forms, key),
          onChange: () => {
            setCanvasDirty(created?.isDirty() ?? false);
            setHistory({ undo: created?.canUndo() ?? false, redo: created?.canRedo() ?? false });
          },
          onBlocks: setBlocks,
          onAssets: setAssetRequest,
        });
        created = instance;
        const project =
          base?.editor?.kind === EDITOR_KIND_GRAPESJS_WEB ? base.editor.project : null;
        if (project) instance.loadProject(project);
        else if (base) instance.loadHtml(base.html, base.css);
        instance.markSaved();
        setEngine(instance);
      })
      .catch((err: unknown) => {
        if (!cancelled) setEngineError(err);
      });
    return () => {
      cancelled = true;
      created?.destroy();
    };
    // El lienzo se crea una vez por pantalla: la version de partida ya esta cargada.
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

  const updateDoc = (next: PageDocument) => {
    setDoc(next);
    setDocDirty(true);
  };

  /** Guarda el lienzo como una version nueva en borrador; las anteriores se conservan. */
  const persist = async (): Promise<number> => {
    if (!engine) throw new DocumentInvalid(t('templates.editor.loadFailed'));
    const built = buildPageContent(
      doc,
      { html: engine.html(), css: engine.css(), project: engine.project() },
      meta,
    );
    setErrors(built.errors);
    if (!built.content) {
      if (built.errors.title || built.errors.description) setRightTab('document');
      throw new DocumentInvalid(built.errors.content ?? t('templates.pages.editor.fixDocument'));
    }
    const version = await pagesApi.createVersion(page.id, built.content);
    engine.markSaved();
    setCanvasDirty(false);
    setDocDirty(false);
    setSaved(version.version);
    return version.version;
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      const version = await persist();
      toast.success(t('templates.pages.editor.saved', { n: version }));
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
    await pagesApi.publish(page.id, version);
    setSaved(null);
    setPublishing(false);
    toast.success(t('templates.versions.published', { n: version }));
  };

  const insertAsset = (asset: TemplateAsset) => {
    if (assetRequest) {
      assetRequest.select(asset.url, asset.name);
      setAssetRequest(null);
      return;
    }
    engine?.insertHtml(imageTag(encodeURI(asset.url), asset.name.replace(/\.[a-z0-9]+$/i, '')));
    setLibraryOpen(false);
  };

  const insertForm = (form: SubscriptionForm) => {
    engine?.insertHtml(formMarker(form.embed.key));
    setPickingForm(false);
  };

  const statusLabel =
    saved !== null
      ? t('templates.editor.editingDraft', { n: saved })
      : base
        ? t('templates.editor.newFrom', { n: base.version })
        : t('templates.pages.editor.firstVersion');

  return (
    <div className="cf-editor">
      <header className="cf-editor__bar">
        <Link to={paths.landingPage(page.id)} className="cf-editor__back">
          <IconChevronLeft size={16} />
          {t('templates.pages.editor.back')}
        </Link>
        <div className="cf-editor__title">
          <strong>{page.name}</strong>
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
          <span className="cf-editor__sep" aria-hidden="true" />
          <Button
            size="sm"
            icon={<IconImage size={16} />}
            disabled={!engine || !can(...PERMISSIONS.templateAssets.read)}
            onClick={() => setLibraryOpen(true)}
          >
            {t('templates.editor.images')}
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

      {!canSave || foreignBase || saveError || engineError ? (
        <div className="cf-editor__notices">
          {!canSave ? (
            <Alert tone="info">
              {archived
                ? t('templates.pages.archivedNotice')
                : t('templates.pages.editor.readOnly')}
            </Alert>
          ) : null}
          {foreignBase ? (
            <Alert tone="info">
              {t('templates.pages.editor.foreignBase', { n: base?.version ?? 0 })}
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
            <PageBlocksPanel
              blocks={blocks}
              onAppend={(id) => engine?.appendBlock(id)}
              onInsertForm={forms ? () => setPickingForm(true) : null}
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
            items={[
              { id: 'styles' as const, label: t('templates.editor.tab.styles') },
              { id: 'traits' as const, label: t('templates.editor.tab.traits') },
              { id: 'document' as const, label: t('templates.editor.tab.document') },
            ]}
            value={rightTab}
            onChange={setRightTab}
            label={t('templates.editor.panels')}
          />
          <div className="cf-editor__panel-body" hidden={rightTab !== 'styles'}>
            <span className="cf-text-sm cf-text-secondary">{t('templates.editor.stylesHint')}</span>
            <div ref={stylesRef} />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'traits'}>
            <span className="cf-text-sm cf-text-secondary">
              {t('templates.pages.editor.traitsHint')}
            </span>
            <div ref={traitsRef} />
          </div>
          <div className="cf-editor__panel-body" hidden={rightTab !== 'document'}>
            <PageDocumentPanel
              value={doc}
              onChange={updateDoc}
              errors={errors}
              meta={meta}
              readOnly={!canSave}
            />
          </div>
        </aside>
      </div>

      <AssetLibrary
        open={libraryOpen || assetRequest !== null}
        onClose={() => {
          assetRequest?.close();
          setAssetRequest(null);
          setLibraryOpen(false);
        }}
        onPick={canSave ? insertAsset : undefined}
      />
      {pickingForm && forms && engine ? (
        <FormPickerModal forms={forms} onPick={insertForm} onClose={() => setPickingForm(false)} />
      ) : null}
      <ConfirmDialog
        open={publishing}
        title={t('templates.versions.publish')}
        message={t('templates.pages.editor.publishConfirm', { url: page.public_url })}
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

export interface PageDocumentPanelProps {
  value: PageDocument;
  onChange: (next: PageDocument) => void;
  errors: PageDocumentErrors;
  meta: Pick<PagesMeta, 'max_title_length' | 'max_description_length'>;
  readOnly: boolean;
}

/** Titulo y descripcion de la pagina: lo que muestran la pestana y los buscadores. */
export function PageDocumentPanel({
  value,
  onChange,
  errors,
  meta,
  readOnly,
}: PageDocumentPanelProps) {
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <FormField
        label={t('templates.pages.documentTitle')}
        htmlFor="page-doc-title"
        required
        error={errors.title}
        hint={t('templates.pages.documentTitleHint')}
      >
        <Input
          id="page-doc-title"
          value={value.title}
          readOnly={readOnly}
          maxLength={meta.max_title_length}
          onChange={(e) => onChange({ ...value, title: e.target.value })}
          invalid={Boolean(errors.title)}
        />
      </FormField>
      <FormField
        label={t('common.description')}
        htmlFor="page-doc-description"
        error={errors.description}
        hint={t('templates.pages.documentDescriptionHint')}
      >
        <Textarea
          id="page-doc-description"
          rows={4}
          value={value.description}
          readOnly={readOnly}
          maxLength={meta.max_description_length}
          onChange={(e) => onChange({ ...value, description: e.target.value })}
        />
      </FormField>
    </div>
  );
}
