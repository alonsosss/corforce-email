import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { api } from '@/api/client';
import { endpoints } from '@/api/endpoints';
import type { FormsMeta, FormStats, SubscriptionForm } from '@/api/forms';
import type { ApiResponse } from '@/api/types';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { iframeSnippet, scriptSnippet } from './embed';
import FormDetailPage from './FormDetailPage';

const FORM_ID = '3f2e1d0c-9b8a-4c7d-8e6f-5a4b3c2d1e0f';

const FORM: SubscriptionForm = {
  id: FORM_ID,
  tenant_id: 't1',
  name: 'Boletin semanal',
  status: 'active',
  list_id: 'list-1',
  fields: [
    { key: 'email', label: 'Correo', required: true, placeholder: '' },
    { key: 'first_name', label: 'Nombre', required: false, placeholder: '' },
  ],
  texts: {
    title: 'Suscribete',
    description: '',
    submit_label: '',
    consent_text: 'Acepto recibir el boletin.',
    success_message: 'Revisa tu correo.',
  },
  redirect_url: null,
  allowed_origins: ['https://www.tienda.test'],
  embed: {
    key: 'fk_1',
    iframe_url: 'https://forms.plataforma.test/f/fk_1',
    script_url: 'https://forms.plataforma.test/f/fk_1/embed.js',
    definition_url: 'https://forms.plataforma.test/api/f/fk_1',
    submit_url: 'https://forms.plataforma.test/api/f/fk_1/submit',
  },
  created_by: 'u1',
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-20T00:00:00Z',
};

const STATS: FormStats = {
  from: '2026-08-24',
  to: '2026-09-22',
  submitted: 12,
  confirmation_sent: 10,
  already_subscribed: 1,
  not_reachable: 1,
  confirmed: 7,
  daily: [
    { date: '2026-09-21', submitted: 5, confirmed: 3 },
    { date: '2026-09-22', submitted: 7, confirmed: 4 },
  ],
};

const META: FormsMeta = {
  builtin_fields: [{ key: 'email', type: 'email' }],
  statuses: ['active', 'disabled'],
  limits: {
    max_fields: 20,
    max_name_length: 120,
    max_label_length: 120,
    max_placeholder_length: 120,
    max_title_length: 200,
    max_description_length: 1000,
    max_submit_label_length: 60,
    max_consent_text_length: 2000,
    max_success_message_length: 1000,
    max_allowed_origins: 20,
    max_redirect_url_length: 2048,
    max_stats_days: 90,
  },
  min_fill_seconds: 3,
  token_ttl_seconds: 3600,
};

function triple(p: readonly [string, string, string]): PermissionTriple {
  return { module: p[0], resource: p[1], action: p[2] };
}

function mockApi() {
  return vi.spyOn(api, 'get').mockImplementation((async (path: string) => {
    const responses: Record<string, ApiResponse<unknown>> = {
      [endpoints.contacts.forms.byId(FORM_ID)]: { data: FORM },
      [endpoints.contacts.forms.stats(FORM_ID)]: { data: STATS },
      [endpoints.contacts.forms.meta]: { data: META },
    };
    return responses[path] ?? { data: null };
  }) as typeof api.get);
}

function renderPage(permissions: PermissionTriple[]) {
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.contacts],
    permissions,
  });
  render(
    <MemoryRouter initialEntries={[paths.contactForm(FORM_ID)]}>
      <ToastProvider>
        <Routes>
          <Route path={paths.contactFormPattern} element={<FormDetailPage />} />
        </Routes>
      </ToastProvider>
    </MemoryRouter>,
  );
}

describe('detalle de un formulario de suscripcion', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  it('muestra la vista previa aislada, el codigo para incrustar y las estadisticas', async () => {
    const get = mockApi();
    renderPage([triple(PERMISSIONS.subscriptionForms.read)]);

    expect(await screen.findByRole('heading', { name: FORM.name })).toBeInTheDocument();

    const frame = screen.getByTitle(t('contacts.forms.previewTitle', { name: FORM.name }));
    expect(frame).toHaveAttribute('src', FORM.embed.iframe_url);
    expect(frame).toHaveAttribute('sandbox', 'allow-forms allow-same-origin');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-scripts');

    expect(screen.getByText(scriptSnippet(FORM.embed))).toBeInTheDocument();
    expect(screen.getByText(iframeSnippet(FORM.embed, FORM.texts.title))).toBeInTheDocument();
    expect(screen.getByText(FORM.embed.definition_url)).toBeInTheDocument();
    expect(screen.getByText(FORM.embed.submit_url)).toBeInTheDocument();
    expect(screen.getByText(t('contacts.forms.doubleOptInAlways'))).toBeInTheDocument();

    expect(
      await screen.findByText(tEnum('contacts.forms.counter', 'confirmation_sent')),
    ).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();
    const statsCall = get.mock.calls.find(([p]) => p === endpoints.contacts.forms.stats(FORM_ID));
    expect(statsCall?.[1]?.params).toEqual({ days: 30 });

    expect(screen.queryByRole('button', { name: t('common.edit') })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: t('common.delete') })).not.toBeInTheDocument();
  });

  it('ofrece editar y borrar solo con esos permisos', async () => {
    mockApi();
    renderPage([
      triple(PERMISSIONS.subscriptionForms.read),
      triple(PERMISSIONS.subscriptionForms.update),
      triple(PERMISSIONS.subscriptionForms.delete),
    ]);
    expect(await screen.findByRole('button', { name: t('common.edit') })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: t('common.delete') })).toBeInTheDocument();
  });
});
