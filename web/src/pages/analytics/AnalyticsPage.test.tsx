import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import type { AnalyticsMeta } from '@/api/analytics';
import { api, type QueryParams } from '@/api/client';
import { endpoints } from '@/api/endpoints';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { t } from '@/i18n';
import AnalyticsPage from './AnalyticsPage';

const META: AnalyticsMeta = {
  classes: ['marketing'],
  timezone: 'America/Lima',
  range: { max_days: 60, default_days: 30 },
  domains: { default_limit: 20, max_limit: 100 },
  links: { default_limit: 100, max_limit: 501 },
  pagination: { default_per_page: 25, max_per_page: 100 },
};

const ZERO = {
  sent: 0,
  delivered: 0,
  bounced_hard: 0,
  bounced_soft: 0,
  complained: 0,
  opened_unique: 0,
  clicked_unique: 0,
  unsubscribed: 0,
  failed: 0,
};
const RATES = {
  delivery: '0',
  bounce: '0',
  complaint: '0',
  open: '0',
  click: '0',
  unsubscribe: '0',
};
const RANGE = { from: '2026-08-15', to: '2026-09-13', timezone: 'America/Lima', class: null };

function mockApi() {
  return vi.spyOn(api, 'get').mockImplementation((async (path: string) => {
    const responses: Record<string, ApiResponse<unknown>> = {
      [endpoints.analytics.meta]: { data: META },
      [endpoints.analytics.overview]: { data: { ...RANGE, totals: ZERO, rates: RATES } },
      [endpoints.analytics.timeseries]: { data: { ...RANGE, points: [] } },
      [endpoints.analytics.domains]: { data: { ...RANGE, limit: 20, domains: [] } },
      [endpoints.analytics.campaigns.collection]: { data: [], meta: { total: 0 } },
    };
    return responses[path] ?? { data: null };
  }) as typeof api.get);
}

function paramsOf(spy: ReturnType<typeof mockApi>, path: string): (QueryParams | undefined)[] {
  return spy.mock.calls.filter(([p]) => p === path).map(([, opts]) => opts?.params);
}

describe('analitica con el catalogo del servicio', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  function renderPage() {
    const [module, resource, action] = PERMISSIONS.analyticsReports.read;
    const permissions: PermissionTriple[] = [{ module, resource, action }];
    useAccessStore.setState({
      loaded: true,
      isAdmin: false,
      policyLoaded: true,
      modules: [MODULES.analytics],
      permissions,
    });
    render(
      <MemoryRouter>
        <AnalyticsPage />
      </MemoryRouter>,
    );
  }

  it('las clases, la zona y la pagina salen de GET /analytics/meta', async () => {
    const get = mockApi();
    renderPage();

    const select = await screen.findByLabelText(t('analytics.class.label'));
    expect(
      within(select)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual([t('analytics.class.all'), t('sendClass.marketing')]);
    expect(
      screen.getByText(t('analytics.range.hint', { tz: 'America/Lima', n: 30 })),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(paramsOf(get, endpoints.analytics.campaigns.collection)[0]).toMatchObject({
        per_page: 25,
      }),
    );
  });

  it('rechaza un rango mayor que el maximo del servicio sin pedir nada', async () => {
    const user = userEvent.setup();
    const get = mockApi();
    renderPage();

    const from = await screen.findByLabelText(t('analytics.range.from'));
    await user.type(from, '2026-01-01');
    await user.type(screen.getByLabelText(t('analytics.range.to')), '2026-03-15');
    const before = paramsOf(get, endpoints.analytics.overview).length;
    const filters = from.closest('form') as HTMLFormElement;
    await user.click(within(filters).getByRole('button', { name: t('common.apply') }));

    expect(
      await screen.findByText(t('analytics.range.tooLong', { max: '60' })),
    ).toBeInTheDocument();
    expect(paramsOf(get, endpoints.analytics.overview)).toHaveLength(before);
  });

  it('el tamano del ranking de dominios se valida contra el maximo publicado', async () => {
    const user = userEvent.setup();
    const get = mockApi();
    renderPage();

    const limit = await screen.findByLabelText(t('analytics.domains.limit'));
    const form = limit.closest('form') as HTMLFormElement;
    await user.type(limit, '500');
    await user.click(within(form).getByRole('button', { name: t('common.apply') }));
    expect(
      await screen.findByText(t('analytics.domains.limitInvalid', { max: '100' })),
    ).toBeInTheDocument();
    expect(paramsOf(get, endpoints.analytics.domains).some((p) => p?.limit === 500)).toBe(false);

    await user.clear(limit);
    await user.type(limit, '50');
    await user.click(within(form).getByRole('button', { name: t('common.apply') }));
    await waitFor(() =>
      expect(paramsOf(get, endpoints.analytics.domains).some((p) => p?.limit === 50)).toBe(true),
    );
  });
});
