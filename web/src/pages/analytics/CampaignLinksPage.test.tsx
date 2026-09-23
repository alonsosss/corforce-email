import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import type { AnalyticsMeta, CampaignLinksReport } from '@/api/analytics';
import { api, type QueryParams } from '@/api/client';
import { endpoints } from '@/api/endpoints';
import { ApiError } from '@/api/errors';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { t } from '@/i18n';
import { paths } from '@/paths';
import CampaignLinksPage from './CampaignLinksPage';

const CAMPAIGN_ID = '5d1c9a0e-7b3f-4e2a-9c8d-1f0e2d3c4b5a';
const TEMPLATE_ID = '8a7b6c5d-4e3f-4a1b-9c2d-3e4f5a6b7c8d';

const META: AnalyticsMeta = {
  classes: ['marketing'],
  timezone: 'UTC',
  range: { max_days: 60, default_days: 30 },
  domains: { default_limit: 20, max_limit: 100 },
  links: { default_limit: 100, max_limit: 501 },
  pagination: { default_per_page: 25, max_per_page: 100 },
};

const LINKS: CampaignLinksReport = {
  campaign_id: CAMPAIGN_ID,
  total_links: 2,
  total_clicks: 8,
  limit: 501,
  links: [
    {
      url: 'https://tienda.test/oferta',
      other: false,
      clicks: 6,
      unique_clicks: 4,
      first_clicked_at: '2026-09-20T10:00:00Z',
      last_clicked_at: '2026-09-21T10:00:00Z',
    },
    {
      url: 'https://tienda.test/personal?c=1',
      other: false,
      clicks: 2,
      unique_clicks: 2,
      first_clicked_at: '2026-09-20T10:00:00Z',
      last_clicked_at: '2026-09-21T10:00:00Z',
    },
  ],
};

function mockApi() {
  const get = vi.spyOn(api, 'get').mockImplementation((async (path: string) => {
    const responses: Record<string, ApiResponse<unknown>> = {
      [endpoints.analytics.meta]: { data: META },
      [endpoints.analytics.campaignLinks(CAMPAIGN_ID)]: { data: LINKS },
      [endpoints.campaigns.byId(CAMPAIGN_ID)]: {
        data: {
          id: CAMPAIGN_ID,
          name: 'Otono 2026',
          template_id: TEMPLATE_ID,
          template_version: 3,
        },
      },
      [endpoints.templates.version(TEMPLATE_ID, 3)]: { data: { version: 3, variables: [] } },
    };
    if (path === endpoints.analytics.campaigns.byId(CAMPAIGN_ID)) {
      throw new ApiError(404, { code: 'NOT_FOUND', message: 'campana sin datos de envio' });
    }
    return responses[path] ?? { data: null };
  }) as typeof api.get);
  const post = vi.spyOn(api, 'post').mockImplementation((async () => ({
    data: {
      subject: 'Otono',
      version: 3,
      text: '',
      html: '<a href="https://tienda.test/oferta">Oferta</a><a href="https://tienda.test/blog">Blog</a>',
    },
  })) as typeof api.post);
  return { get, post };
}

function renderPage(extra: PermissionTriple[], modules: string[]) {
  const [module, resource, action] = PERMISSIONS.analyticsReports.read;
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.analytics, ...modules],
    permissions: [{ module, resource, action }, ...extra],
  });
  render(
    <MemoryRouter initialEntries={[paths.analyticsCampaignLinks(CAMPAIGN_ID)]}>
      <Routes>
        <Route path={paths.analyticsCampaignLinksPattern} element={<CampaignLinksPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

function triple(p: readonly [string, string, string]): PermissionTriple {
  return { module: p[0], resource: p[1], action: p[2] };
}

describe('clics por enlace de una campana', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  it('pide la lista con el tope publicado y pinta la tabla y el mapa de calor', async () => {
    const { get, post } = mockApi();
    renderPage(
      [
        triple(PERMISSIONS.campaigns.read),
        triple(PERMISSIONS.templates.read),
        triple(PERMISSIONS.templates.render),
      ],
      [MODULES.campaigns, MODULES.templates],
    );

    expect(await screen.findByRole('heading', { name: 'Otono 2026' })).toBeInTheDocument();
    expect(await screen.findByText('https://tienda.test/oferta')).toBeInTheDocument();
    const params = get.mock.calls
      .filter(([p]) => p === endpoints.analytics.campaignLinks(CAMPAIGN_ID))
      .map(([, opts]) => opts?.params as QueryParams | undefined);
    expect(params[0]).toMatchObject({ limit: 501 });

    const frame = (await screen.findByTitle(t('analytics.links.heatmap'))) as HTMLIFrameElement;
    const srcdoc = frame.getAttribute('srcdoc') ?? '';
    expect(srcdoc).toContain('data-cf-heat="hot"');
    expect(srcdoc).toContain('data-cf-heat="cold"');
    expect(post).toHaveBeenCalledWith(endpoints.templates.preview(TEMPLATE_ID), {
      body: { version: 3, variables: {} },
    });
    expect(screen.getByText(t('analytics.links.unmatched', { n: 1 }))).toBeInTheDocument();
  });

  it('sin permiso sobre plantillas muestra la tabla y explica por que no hay vista previa', async () => {
    const { post } = mockApi();
    renderPage([triple(PERMISSIONS.campaigns.read)], [MODULES.campaigns]);

    expect(await screen.findByText('https://tienda.test/oferta')).toBeInTheDocument();
    expect(await screen.findByText(t('analytics.links.noPreviewPermission'))).toBeInTheDocument();
    await waitFor(() => expect(post).not.toHaveBeenCalled());
  });
});
