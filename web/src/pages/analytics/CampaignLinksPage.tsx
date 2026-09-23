import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  analyticsApi,
  analyticsMeta,
  type CampaignLinksReport,
  type LinkReport,
} from '@/api/analytics';
import { campaignsApi, type Campaign } from '@/api/campaigns';
import { isApiError } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import { templatesApi, type RenderedTemplate, type TemplateVariable } from '@/api/templates';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Alert,
  Button,
  Card,
  DataTable,
  DescriptionList,
  EmptyState,
  ErrorState,
  HtmlPreviewFrame,
  PageHeader,
  Skeleton,
  type Column,
} from '@/design/components';
import { formatRate } from '@/lib/decimal';
import { formatDateTime } from '@/lib/format';
import { getLocale, t } from '@/i18n';
import { paths } from '@/paths';
import { buildRenderVariables, initialValues, type ValueDraft } from '@/pages/templates/variables';
import { VariableValuesForm } from '@/pages/templates/VariableValuesForm';
import { ResourceGate } from '@/pages/shared/ResourceGate';
import { heatmapHtml } from './linkHeatmap';

const HTTP_NOT_FOUND = 404;

export default function CampaignLinksPage() {
  const { id = '' } = useParams();
  const { can } = useAccess();
  const meta = useResource(analyticsMeta);
  const canReadCampaign = can(...PERMISSIONS.campaigns.read);
  const campaign = useQuery(
    async () => (canReadCampaign ? (await campaignsApi.get(id)).data : null),
    [id, canReadCampaign],
  );
  const title = campaign.data?.name ?? t('analytics.links.title');

  return (
    <div>
      <PageHeader
        title={title}
        description={t('analytics.links.subtitle')}
        back={{ to: paths.analytics, label: t('nav.analytics') }}
      />
      <ResourceGate resource={meta}>
        {(m) => (
          <LinksView
            campaignId={id}
            campaign={campaign.data}
            campaignLoading={canReadCampaign && campaign.loading}
            limit={m.links.max_limit}
          />
        )}
      </ResourceGate>
    </div>
  );
}

function LinksView({
  campaignId,
  campaign,
  campaignLoading,
  limit,
}: {
  campaignId: string;
  campaign: Campaign | null;
  campaignLoading: boolean;
  limit: number;
}) {
  const { can, hasModule } = useAccess();
  const format = useMemo(() => new Intl.NumberFormat(getLocale()), []);
  const percent = useMemo(
    () =>
      new Intl.NumberFormat(getLocale(), {
        style: 'percent',
        minimumFractionDigits: 1,
        maximumFractionDigits: 1,
      }),
    [],
  );
  const links = useQuery(() => analyticsApi.campaignLinks(campaignId, limit), [campaignId, limit]);
  // Sin envios la campana no existe para analytics (404): solo falta el resumen.
  const report = useQuery(async () => {
    try {
      return await analyticsApi.campaign(campaignId);
    } catch (err) {
      if (isApiError(err) && err.status === HTTP_NOT_FOUND) return null;
      throw err;
    }
  }, [campaignId]);

  const badge = useCallback(
    (s: number, clicks: number) =>
      t('analytics.links.badge', { share: percent.format(s), n: format.format(clicks) }),
    [percent, format],
  );
  const data = links.data;
  const share = (clicks: number) =>
    data && data.total_clicks > 0 ? percent.format(clicks / data.total_clicks) : t('common.dash');

  const columns: Column<LinkReport>[] = [
    {
      key: 'url',
      header: t('analytics.links.url'),
      render: (l) =>
        l.other ? (
          <span className="cf-text-muted">{t('analytics.links.other')}</span>
        ) : (
          <span className="cf-mono cf-text-sm" style={{ wordBreak: 'break-all' }}>
            {l.url}
          </span>
        ),
    },
    {
      key: 'clicks',
      header: t('analytics.links.clicks'),
      align: 'right',
      render: (l) => format.format(l.clicks),
    },
    {
      key: 'unique',
      header: t('analytics.links.unique'),
      align: 'right',
      render: (l) => format.format(l.unique_clicks),
    },
    {
      key: 'share',
      header: t('analytics.links.share'),
      align: 'right',
      render: (l) => share(l.clicks),
    },
    {
      key: 'last',
      header: t('analytics.links.lastClick'),
      render: (l) => formatDateTime(l.last_clicked_at),
    },
  ];

  const templateVersion = campaign?.template_version ?? null;
  const canPreview =
    hasModule(MODULES.templates) &&
    can(...PERMISSIONS.templates.read) &&
    can(...PERMISSIONS.templates.render);

  return (
    <div className="cf-stack">
      <Card title={t('analytics.links.summary')}>
        {links.error ? (
          <ErrorState error={links.error} onRetry={links.reload} />
        ) : !data ? (
          <Skeleton lines={2} />
        ) : (
          <DescriptionList
            items={[
              { label: t('analytics.links.totalClicks'), value: format.format(data.total_clicks) },
              { label: t('analytics.links.totalLinks'), value: format.format(data.total_links) },
              {
                label: t('analytics.counter.clicked_unique'),
                value: report.data
                  ? format.format(report.data.totals.clicked_unique)
                  : t('common.dash'),
              },
              {
                label: t('analytics.rate.click'),
                value: report.data ? formatRate(report.data.rates.click) : t('common.dash'),
              },
            ]}
          />
        )}
      </Card>

      <Card title={t('analytics.links.heatmap')} description={t('analytics.links.heatmapHint')}>
        {campaignLoading ? (
          <Skeleton lines={4} />
        ) : !campaign ? (
          <EmptyState title={t('analytics.links.noCampaign')} />
        ) : !canPreview ? (
          <EmptyState title={t('analytics.links.noPreviewPermission')} />
        ) : templateVersion === null ? (
          <EmptyState title={t('analytics.links.noVersion')} />
        ) : !data ? (
          <Skeleton lines={4} />
        ) : (
          <HeatmapPreview
            key={`${campaign.template_id}:${templateVersion}`}
            templateId={campaign.template_id}
            version={templateVersion}
            links={data}
            label={badge}
          />
        )}
      </Card>

      <Card
        title={t('analytics.links.table')}
        description={
          data && data.links.filter((l) => !l.other).length < data.total_links
            ? t('analytics.links.truncated', { n: format.format(data.links.length) })
            : undefined
        }
      >
        <DataTable
          columns={columns}
          rows={data?.links ?? []}
          rowKey={(l) => (l.other ? '' : l.url)}
          loading={links.loading}
          error={links.error}
          onRetry={links.reload}
          empty={{ title: t('analytics.links.empty') }}
        />
        {hasModule(MODULES.campaigns) ? (
          <div className="cf-form__actions">
            <Link to={paths.campaign(campaignId)}>{t('analytics.links.openCampaign')}</Link>
          </div>
        ) : null}
      </Card>
    </div>
  );
}

function HeatmapPreview({
  templateId,
  version,
  links,
  label,
}: {
  templateId: string;
  version: number;
  links: CampaignLinksReport;
  label: (share: number, clicks: number) => string;
}) {
  const content = useQuery(
    async () => (await templatesApi.getVersion(templateId, version)).data,
    [templateId, version],
  );
  if (content.error) return <ErrorState error={content.error} onRetry={content.reload} />;
  if (!content.data) return <Skeleton lines={4} />;
  return (
    <RenderedHeatmap
      templateId={templateId}
      version={version}
      variables={content.data.variables ?? []}
      links={links}
      label={label}
    />
  );
}

function RenderedHeatmap({
  templateId,
  version,
  variables,
  links,
  label,
}: {
  templateId: string;
  version: number;
  variables: readonly TemplateVariable[];
  links: CampaignLinksReport;
  label: (share: number, clicks: number) => string;
}) {
  const [values, setValues] = useState<ValueDraft>(() => initialValues(variables));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [rendered, setRendered] = useState<RenderedTemplate | null>(null);
  const render = useAction(async (draft: ValueDraft) => {
    const built = buildRenderVariables(variables, draft);
    setErrors(built.errors);
    if (Object.keys(built.errors).length) return;
    const { data } = await templatesApi.preview(templateId, {
      version,
      variables: built.variables,
    });
    setRendered(data);
  });

  // Primer render con los valores por defecto; si falta alguno obligatorio se pide.
  useEffect(() => {
    void render.run(initialValues(variables));
    // Solo al montar: el componente se vuelve a crear al cambiar de version.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const heat = useMemo(
    () =>
      rendered
        ? heatmapHtml(rendered.html, links.links, { totalClicks: links.total_clicks, label })
        : null,
    [rendered, links, label],
  );
  const unmatched = heat
    ? links.links.filter((l) => !l.other && !heat.matched.has(l.url)).length
    : 0;

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      {variables.length > 0 ? (
        <form
          className="cf-form"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void render.run(values);
          }}
        >
          <VariableValuesForm
            idPrefix={`heatmap-${version}`}
            variables={variables}
            values={values}
            onChange={setValues}
            errors={errors}
          />
          <div className="cf-form__actions">
            <Button type="submit" loading={render.busy}>
              {t('templates.preview.render')}
            </Button>
          </div>
        </form>
      ) : null}
      {render.error ? (
        <div className="cf-form__error" role="alert">
          {errorMessage(render.error)}
        </div>
      ) : null}
      {heat ? (
        <>
          {unmatched > 0 ? (
            <Alert tone="info">{t('analytics.links.unmatched', { n: unmatched })}</Alert>
          ) : null}
          <HtmlPreviewFrame html={heat.html} title={t('analytics.links.heatmap')} height={560} />
        </>
      ) : !render.error && Object.keys(errors).length === 0 ? (
        <Skeleton lines={4} />
      ) : null}
    </div>
  );
}
