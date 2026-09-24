import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ApiError } from '@/api/errors';
import { webmailApi, type Contact } from '@/api/webmail';
import { saveBlob } from '@/lib/download';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { renderScreen } from '../testing';
import ContactsPage from './ContactsPage';

vi.mock('@/lib/download', () => ({ saveBlob: vi.fn() }));

const LUIS: Contact = {
  id: 'c1',
  etag: '"1"',
  updated_at: '2026-09-20T10:00:00Z',
  name: 'Luis Rojas',
  given_name: 'Luis',
  family_name: 'Rojas',
  emails: [{ value: 'luis@cliente.com', type: 'work' }],
  phones: [{ value: '+51 999 111 222', type: 'mobile' }],
  organization: 'Acme',
  title: 'Compras',
  notes: '',
  birthday: '--05-12',
};

const PAGE = { items: [LUIS], page: 1, perPage: 50, total: 1, totalPages: 1 };

function renderContacts(url = '/webmail/contacts') {
  renderScreen(<ContactsPage />, { path: '/webmail/contacts', url });
}

describe('contactos personales', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'davMeta').mockResolvedValue({
      limits: { max_import_bytes: 20 },
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it('lista, abre la ficha y escribe a un contacto', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue(PAGE);
    vi.spyOn(webmailApi, 'contact').mockResolvedValue(LUIS);
    renderContacts();

    await user.click(await screen.findByRole('link', { name: /Luis Rojas/ }));
    expect(await screen.findByRole('heading', { name: 'Luis Rojas' })).toBeInTheDocument();
    expect(screen.getByText('Compras - Acme')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('webmail.contacts.write') }));
    expect(screen.getByTestId('location').textContent).toBe(
      '/webmail/compose?to=luis%40cliente.com',
    );
  });

  it('la busqueda se aplica al dejar de escribir y viaja en la URL', async () => {
    const user = userEvent.setup();
    const list = vi.spyOn(webmailApi, 'contacts').mockResolvedValue(PAGE);
    renderContacts();

    await user.type(await screen.findByLabelText(t('webmail.contacts.search')), 'acme');
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith({ q: 'acme', page: 1 }, expect.anything()),
    );
    expect(screen.getByTestId('location').textContent).toContain('q=acme');
  });

  it('crea un contacto y valida antes de enviar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue({ ...PAGE, items: [], total: 0 });
    vi.spyOn(webmailApi, 'contact').mockResolvedValue({ ...LUIS, id: 'c2' });
    const create = vi
      .spyOn(webmailApi, 'createContact')
      .mockImplementation(async (input) => ({ ...LUIS, ...input, id: 'c2' }));
    renderContacts();

    await user.click(await screen.findByRole('button', { name: t('webmail.contacts.new') }));
    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    expect(within(dialog).getByText(t('webmail.contacts.nameOrEmail'))).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();

    await user.type(within(dialog).getByLabelText(t('webmail.contacts.name')), 'Eva');
    await user.type(
      within(dialog).getAllByLabelText(t('webmail.contacts.emails'))[0] as HTMLElement,
      'eva@cliente.com',
    );
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Eva',
          emails: [{ value: 'eva@cliente.com', type: 'work' }],
        }),
      ),
    );
    expect(screen.getByTestId('location').textContent).toContain('id=c2');
  });

  it('un 412 al guardar avisa del cambio en otro dispositivo y carga la version actual', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue(PAGE);
    const read = vi
      .spyOn(webmailApi, 'contact')
      .mockResolvedValueOnce(LUIS)
      .mockResolvedValueOnce({ ...LUIS, etag: '"2"', organization: 'Acme Global' });
    const update = vi
      .spyOn(webmailApi, 'updateContact')
      .mockRejectedValue(new ApiError(412, { code: 'PRECONDITION_FAILED', message: '' }));
    renderContacts('/webmail/contacts?id=c1');

    await user.click(await screen.findByRole('button', { name: t('common.edit') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(t('webmail.contacts.jobTitle')), ' jefe');
    await user.click(within(dialog).getByRole('button', { name: t('common.save') }));

    await waitFor(() => expect(update).toHaveBeenCalledWith('c1', expect.anything(), '"1"'));
    expect(await within(dialog).findByText(t('webmail.contacts.conflict'))).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: t('common.save') })).toBeDisabled();

    await user.click(
      within(dialog).getByRole('button', { name: t('webmail.contacts.loadLatest') }),
    );
    await waitFor(() =>
      expect(within(dialog).getByLabelText(t('webmail.contacts.organization'))).toHaveValue(
        'Acme Global',
      ),
    );
    expect(read).toHaveBeenCalledTimes(2);
    expect(within(dialog).getByRole('button', { name: t('common.save') })).not.toBeDisabled();
  });

  it('importa un .vcf y resume lo importado y lo omitido', async () => {
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue(PAGE);
    const upload = vi.spyOn(webmailApi, 'importContacts').mockResolvedValue({
      imported: 3,
      updated: 1,
      skipped: [{ index: 4, reason: 'sin nombre ni correo' }],
    });
    renderContacts();

    const input = await screen.findByLabelText(t('webmail.contacts.importFile'));
    fireEvent.change(input, {
      target: { files: [new File(['BEGIN:VCARD'], 'agenda.vcf', { type: 'text/vcard' })] },
    });
    await waitFor(() => expect(upload).toHaveBeenCalled());
    expect(
      await screen.findByText(
        t('webmail.contacts.importDone', { imported: 3, updated: 1, skipped: 1 }),
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        t('webmail.contacts.importSkipped', { n: 5, reason: 'sin nombre ni correo' }),
      ),
    ).toBeInTheDocument();
  });

  it('no sube un fichero por encima del tope servido; un tope del servidor se explica', async () => {
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue(PAGE);
    const upload = vi
      .spyOn(webmailApi, 'importContacts')
      .mockRejectedValue(
        new ApiError(
          507,
          { code: 'LIMIT_EXCEEDED', message: '' },
          { error: { code: 'LIMIT_EXCEEDED', message: '', details: { limit: 'contacts' } } },
        ),
      );
    renderContacts();

    const input = await screen.findByLabelText(t('webmail.contacts.importFile'));
    await waitFor(() => expect(webmailApi.davMeta).toHaveBeenCalled());
    fireEvent.change(input, { target: { files: [new File(['x'.repeat(40)], 'grande.vcf')] } });
    expect(await screen.findByText(/el maximo es/)).toBeInTheDocument();
    expect(upload).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { files: [new File(['x'], 'uno.vcf')] } });
    expect(await screen.findByText(t('webmail.dav.limit.contacts'))).toBeInTheDocument();
  });

  it('exporta la agenda como fichero y borra un contacto tras confirmar', async () => {
    const user = userEvent.setup();
    vi.spyOn(webmailApi, 'contacts').mockResolvedValue(PAGE);
    vi.spyOn(webmailApi, 'contact').mockResolvedValue(LUIS);
    const blob = new Blob(['BEGIN:VCARD']);
    vi.spyOn(webmailApi, 'exportContacts').mockResolvedValue({
      blob,
      filename: null,
      contentType: 'text/vcard',
    });
    const remove = vi.spyOn(webmailApi, 'deleteContact').mockResolvedValue(null);
    renderContacts('/webmail/contacts?id=c1');

    await user.click(await screen.findByRole('button', { name: t('webmail.contacts.export') }));
    await waitFor(() =>
      expect(saveBlob).toHaveBeenCalledWith(blob, t('webmail.contacts.exportFilename')),
    );

    await user.click(await screen.findByRole('button', { name: t('common.delete') }));
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: t('common.delete') }),
    );
    await waitFor(() => expect(remove).toHaveBeenCalledWith('c1'));
    expect(screen.getByTestId('location').textContent).toBe('/webmail/contacts');
  });
});
