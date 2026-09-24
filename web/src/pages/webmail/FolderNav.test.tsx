import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ApiError } from '@/api/errors';
import { webmailApi, type WebmailFolder } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { FolderNav } from './FolderNav';
import { CLIENTES, INBOX, META, TRASH } from './testing';

const SUB: WebmailFolder = { ...CLIENTES, name: 'Clientes/2026' };
const SCHEDULED: WebmailFolder = { ...INBOX, name: 'Scheduled', role: 'scheduled', unread: 0 };

function renderNav(folders: WebmailFolder[] = [INBOX, TRASH, CLIENTES, SCHEDULED]) {
  const onChanged = vi.fn();
  render(
    <ToastProvider>
      <MemoryRouter>
        <FolderNav folders={folders} current="INBOX" onChanged={onChanged} />
      </MemoryRouter>
    </ToastProvider>,
  );
  return onChanged;
}

describe('carpetas propias en el panel', () => {
  beforeEach(() => {
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'meta').mockResolvedValue(META);
  });
  afterEach(() => vi.restoreAllMocks());

  it('INBOX y las carpetas con papel no se gestionan; Programados lleva a su pagina', () => {
    renderNav();
    expect(
      screen.queryByRole('button', {
        name: t('webmail.folderAdmin.manage', { name: 'Bandeja de entrada' }),
      }),
    ).toBeNull();
    expect(
      screen.getByRole('button', { name: t('webmail.folderAdmin.manage', { name: 'Clientes' }) }),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: t('webmail.role.scheduled') })).toHaveAttribute(
      'href',
      '/webmail/scheduled',
    );
  });

  it('crea una subcarpeta con la ruta completa del servidor', async () => {
    const user = userEvent.setup();
    const create = vi.spyOn(webmailApi, 'createFolder').mockResolvedValue(SUB);
    const onChanged = renderNav();

    await user.click(screen.getByRole('button', { name: t('webmail.folderAdmin.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(t('webmail.folderAdmin.name')), '2026');
    await user.selectOptions(
      within(dialog).getByLabelText(t('webmail.folderAdmin.parent')),
      'Clientes',
    );
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));

    await waitFor(() => expect(create).toHaveBeenCalledWith('Clientes/2026'));
    expect(onChanged).toHaveBeenCalled();
  });

  it('valida el nombre y muestra el conflicto del servicio', async () => {
    const user = userEvent.setup();
    const create = vi
      .spyOn(webmailApi, 'createFolder')
      .mockRejectedValue(new ApiError(409, { code: 'FOLDER_EXISTS', message: '' }));
    renderNav();

    await user.click(screen.getByRole('button', { name: t('webmail.folderAdmin.new') }));
    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByLabelText(t('webmail.folderAdmin.name')), 'a/b');
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));
    expect(
      within(dialog).getByText(t('webmail.folderAdmin.nameDelimiter', { delimiter: '/' })),
    ).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();

    // Una carpeta que ya esta en la lista no llega a pedirse.
    await user.clear(within(dialog).getByLabelText(t('webmail.folderAdmin.name')));
    await user.type(within(dialog).getByLabelText(t('webmail.folderAdmin.name')), 'clientes');
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));
    expect(within(dialog).getByText(t('webmail.folderAdmin.nameTaken'))).toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();

    // Una que aparecio desde otro cliente la rechaza el servicio.
    await user.clear(within(dialog).getByLabelText(t('webmail.folderAdmin.name')));
    await user.type(within(dialog).getByLabelText(t('webmail.folderAdmin.name')), 'Proveedores');
    await user.click(within(dialog).getByRole('button', { name: t('common.create') }));
    expect(await within(dialog).findByText(t('error.code.FOLDER_EXISTS'))).toBeInTheDocument();
  });

  it('renombra conservando la carpeta padre y borra tras confirmar', async () => {
    const user = userEvent.setup();
    const rename = vi
      .spyOn(webmailApi, 'renameFolder')
      .mockResolvedValue({ ...SUB, name: 'Clientes/Antiguos' });
    const remove = vi.spyOn(webmailApi, 'deleteFolder').mockResolvedValue(null);
    renderNav([INBOX, CLIENTES, SUB]);

    await user.click(
      screen.getByRole('button', { name: t('webmail.folderAdmin.manage', { name: '2026' }) }),
    );
    let dialog = screen.getByRole('dialog');
    const name = within(dialog).getByLabelText(t('webmail.folderAdmin.name'));
    await user.clear(name);
    await user.type(name, 'Antiguos');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.folderAdmin.rename') }));
    await waitFor(() => expect(rename).toHaveBeenCalledWith('Clientes/2026', 'Clientes/Antiguos'));

    await user.click(
      screen.getByRole('button', { name: t('webmail.folderAdmin.manage', { name: '2026' }) }),
    );
    dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: t('webmail.folderAdmin.delete') }));
    dialog = screen.getByRole('dialog');
    expect(remove).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: t('webmail.folderAdmin.delete') }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith('Clientes/2026'));
  });

  it('una carpeta con subcarpetas no ofrece borrarse', async () => {
    const user = userEvent.setup();
    renderNav([INBOX, CLIENTES, SUB]);
    await user.click(
      screen.getByRole('button', { name: t('webmail.folderAdmin.manage', { name: 'Clientes' }) }),
    );
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByRole('button', { name: t('webmail.folderAdmin.delete') }),
    ).toBeDisabled();
    expect(within(dialog).getByText(t('webmail.folderAdmin.hasChildren'))).toBeInTheDocument();
  });
});
