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
  // Busqueda avanzada: fechas AAAA-MM-DD y marcas como "1".
  from?: string;
  to?: string;
  subject?: string;
  since?: string;
  before?: string;
  unread?: string;
  flagged?: string;
  attachments?: string;
}

export const paths = {
  // Webmail: otra sesion (la del buzon), fuera del layout de la plataforma. La carpeta, el
  // mensaje, la pagina y la busqueda viajan en la query: un nombre de carpeta puede llevar
  // el separador "/".
  webmail: '/webmail',
  webmailLogin: '/webmail/login',
  webmailCompose: '/webmail/compose',
  webmailSettings: '/webmail/settings',
  webmailView: (view: WebmailView) => withQuery('/webmail', { ...view }),
  webmailComposeFrom: (mode: string, folder: string, uid: number) =>
    withQuery('/webmail/compose', { mode, folder, uid }),
  webmailComposeTo: (to: string) => withQuery('/webmail/compose', { to }),
  webmailSettingsTab: (tab: string) => withQuery('/webmail/settings', { tab }),
  webmailScheduled: '/webmail/scheduled',
  webmailContacts: '/webmail/contacts',
  webmailCalendar: '/webmail/calendar',
  /** Pagina publica de citas de un buzon: fuera de las dos sesiones (plataforma y webmail). */
  publicBookingPattern: '/citas/:cell/:tenant/:page',
  publicBooking: (cell: string, tenant: string, page: string) =>
    `/citas/${encodeURIComponent(cell)}/${encodeURIComponent(tenant)}/${encodeURIComponent(page)}`,

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
  apiKeys: '/access/api-keys',
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
  templateEditor: (id: string) => `/sending/templates/${encodeURIComponent(id)}/editor`,
  templateEditorPattern: '/sending/templates/:id/editor',
  /** Editor de una plantilla por crear (el alta desde la galeria o en blanco). */
  templateNewEditor: '/sending/templates/new/editor',
  brandKit: '/sending/brand-kit',
  landingPages: '/sending/pages',
  landingPage: (id: string) => `/sending/pages/${encodeURIComponent(id)}`,
  landingPageEditor: (id: string) => `/sending/pages/${encodeURIComponent(id)}/editor`,
  landingPageEditorPattern: '/sending/pages/:id/editor',
  suppression: '/sending/suppression',
  reputation: '/sending/reputation',
  contacts: '/marketing/contacts',
  contact: (id: string) => `/marketing/contacts/${encodeURIComponent(id)}`,
  contactList: (id: string) => `/marketing/lists/${encodeURIComponent(id)}`,
  contactListPattern: '/marketing/lists/:id',
  contactForm: (id: string) => `/marketing/forms/${encodeURIComponent(id)}`,
  contactFormPattern: '/marketing/forms/:id',
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
  analyticsCampaignLinks: (id: string) =>
    `/marketing/analytics/campaigns/${encodeURIComponent(id)}/links`,
  analyticsCampaignLinksPattern: '/marketing/analytics/campaigns/:id/links',
  billing: '/billing',
  scheduler: '/scheduler',
  schedulerJob: (id: string) => `/scheduler/${encodeURIComponent(id)}`,
  platformBilling: '/platform/billing',
  platformReputation: '/platform/reputation',
  platformMailQueue: '/platform/mail-queue',
  platformLogs: '/platform/logs',
  platformRspamd: '/platform/rspamd',
} as const;
