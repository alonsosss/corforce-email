import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { directoryMeta, type DirectoryMeta } from '@/api/mailDirectory';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { DavTab } from './DavTab';
import { DIRECTORY_META, MAILBOX } from './mailboxFixtures';

vi.mock('@/api/mailDirectory', () => ({
  directoryMeta: { get: vi.fn() },
  mailDirectoryApi: {},
}));

const meta = vi.mocked(directoryMeta.get);

function mount(props: Partial<Parameters<typeof DavTab>[0]> = {}) {
  return render(
    <ToastProvider>
      <DavTab mailbox={MAILBOX} {...props} />
    </ToastProvider>,
  );
}

function withDav(dav: DirectoryMeta['dav']): DirectoryMeta {
  return { ...DIRECTORY_META, dav };
}

beforeEach(() => {
  meta.mockResolvedValue(DIRECTORY_META);
});

afterEach(() => {
  vi.resetAllMocks();
});

describe('pestana de contactos CardDAV', () => {
  it('muestra la URL que da la API, el usuario del buzon y la indicacion de contrasena de aplicacion', async () => {
    mount();
    expect(await screen.findByText('https://dav.empresa.test/dav/')).toBeInTheDocument();
    expect(screen.getByText(MAILBOX.username)).toBeInTheDocument();
    expect(screen.getByText(t('dav.passwordHint'))).toBeInTheDocument();
    expect(screen.queryByText(t('dav.notConfigured.title'))).not.toBeInTheDocument();
    expect(screen.queryByText(t('dav.disabled.title'))).not.toBeInTheDocument();
  });

  it('sin servidor configurado por el operador lo dice y no muestra datos de conexion', async () => {
    meta.mockResolvedValue(withDav(null));
    mount();
    expect(await screen.findByText(t('dav.notConfigured.title'))).toBeInTheDocument();
    expect(screen.queryByText(t('dav.serverUrl'))).not.toBeInTheDocument();
    expect(screen.queryByText(MAILBOX.username)).not.toBeInTheDocument();
  });

  it('avisa que el acceso DAV del buzon esta desactivado', async () => {
    mount({ mailbox: { ...MAILBOX, dav_access: false } });
    expect(await screen.findByText(t('dav.disabled.title'))).toBeInTheDocument();
    expect(screen.getByText('https://dav.empresa.test/dav/')).toBeInTheDocument();
  });

  it('ofrece ir a las contrasenas de aplicacion solo si el usuario puede verlas', async () => {
    const onOpen = vi.fn();
    const user = userEvent.setup();
    const view = mount({ onOpenAppPasswords: onOpen });
    await user.click(await screen.findByRole('button', { name: t('dav.openAppPasswords') }));
    expect(onOpen).toHaveBeenCalledTimes(1);

    view.unmount();
    mount();
    await screen.findByText(t('dav.passwordHint'));
    expect(
      screen.queryByRole('button', { name: t('dav.openAppPasswords') }),
    ).not.toBeInTheDocument();
  });
});
