import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { webmailApi } from '@/api/webmail';
import { resetWebmailCatalogs } from '@/webmail/catalogs';
import { useWebmailStore } from '@/webmail/store';
import WebmailApp from './WebmailApp';

const OPEN_MESSAGE = '/webmail?folder=INBOX&uid=2';

describe('acceso al webmail sin sesion', () => {
  let seen: { pathname: string; state: unknown } | null = null;

  const Probe = () => {
    const location = useLocation();
    seen = { pathname: location.pathname, state: location.state };
    return null;
  };

  const open = () =>
    render(
      <MemoryRouter initialEntries={[OPEN_MESSAGE]}>
        <Probe />
        <Routes>
          <Route path="/webmail/*" element={<WebmailApp />} />
        </Routes>
      </MemoryRouter>,
    );

  beforeEach(() => {
    seen = null;
    resetWebmailCatalogs();
    vi.spyOn(webmailApi, 'session').mockRejectedValue(
      new ApiError(401, { code: ERROR_CODES.SESSION_EXPIRED, message: 'sin sesion' }),
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('sin sesion recuerda la pantalla pedida para volver a ella tras entrar', async () => {
    useWebmailStore.setState({ status: 'checking', session: null, closedByUser: false });
    open();
    await waitFor(() => expect(seen?.pathname).toBe('/webmail/login'));
    expect(seen?.state).toEqual({ from: OPEN_MESSAGE });
  });

  it('tras cerrar la sesion no arrastra el mensaje abierto al buzon que entre despues', async () => {
    useWebmailStore.setState({ status: 'checking', session: null, closedByUser: true });
    open();
    await waitFor(() => expect(seen?.pathname).toBe('/webmail/login'));
    expect(seen?.state).toBeNull();
  });
});
