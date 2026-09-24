import type { ReactElement } from 'react';
import { render } from '@testing-library/react';
import { MemoryRouter, Outlet, Route, Routes, useLocation } from 'react-router-dom';
import { vi } from 'vitest';
import type { WebmailFolder, WebmailMeta } from '@/api/webmail';
import { ToastProvider } from '@/design/components';
import type { QueryState } from '@/hooks/useQuery';
import type { WebmailOutlet } from './webmailContext';

/** Utilidades de prueba de las pantallas del webmail: el marco comparte carpetas por el Outlet. */

export const INBOX: WebmailFolder = {
  name: 'INBOX',
  delimiter: '/',
  role: 'inbox',
  selectable: true,
  total: 3,
  unread: 2,
};
export const TRASH: WebmailFolder = { ...INBOX, name: 'Trash', role: 'trash', unread: 0 };
export const JUNK: WebmailFolder = { ...INBOX, name: 'Junk', role: 'junk', unread: 0 };
export const ARCHIVE: WebmailFolder = { ...INBOX, name: 'Archive', role: 'archive', unread: 0 };
export const DRAFTS: WebmailFolder = { ...INBOX, name: 'Drafts', role: 'drafts', unread: 0 };
export const CLIENTES: WebmailFolder = { ...INBOX, name: 'Clientes', role: '', unread: 0 };

export const META: WebmailMeta = {
  limits: {
    max_recipients: 50,
    max_message_bytes: 10_000_000,
    max_attachments: 10,
    max_download_bytes: 10_000_000,
    max_body_part_bytes: 1_000_000,
    max_subject_chars: 200,
    max_search_bytes: 64,
    max_folder_name_bytes: 30,
    max_batch_uids: 2,
    max_scheduled_days: 30,
  },
  pagination: { default_page_size: 50, max_page_size: 100 },
  folder_roles: ['inbox', 'sent', 'drafts', 'trash', 'junk', 'archive', 'scheduled'],
  mutable_flags: ['\\Seen', '\\Flagged', '\\Answered'],
  session: { idle_timeout_seconds: 1800, max_lifetime_seconds: 43200 },
};

export function outletFor(folders: WebmailFolder[]): WebmailOutlet {
  const query: QueryState<WebmailFolder[]> = {
    data: folders,
    error: null,
    loading: false,
    reload: vi.fn(),
    setData: vi.fn(),
  };
  return { folders: query, adjustUnread: vi.fn(), reloadFolders: vi.fn(), inboxTick: 0 };
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

/** Monta una pantalla del webmail bajo el Outlet del marco, en la ruta pedida. */
export interface ScreenOptions {
  path?: string;
  url?: string;
  outlet?: WebmailOutlet;
}

export function renderScreen(element: ReactElement, options: ScreenOptions = {}) {
  const path = options.path ?? '/webmail';
  const url = options.url ?? path;
  const outlet = options.outlet ?? outletFor([INBOX]);
  render(
    <ToastProvider>
      <MemoryRouter initialEntries={[url]}>
        <Routes>
          <Route element={<Outlet context={outlet} />}>
            <Route path={path} element={element} />
            <Route path="*" element={<p>otra pantalla</p>} />
          </Route>
        </Routes>
        <LocationProbe />
      </MemoryRouter>
    </ToastProvider>,
  );
  return outlet;
}
