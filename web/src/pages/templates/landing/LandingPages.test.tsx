import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { api } from '@/api/client';
import { endpoints } from '@/api/endpoints';
import type { LandingPage, PageDetail, PagesMeta } from '@/api/pages';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import LandingPagesPage from './LandingPagesPage';
import { PageCreateForm } from './PageCreateForm';

const META: PagesMeta = {
  editor_kinds: ['grapesjs-web'],
  max_html_bytes: 512000,
  max_css_bytes: 128000,
  max_editor_bytes: 2000000,
  max_name_length: 120,
  max_title_length: 200,
  max_description_length: 500,
  max_slug_length: 80,
  slug_pattern: '^[a-z0-9]+(?:-[a-z0-9]+)*$',
  public_prefix: 'https://paginas.empresa.test/p',
};

function landing(partial: Partial<LandingPage>): LandingPage {
  return {
    id: 'p1',
    name: 'Pagina',
    slug: 'pagina',
    status: 'active',
    noindex: true,
    current_version: 0,
    public_url: 'https://paginas.empresa.test/p/pagina',
    created_by: 'u1',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-20T00:00:00Z',
    ...partial,
  };
}

function triple(p: readonly [string, string, string]): PermissionTriple {
  return { module: p[0], resource: p[1], action: p[2] };
}

function grant(...permissions: (readonly [string, string, string])[]) {
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.templates],
    permissions: permissions.map(triple),
  });
}

function renderWithProviders(ui: JSX.Element) {
  return render(
    <MemoryRouter>
      <ToastProvider>{ui}</ToastProvider>
    </MemoryRouter>,
  );
}

describe('paginas de aterrizaje', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  it('enlaza la URL publica solo de las paginas con una version publicada', async () => {
    grant(PERMISSIONS.landingPages.read);
    const published = landing({
      id: 'p1',
      name: 'Lanzamiento',
      slug: 'lanzamiento',
      current_version: 2,
      public_url: 'https://paginas.empresa.test/p/lanzamiento',
    });
    const draft = landing({ id: 'p2', name: 'Borrador', slug: 'borrador' });
    vi.spyOn(api, 'get').mockImplementation((async (path: string) => {
      if (path === endpoints.templates.pages.collection) {
        return {
          data: [published, draft],
          meta: { page: 1, per_page: 20, total: 2, total_pages: 1 },
        } as ApiResponse<unknown>;
      }
      return { data: null };
    }) as typeof api.get);

    renderWithProviders(<LandingPagesPage />);

    const link = await screen.findByRole('link', { name: published.public_url });
    expect(link).toHaveAttribute('href', published.public_url);
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getAllByRole('link')).toHaveLength(1);
    expect(screen.getByText(t('templates.pages.notServed'))).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: t('templates.pages.new') }),
    ).not.toBeInTheDocument();
  });

  it('el alta deriva la direccion del nombre, oculta a buscadores y respeta la editada', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.landingPages.read, PERMISSIONS.landingPages.create);
    vi.spyOn(api, 'get').mockImplementation((async (path: string) =>
      path === endpoints.templates.pages.meta ? { data: META } : { data: null }) as typeof api.get);
    const created: PageDetail = {
      page: landing({ id: 'nueva', name: 'Oferta de Otoño', slug: 'oferta-otono' }),
      current: null,
      versions: [],
    };
    const post = vi
      .spyOn(api, 'post')
      .mockImplementation((async () => ({ data: created })) as typeof api.post);
    const onCreated = vi.fn();

    renderWithProviders(<PageCreateForm onClose={() => undefined} onCreated={onCreated} />);

    const name = await screen.findByLabelText(new RegExp(t('common.name')));
    await user.type(name, 'Oferta de Otoño');
    const slug = screen.getByLabelText(new RegExp(t('templates.pages.slug')));
    expect(slug).toHaveValue('oferta-de-otono');
    expect(
      screen.getByText(
        t('templates.pages.slugHint', { url: `${META.public_prefix}/oferta-de-otono` }),
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(t('templates.pages.noindex'))).toBeChecked();

    await user.clear(slug);
    await user.type(slug, 'oferta-otono');
    await user.type(name, ' 2026');
    expect(slug).toHaveValue('oferta-otono');

    await user.click(screen.getByRole('button', { name: t('common.create') }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(created.page));
    expect(post).toHaveBeenCalledWith(endpoints.templates.pages.collection, {
      body: { name: 'Oferta de Otoño 2026', slug: 'oferta-otono', noindex: true },
    });
  });

  it('el alta no envia una direccion que no cumple el patron del servicio', async () => {
    const user = userEvent.setup();
    grant(PERMISSIONS.landingPages.read, PERMISSIONS.landingPages.create);
    vi.spyOn(api, 'get').mockImplementation((async (path: string) =>
      path === endpoints.templates.pages.meta ? { data: META } : { data: null }) as typeof api.get);
    const post = vi.spyOn(api, 'post');

    renderWithProviders(<PageCreateForm onClose={() => undefined} onCreated={() => undefined} />);

    await user.type(await screen.findByLabelText(new RegExp(t('common.name'))), 'Oferta');
    const slug = screen.getByLabelText(new RegExp(t('templates.pages.slug')));
    await user.clear(slug);
    await user.type(slug, 'oferta--');
    await user.click(screen.getByRole('button', { name: t('common.create') }));
    expect(await screen.findByText(t('validation.slug'))).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
  });
});
