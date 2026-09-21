import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { directoryMeta, mailDirectoryApi } from '@/api/mailDirectory';
import { MODULES } from '@/access/modules';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { MailboxDataTab } from './MailboxDataTab';
import { DIRECTORY_META, MAILBOX } from './mailboxFixtures';

vi.mock('@/api/mailDirectory', () => ({
  directoryMeta: { get: vi.fn() },
  mailDirectoryApi: { updateMailbox: vi.fn() },
}));

const updateMailbox = vi.mocked(mailDirectoryApi.updateMailbox);

function grantUpdate() {
  useAccessStore.setState({
    loaded: true,
    isAdmin: false,
    policyLoaded: true,
    modules: [MODULES.mailboxes],
    permissions: [{ module: MODULES.mailboxes, resource: 'mailboxes', action: 'update' }],
  });
}

const checkbox = () => screen.findByRole('checkbox', { name: t('mail.protocol.dav_access') });

beforeEach(() => {
  vi.mocked(directoryMeta.get).mockResolvedValue(DIRECTORY_META);
  grantUpdate();
});

afterEach(() => {
  vi.resetAllMocks();
  useAccessStore.getState().reset();
});

describe('ficha del buzon: acceso DAV', () => {
  it('refleja el valor del buzon y envia solo dav_access cuando se cambia', async () => {
    const user = userEvent.setup();
    updateMailbox.mockResolvedValue({ data: { ...MAILBOX, dav_access: false } } as never);
    render(
      <ToastProvider>
        <MailboxDataTab mailbox={MAILBOX} onChange={vi.fn()} />
      </ToastProvider>,
    );
    const dav = await checkbox();
    expect(dav).toBeChecked();

    await user.click(dav);
    await user.click(screen.getByRole('button', { name: t('common.save') }));

    expect(updateMailbox).toHaveBeenCalledTimes(1);
    expect(updateMailbox).toHaveBeenCalledWith(
      MAILBOX.id,
      expect.objectContaining({ dav_access: false }),
    );
    const body = updateMailbox.mock.calls[0]?.[1] as Record<string, unknown>;
    expect(Object.keys(body).filter((key) => body[key] !== undefined)).toEqual(['dav_access']);
  });

  it('no permite editarlo sin el permiso de actualizar', async () => {
    useAccessStore.getState().reset();
    render(
      <ToastProvider>
        <MailboxDataTab mailbox={MAILBOX} onChange={vi.fn()} />
      </ToastProvider>,
    );
    expect(await checkbox()).toBeDisabled();
  });
});
