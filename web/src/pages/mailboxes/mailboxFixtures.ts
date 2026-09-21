import type { DirectoryMeta, Mailbox } from '@/api/mailDirectory';

// Datos de prueba de las pruebas de la ficha del buzon.
export const MAILBOX: Mailbox = {
  id: 'mb-1',
  tenant_id: 'tn-1',
  username: 'ana@empresa.test',
  local_part: 'ana',
  domain: 'empresa.test',
  display_name: 'Ana',
  quota_bytes: 1073741824,
  active: 1,
  kind: '',
  imap_access: true,
  pop3_access: true,
  smtp_access: true,
  sieve_access: true,
  dav_access: true,
  tls_enforce_in: false,
  tls_enforce_out: false,
  relayhost_id: null,
  force_pw_update: false,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

export const DIRECTORY_META: DirectoryMeta = {
  mailbox: {
    password_min_length: 12,
    password_max_length: 128,
    local_part_max_length: 64,
    display_name_max_length: 100,
    kinds: [],
    active_states: [
      { value: 0, code: 'inactive' },
      { value: 1, code: 'active' },
      { value: 2, code: 'receive_only' },
    ],
  },
  alias: { active_states: [] },
  domain: { name_max_length: 253, label_max_length: 63, description_max_length: 255 },
  sieve: { script_max_bytes: 65536, filter_types: ['prefilter', 'postfilter'] },
  tls_policies: [],
  bcc_map_types: [],
  limits: { quota_unit: 'bytes', unlimited: 0 },
  pagination: { default_page_size: 25, max_page_size: 100 },
  search: { max_length: 100 },
  dav: { server_url: 'https://dav.empresa.test/dav/' },
};
