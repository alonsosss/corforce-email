import { useState } from 'react';
import { errorMessage } from '@/api/messages';
import {
  templatesApi,
  templatesMeta,
  type BrandKit,
  type BrandKitFooter,
  type TemplateAsset,
  type TemplatesMeta,
} from '@/api/templates';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import {
  Alert,
  Button,
  Card,
  ChipsInput,
  ErrorState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconImage, IconPlus, IconTrash } from '@/design/icons';
import { useQuery } from '@/hooks/useQuery';
import { formatDateTime } from '@/lib/format';
import { rules } from '@/lib/validate';
import { t } from '@/i18n';
import { MissingPermission } from '@/pages/shared/MissingPermission';
import { normalizeImageHost } from './imageHosts';
import { AssetLibrary } from '../AssetLibrary';
import { findAsset } from '../assets';
import { safeHttpUrl } from '../editor/brand';
import '../templateMedia.css';

const HEX = /^#[0-9A-Fa-f]{6}$/;

type FooterErrors = Partial<Record<keyof BrandKitFooter, string>>;

interface KitErrors {
  colors?: Record<number, string>;
  footer?: FooterErrors;
}

interface LoadedKit {
  kit: BrandKit;
  logo: TemplateAsset | null;
  meta: TemplatesMeta;
}

export default function BrandKitPage() {
  const { can } = useAccess();
  const canRead = can(...PERMISSIONS.brandKit.read);
  const canReadAssets = can(...PERMISSIONS.templateAssets.read);

  const data = useQuery(async (): Promise<LoadedKit | null> => {
    if (!canRead) return null;
    const [kit, meta] = await Promise.all([templatesApi.brandKit(), templatesMeta.get()]);
    const logo = kit.logo_asset_id && canReadAssets ? await findAsset(kit.logo_asset_id) : null;
    return { kit, logo, meta };
  }, [canRead, canReadAssets]);

  if (!canRead) return <MissingPermission title={t('templates.brandKit.title')} />;

  return (
    <div>
      <PageHeader
        title={t('templates.brandKit.title')}
        description={t('templates.brandKit.subtitle')}
      />
      {data.error ? (
        <Card>
          <ErrorState error={data.error} onRetry={data.reload} />
        </Card>
      ) : !data.data ? (
        <Card>
          <Skeleton lines={8} />
        </Card>
      ) : (
        <BrandKitForm
          key={data.data.kit.updated_at ?? 'empty'}
          loaded={data.data}
          onSaved={data.reload}
        />
      )}
    </div>
  );
}

function validate(colors: string[], footer: BrandKitFooter): KitErrors {
  const errors: KitErrors = {};
  colors.forEach((color, index) => {
    if (!HEX.test(color))
      errors.colors = { ...errors.colors, [index]: t('templates.brandKit.colorInvalid') };
  });
  const footerErrors: FooterErrors = {};
  if (footer.website.trim() && !safeHttpUrl(footer.website)) {
    footerErrors.website = t('templates.brandKit.websiteInvalid');
  }
  if (footer.support_email.trim()) {
    const email = rules.email(footer.support_email);
    if (email) footerErrors.support_email = email;
  }
  if (Object.keys(footerErrors).length) errors.footer = footerErrors;
  return errors;
}

function BrandKitForm({ loaded, onSaved }: { loaded: LoadedKit; onSaved: () => void }) {
  const toast = useToast();
  const { can } = useAccess();
  const canUpdate = can(...PERMISSIONS.brandKit.update);
  const canPickLogo = can(...PERMISSIONS.templateAssets.read);
  const { kit, meta } = loaded;
  const {
    max_brand_colors: maxColors,
    max_brand_fonts: maxFonts,
    max_brand_image_hosts: maxImageHosts,
  } = meta.limits;
  const [logo, setLogo] = useState<TemplateAsset | null>(loaded.logo);
  const [logoId, setLogoId] = useState<string | null>(kit.logo_asset_id);
  const [colors, setColors] = useState<string[]>(kit.colors);
  const [fonts, setFonts] = useState<string[]>(kit.fonts);
  const [footer, setFooter] = useState<BrandKitFooter>(kit.footer);
  const [imageHosts, setImageHosts] = useState<string[]>(kit.image_hosts);
  const [errors, setErrors] = useState<KitErrors>({});
  const [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);

  const setFooterField = (field: keyof BrandKitFooter, value: string) =>
    setFooter((prev) => ({ ...prev, [field]: value }));

  const setColor = (index: number, value: string) =>
    setColors((prev) => prev.map((c, i) => (i === index ? value : c)));

  const submit = async () => {
    const trimmed: BrandKitFooter = {
      company: footer.company.trim(),
      address: footer.address.trim(),
      website: footer.website.trim(),
      support_email: footer.support_email.trim(),
    };
    const normalized = colors.map((c) => c.trim().toUpperCase());
    const next = validate(normalized, trimmed);
    setErrors(next);
    if (next.colors || next.footer) return;
    setBusy(true);
    setSaveError(null);
    try {
      await templatesApi.saveBrandKit({
        logo_asset_id: logoId,
        colors: normalized,
        fonts,
        footer: trimmed,
        image_hosts: imageHosts,
      });
      toast.success(t('templates.brandKit.saved'));
      onSaved();
    } catch (err) {
      setSaveError(err);
    } finally {
      setBusy(false);
    }
  };

  const readOnly = !canUpdate;

  return (
    <form
      className="cf-stack"
      style={{ gap: 'var(--cf-space-4)' }}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      {kit.updated_at === null ? <Alert tone="info">{t('templates.brandKit.empty')}</Alert> : null}
      {!footer.address.trim() ? (
        <Alert tone="warning">{t('templates.brandKit.missingAddress')}</Alert>
      ) : null}
      <Card title={t('templates.brandKit.logo')} description={t('templates.brandKit.logoHint')}>
        <div className="cf-inline" style={{ gap: 'var(--cf-space-4)' }}>
          {logo ? (
            <div className="cf-brand-logo">
              <img src={logo.url} alt={logo.name} />
            </div>
          ) : (
            <span className="cf-text-sm cf-text-muted">
              {logoId ? t('templates.brandKit.logoMissing') : t('templates.brandKit.noLogo')}
            </span>
          )}
          {!readOnly && canPickLogo ? (
            <Button icon={<IconImage size={16} />} onClick={() => setPicking(true)}>
              {t('templates.brandKit.chooseLogo')}
            </Button>
          ) : null}
          {!readOnly && logoId ? (
            <Button
              variant="ghost"
              icon={<IconTrash size={16} />}
              onClick={() => {
                setLogo(null);
                setLogoId(null);
              }}
            >
              {t('templates.brandKit.removeLogo')}
            </Button>
          ) : null}
        </div>
      </Card>
      <Card title={t('templates.brandKit.colors')} description={t('templates.brandKit.colorsHint')}>
        <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
          {colors.length === 0 ? (
            <span className="cf-text-sm cf-text-muted">{t('common.none')}</span>
          ) : null}
          {colors.map((color, index) => (
            <div key={index} className="cf-stack" style={{ gap: 'var(--cf-space-1)' }}>
              <div className="cf-inline">
                <input
                  type="color"
                  className="cf-color-input"
                  aria-label={t('templates.brandKit.colorN', { n: index + 1 })}
                  value={HEX.test(color) ? color : '#000000'}
                  disabled={readOnly}
                  onChange={(e) => setColor(index, e.target.value.toUpperCase())}
                />
                <Input
                  className="cf-mono"
                  aria-label={t('templates.brandKit.colorN', { n: index + 1 })}
                  value={color}
                  readOnly={readOnly}
                  invalid={Boolean(errors.colors?.[index])}
                  onChange={(e) => setColor(index, e.target.value)}
                />
                {!readOnly ? (
                  <Button
                    variant="ghost"
                    iconOnly
                    icon={<IconTrash size={16} />}
                    onClick={() => setColors((prev) => prev.filter((_, i) => i !== index))}
                  >
                    {t('templates.brandKit.removeColor')}
                  </Button>
                ) : null}
              </div>
              {errors.colors?.[index] ? (
                <span className="cf-field__error" role="alert">
                  {errors.colors[index]}
                </span>
              ) : null}
            </div>
          ))}
          {!readOnly ? (
            <div className="cf-inline">
              <Button
                size="sm"
                icon={<IconPlus size={16} />}
                disabled={colors.length >= maxColors}
                onClick={() => setColors((prev) => [...prev, '#000000'])}
              >
                {t('templates.brandKit.addColor')}
              </Button>
              <span className="cf-text-sm cf-text-secondary">
                {t('templates.brandKit.maxColors', { n: maxColors })}
              </span>
            </div>
          ) : null}
        </div>
      </Card>
      <Card title={t('templates.brandKit.fonts')} description={t('templates.brandKit.fontsHint')}>
        <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
          {fonts.length === 0 ? (
            <span className="cf-text-sm cf-text-muted">{t('common.none')}</span>
          ) : null}
          {fonts.map((name, index) => {
            const font = meta.brand_fonts.find((f) => f.name === name);
            return (
              <div key={name} className="cf-inline">
                <span style={{ fontFamily: font?.stack }}>{name}</span>
                <span className="cf-text-sm cf-text-secondary">
                  {index === 0 ? t('templates.brandKit.fontPrimary') : font?.stack}
                </span>
                {!readOnly ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={<IconTrash size={16} />}
                    onClick={() => setFonts((prev) => prev.filter((f) => f !== name))}
                  >
                    {t('templates.brandKit.removeFont', { name })}
                  </Button>
                ) : null}
              </div>
            );
          })}
          {!readOnly && fonts.length < maxFonts ? (
            <FormField label={t('templates.brandKit.addFont')} htmlFor="brand-kit-font">
              <Select
                id="brand-kit-font"
                value=""
                placeholder={t('templates.brandKit.chooseFont')}
                options={meta.brand_fonts
                  .filter((f) => !fonts.includes(f.name))
                  .map((f) => ({
                    value: f.name,
                    label: f.web ? t('templates.brandKit.webFont', { name: f.name }) : f.name,
                  }))}
                onChange={(e) => {
                  const value = e.target.value;
                  if (value) setFonts((prev) => [...prev, value]);
                }}
              />
            </FormField>
          ) : null}
        </div>
      </Card>
      <Card title={t('templates.brandKit.footer')} description={t('templates.brandKit.footerHint')}>
        <div className="cf-form__row">
          <FormField label={t('templates.brandKit.company')} htmlFor="brand-kit-company">
            <Input
              id="brand-kit-company"
              value={footer.company}
              readOnly={readOnly}
              onChange={(e) => setFooterField('company', e.target.value)}
            />
          </FormField>
          <FormField
            label={t('templates.brandKit.address')}
            htmlFor="brand-kit-address"
            hint={t('templates.brandKit.addressHint')}
          >
            <Input
              id="brand-kit-address"
              value={footer.address}
              readOnly={readOnly}
              onChange={(e) => setFooterField('address', e.target.value)}
            />
          </FormField>
        </div>
        <div className="cf-form__row">
          <FormField
            label={t('templates.brandKit.website')}
            htmlFor="brand-kit-website"
            error={errors.footer?.website}
          >
            <Input
              id="brand-kit-website"
              type="url"
              value={footer.website}
              readOnly={readOnly}
              invalid={Boolean(errors.footer?.website)}
              onChange={(e) => setFooterField('website', e.target.value)}
            />
          </FormField>
          <FormField
            label={t('templates.brandKit.supportEmail')}
            htmlFor="brand-kit-email"
            error={errors.footer?.support_email}
          >
            <Input
              id="brand-kit-email"
              type="email"
              value={footer.support_email}
              readOnly={readOnly}
              invalid={Boolean(errors.footer?.support_email)}
              onChange={(e) => setFooterField('support_email', e.target.value)}
            />
          </FormField>
        </div>
      </Card>
      <Card
        title={t('templates.brandKit.imageHosts')}
        description={t('templates.brandKit.imageHostsHint')}
      >
        <FormField
          label={t('templates.brandKit.imageHostsLabel')}
          htmlFor="brand-kit-image-hosts"
          hint={t('templates.brandKit.imageHostsMax', { n: maxImageHosts })}
        >
          <ChipsInput
            id="brand-kit-image-hosts"
            values={imageHosts}
            onChange={(next) => setImageHosts(next.slice(0, maxImageHosts))}
            normalize={normalizeImageHost}
            placeholder={t('templates.brandKit.imageHostsPlaceholder')}
            disabled={readOnly}
            removeLabel={(host) => t('templates.brandKit.removeImageHost', { host })}
            rejectedLabel={(rejected) =>
              t('templates.brandKit.imageHostInvalid', { hosts: rejected.join(', ') })
            }
          />
        </FormField>
      </Card>
      {saveError ? <Alert tone="danger">{errorMessage(saveError)}</Alert> : null}
      <div className="cf-inline" style={{ justifyContent: 'space-between' }}>
        <span className="cf-text-sm cf-text-secondary">
          {kit.updated_at
            ? t('templates.brandKit.updatedAt', { date: formatDateTime(kit.updated_at) })
            : null}
        </span>
        {!readOnly ? (
          <Button type="submit" variant="primary" loading={busy}>
            {t('common.save')}
          </Button>
        ) : null}
      </div>
      <AssetLibrary
        open={picking}
        title={t('templates.brandKit.chooseLogo')}
        onClose={() => setPicking(false)}
        onPick={(asset) => {
          setLogo(asset);
          setLogoId(asset.id);
          setPicking(false);
        }}
      />
    </form>
  );
}
