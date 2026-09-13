import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import {
  contactsApi,
  contactsMeta,
  type ContactStatusDetail,
  type ContactsMeta,
} from '@/api/contacts';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { hasMessage, t, tEnum } from '@/i18n';
import { ContactsTab } from './ContactsTab';
import {
  ContactStatusBadge,
  ContactStatusSummary,
  contactStatusTone,
  statusLiftHint,
} from './contactStatus';

// status_details con la forma de GET /contacts/meta (services/contacts/internal/adapters/http/meta.go).
const DETAILS: ContactStatusDetail[] = [
  { status: 'active', severity: 0 },
  { status: 'unsubscribed', severity: 3, lifted_by: 'reconsent' },
  { status: 'bounced', severity: 4, lifted_by: 'operator' },
  { status: 'complained', severity: 5, lifted_by: 'operator' },
  { status: 'invalid', severity: 2, lifted_by: 'operator' },
  { status: 'excluded', severity: 1, lifted_by: 'operator_or_expiry' },
];

const META: ContactsMeta = {
  statuses: DETAILS.map((d) => d.status),
  status_details: DETAILS,
  consent_statuses: ['granted', 'revoked', 'pending', 'none'],
  consent_methods: ['form', 'import', 'api', 'double_opt_in', 'unsubscribe_link', 'suppression'],
  api_consent_statuses: ['granted', 'revoked'],
  api_consent_methods: ['api', 'form'],
  sources: ['api', 'import', 'form', 'integration'],
  attribute_types: ['string', 'number', 'boolean', 'date'],
  import: {
    max_rows: 1000,
    max_errors: 100,
    consent_statuses: ['granted', 'none'],
    max_consent_basis_length: 500,
  },
  limits: {
    max_email_length: 320,
    max_name_length: 200,
    max_tags: 100,
    max_tag_length: 64,
    max_attribute_definitions: 100,
    max_attribute_string_length: 1000,
    max_consent_source_length: 500,
    max_search_length: 200,
  },
  pagination: { default_page_size: 20, max_page_size: 100 },
};

function grant(...triples: (readonly [string, string, string])[]) {
  const permissions: PermissionTriple[] = triples.map(([module, resource, action]) => ({
    module,
    resource,
    action,
  }));
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.contacts],
    permissions,
  });
}

describe('estado del contacto desde el catalogo', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  it('el tono sale de lo que levanta el estado, no de una lista de estados', () => {
    expect(contactStatusTone('active', DETAILS)).toBe('success');
    expect(contactStatusTone('unsubscribed', DETAILS)).toBe('warning');
    expect(contactStatusTone('bounced', DETAILS)).toBe('danger');
    expect(contactStatusTone('invalid', DETAILS)).toBe('danger');
    expect(contactStatusTone('excluded', DETAILS)).toBe('info');
    // Un estado que la interfaz no conoce se pinta por lo que dice el catalogo.
    expect(contactStatusTone('futuro', [{ status: 'futuro', severity: 6, lifted_by: 'operator' }])).toBe(
      'danger',
    );
    expect(contactStatusTone('futuro', DETAILS)).toBe('neutral');
    expect(contactStatusTone('excluded', undefined)).toBe('neutral');
  });

  it('cada estado del catalogo tiene su etiqueta y cada forma de levantarlo su explicacion', () => {
    for (const detail of DETAILS) {
      expect(hasMessage(`contacts.status.${detail.status}`)).toBe(true);
      if (detail.lifted_by) {
        expect(statusLiftHint(detail.status, DETAILS)).toBe(
          t(`contacts.statusLift.${detail.lifted_by}` as Parameters<typeof t>[0]),
        );
      } else {
        expect(statusLiftHint(detail.status, DETAILS)).toBeNull();
      }
    }
    expect(statusLiftHint('futuro', [{ status: 'futuro', severity: 6, lifted_by: 'otra' }])).toBeNull();
  });

  it('la insignia y la ficha leen el catalogo compartido', async () => {
    vi.spyOn(contactsMeta, 'get').mockResolvedValue(META);
    render(
      <>
        <ContactStatusBadge status="excluded" />
        <ContactStatusSummary status="invalid" />
      </>,
    );
    await waitFor(() =>
      expect(screen.getByText(tEnum('contacts.status', 'excluded'))).toHaveClass('cf-badge--info'),
    );
    expect(screen.getByText(tEnum('contacts.status', 'invalid'))).toHaveClass('cf-badge--danger');
    expect(screen.getByText(t('contacts.statusLift.operator'))).toBeInTheDocument();
  });

  it('el filtro de estado del listado ofrece los estados del catalogo', async () => {
    grant(PERMISSIONS.contacts.read);
    vi.spyOn(contactsMeta, 'get').mockResolvedValue(META);
    vi.spyOn(contactsApi, 'list').mockResolvedValue({
      items: [],
      page: 1,
      perPage: 20,
      total: 0,
      totalPages: 0,
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ContactsTab />
        </ToastProvider>
      </MemoryRouter>,
    );
    const select = await screen.findByLabelText(t('common.status'));
    await waitFor(() => expect(select).toBeEnabled());
    for (const status of META.statuses) {
      expect(
        within(select).getByRole('option', { name: tEnum('contacts.status', status) }),
      ).toBeInTheDocument();
    }
  });
});
