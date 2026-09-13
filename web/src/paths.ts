// Rutas de la aplicacion. Sin imports: lo consumen el enrutador, el menu y las pantallas
// sin crear ciclos entre modulos.

function withQuery(path: string, query: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === '') continue;
    params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

export interface WebmailView {
  folder?: string;
  uid?: number;
  page?: number;
  q?: string;
}

export const paths = {
  // Webmail: otra sesion (la del buzon), fuera del layout de la plataforma. La carpeta, el
  // mensaje, la pagina y la busqueda viajan en la query: un nombre de carpeta puede llevar
  // el separador "/".
  webmail: '/webmail',
  webmailLogin: '/webmail/login',
  webmailCompose: '/webmail/compose',
  webmailView: (view: WebmailView) => withQuery('/webmail', { ...view }),
  webmailComposeFrom: (mode: string, folder: string, uid: number) =>
    withQuery('/webmail/compose', { mode, folder, uid }),

  home: '/',
  login: '/login',
  forgotPassword: '/forgot-password',
  resetPassword: '/reset-password',
  sessionExpired: '/session-expired',
  account: '/account',
  users: '/users',
  user: (id: string) => `/users/${encodeURIComponent(id)}`,
  roles: '/roles',
  role: (id: string) => `/roles/${encodeURIComponent(id)}`,
  denials: '/access/denials',
  sessions: '/sessions',
  organizations: '/organizations',
  organization: (id: string) => `/organizations/${encodeURIComponent(id)}`,
  migrations: '/organizations/migrations',
  cells: '/cells',
  auditLogs: '/audit/logs',
  securityEvents: '/audit/security-events',
  integrity: '/audit/integrity',
  domains: '/mail/domains',
  domain: (id: string) => `/mail/domains/${encodeURIComponent(id)}`,
  mailboxes: '/mail/mailboxes',
  mailbox: (id: string) => `/mail/mailboxes/${encodeURIComponent(id)}`,
  mailRouting: '/mail/routing',
  mailSecurity: '/mail/security',
  quarantine: '/mail/quarantine',
  templates: '/sending/templates',
  template: (id: string) => `/sending/templates/${encodeURIComponent(id)}`,
  suppression: '/sending/suppression',
  reputation: '/sending/reputation',
  contacts: '/marketing/contacts',
  contact: (id: string) => `/marketing/contacts/${encodeURIComponent(id)}`,
  contactList: (id: string) => `/marketing/lists/${encodeURIComponent(id)}`,
  contactListPattern: '/marketing/lists/:id',
  segments: '/marketing/segments',
  segmentNew: '/marketing/segments/new',
  segment: (id: string) => `/marketing/segments/${encodeURIComponent(id)}`,
  campaigns: '/marketing/campaigns',
  campaign: (id: string) => `/marketing/campaigns/${encodeURIComponent(id)}`,
  automations: '/marketing/automations',
  automationNew: '/marketing/automations/new',
  automation: (id: string) => `/marketing/automations/${encodeURIComponent(id)}`,
  automationRun: (id: string) => `/marketing/automations/runs/${encodeURIComponent(id)}`,
  automationRunPattern: '/marketing/automations/runs/:id',
  analytics: '/marketing/analytics',
  billing: '/billing',
  scheduler: '/scheduler',
  schedulerJob: (id: string) => `/scheduler/${encodeURIComponent(id)}`,
  platformBilling: '/platform/billing',
  platformReputation: '/platform/reputation',
} as const;
