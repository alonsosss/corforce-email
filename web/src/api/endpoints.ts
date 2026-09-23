// Unico lugar donde viven las rutas del API. Cada ruta espeja un handler Go real:
// services/identity, access-control, organization y audit, servidos por el gateway bajo
// /api/v1 en el mismo origen que la aplicacion.

const API_PREFIX = '/api/v1';

const seg = (value: string): string => encodeURIComponent(value);

/**
 * Segmento que lleva una direccion o un dominio (objetos de mail-security). '@' y '+' son
 * validos en una ruta (RFC 3986) y viajan sin escapar: si llegaran escapados, chi enruta
 * sobre RawPath y el servicio Go recibiria el parametro sin decodificar.
 */
const addressSeg = (value: string): string =>
  encodeURIComponent(value).replace(/%40/g, '@').replace(/%2B/gi, '+');

/** Coleccion REST con detalle por id. */
const collectionOf = (path: string) => ({
  collection: `${API_PREFIX}${path}`,
  byId: (id: string) => `${API_PREFIX}${path}/${seg(id)}`,
});

/**
 * Origen del API. Vacio por defecto: la aplicacion y el API comparten origen y las rutas
 * son relativas. VITE_API_URL solo se rellena cuando el API vive en otro host.
 */
export function apiBase(): string {
  const configured = (import.meta.env.VITE_API_URL ?? '').trim();
  return configured.replace(/\/+$/, '');
}

export const endpoints = {
  auth: {
    login: `${API_PREFIX}/auth/login`,
    refresh: `${API_PREFIX}/auth/refresh`,
    forgotPassword: `${API_PREFIX}/auth/forgot-password`,
    resetPassword: `${API_PREFIX}/auth/reset-password`,
    resetPasswordPolicy: `${API_PREFIX}/auth/reset-password/policy`,
    stepUp: `${API_PREFIX}/auth/step-up`,
    mfaChallenge: `${API_PREFIX}/auth/mfa/challenge`,
    mfaSetup: `${API_PREFIX}/auth/mfa/setup`,
    mfaActivate: `${API_PREFIX}/auth/mfa/activate`,
    mfaDisable: `${API_PREFIX}/auth/mfa/disable`,
  },
  users: {
    collection: `${API_PREFIX}/users`,
    byId: (id: string) => `${API_PREFIX}/users/${seg(id)}`,
    deactivate: (id: string) => `${API_PREFIX}/users/${seg(id)}/deactivate`,
    resetPassword: (id: string) => `${API_PREFIX}/users/${seg(id)}/reset-password`,
    changePassword: `${API_PREFIX}/users/change-password`,
    passwordPolicy: `${API_PREFIX}/users/password-policy`,
  },
  sessions: {
    logout: `${API_PREFIX}/sessions/logout`,
    logoutAll: `${API_PREFIX}/sessions/logout-all`,
    mine: `${API_PREFIX}/sessions/mine`,
    platform: `${API_PREFIX}/sessions/platform`,
    collection: `${API_PREFIX}/sessions`,
    policy: `${API_PREFIX}/sessions/policy`,
    byId: (id: string) => `${API_PREFIX}/sessions/${seg(id)}`,
  },
  roles: {
    collection: `${API_PREFIX}/roles`,
    byId: (id: string) => `${API_PREFIX}/roles/${seg(id)}`,
    permissions: (id: string) => `${API_PREFIX}/roles/${seg(id)}/permissions`,
  },
  permissions: {
    collection: `${API_PREFIX}/permissions`,
  },
  userRoles: {
    assign: `${API_PREFIX}/user-roles/assign`,
    revoke: `${API_PREFIX}/user-roles/revoke`,
    ofUser: (userId: string) => `${API_PREFIX}/user-roles/user/${seg(userId)}`,
  },
  access: {
    myModules: `${API_PREFIX}/access/my-modules`,
    denials: `${API_PREFIX}/access/denials`,
    policy: (userId: string) => `${API_PREFIX}/policy/${seg(userId)}`,
  },
  organizations: {
    collection: `${API_PREFIX}/organizations`,
    byId: (id: string) => `${API_PREFIX}/organizations/${seg(id)}`,
    modules: (id: string) => `${API_PREFIX}/organizations/${seg(id)}/modules`,
    migrate: (id: string) => `${API_PREFIX}/organizations/${seg(id)}/migrate`,
    reseedRoles: (id: string) => `${API_PREFIX}/organizations/${seg(id)}/reseed-roles`,
    migrateAll: `${API_PREFIX}/organizations/migrate`,
    migrationsStatus: `${API_PREFIX}/organizations/migrations/status`,
  },
  cells: {
    collection: `${API_PREFIX}/cells`,
    byId: (id: string) => `${API_PREFIX}/cells/${seg(id)}`,
  },
  audit: {
    logs: `${API_PREFIX}/audit/logs`,
    securityEvents: `${API_PREFIX}/audit/security-events`,
    acknowledge: (id: string) => `${API_PREFIX}/audit/security-events/${seg(id)}/acknowledge`,
    integrityRuns: `${API_PREFIX}/audit/integrity/runs`,
    integrityRun: (id: string) => `${API_PREFIX}/audit/integrity/runs/${seg(id)}`,
    cancelIntegrityRun: (id: string) => `${API_PREFIX}/audit/integrity/runs/${seg(id)}/cancel`,
  },
  domains: {
    collection: `${API_PREFIX}/domains`,
    byId: (id: string) => `${API_PREFIX}/domains/${seg(id)}`,
    verify: (id: string) => `${API_PREFIX}/domains/${seg(id)}/verify`,
    rotateDkim: (id: string) => `${API_PREFIX}/domains/${seg(id)}/rotate-dkim`,
    revokeDkim: (id: string) => `${API_PREFIX}/domains/${seg(id)}/revoke-dkim`,
    dnsMode: (id: string) => `${API_PREFIX}/domains/${seg(id)}/dns-mode`,
    publishDns: (id: string) => `${API_PREFIX}/domains/${seg(id)}/publish-dns`,
    dnsProvider: (provider: string) => `${API_PREFIX}/domains/dns-providers/${seg(provider)}`,
    dnsProviderConnect: (provider: string) =>
      `${API_PREFIX}/domains/dns-providers/${seg(provider)}/connect`,
    dnsProviderDisconnect: (provider: string) =>
      `${API_PREFIX}/domains/dns-providers/${seg(provider)}/disconnect`,
  },
  mailDirectory: {
    meta: `${API_PREFIX}/mail-directory/meta`,
  },
  mailDomains: {
    ...collectionOf('/mail-domains'),
    aliasDomains: collectionOf('/mail-domains/alias-domains'),
    mtaSts: (domain: string) => `${API_PREFIX}/mail-domains/mta-sts/${seg(domain)}`,
  },
  mailboxes: {
    ...collectionOf('/mailboxes'),
    password: (id: string) => `${API_PREFIX}/mailboxes/${seg(id)}/password`,
    quota: (id: string) => `${API_PREFIX}/mailboxes/${seg(id)}/quota`,
    logins: (id: string) => `${API_PREFIX}/mailboxes/${seg(id)}/logins`,
    sieve: (id: string) => `${API_PREFIX}/mailboxes/${seg(id)}/sieve`,
    vacation: (id: string) => `${API_PREFIX}/mailboxes/${seg(id)}/vacation`,
    appPasswords: (id: string) => `${API_PREFIX}/mailboxes/${seg(id)}/app-passwords`,
    appPassword: (id: string, appPasswordId: string) =>
      `${API_PREFIX}/mailboxes/${seg(id)}/app-passwords/${seg(appPasswordId)}`,
  },
  mailMigration: {
    meta: `${API_PREFIX}/mail-migration/meta`,
    jobs: `${API_PREFIX}/mail-migration/jobs`,
    job: (id: string) => `${API_PREFIX}/mail-migration/jobs/${seg(id)}`,
    cancel: (id: string) => `${API_PREFIX}/mail-migration/jobs/${seg(id)}/cancel`,
  },
  mailRouting: {
    aliases: collectionOf('/mail-routing/aliases'),
    spamAliases: collectionOf('/mail-routing/spam-aliases'),
    senderAcl: collectionOf('/mail-routing/sender-acl'),
    relayhosts: collectionOf('/mail-routing/relayhosts'),
    transports: collectionOf('/mail-routing/transports'),
    tlsPolicies: collectionOf('/mail-routing/tls-policies'),
    recipientMaps: collectionOf('/mail-routing/recipient-maps'),
    bccMaps: collectionOf('/mail-routing/bcc-maps'),
  },
  mailSecurity: {
    spamScores: `${API_PREFIX}/mail-security/spam-scores`,
    spamScore: (object: string) => `${API_PREFIX}/mail-security/spam-scores/${addressSeg(object)}`,
    addressLists: `${API_PREFIX}/mail-security/address-lists`,
    addressList: (id: string) => `${API_PREFIX}/mail-security/address-lists/${seg(id)}`,
    footers: `${API_PREFIX}/mail-security/footers`,
    footer: (domain: string) => `${API_PREFIX}/mail-security/footers/${addressSeg(domain)}`,
    forwardingHosts: `${API_PREFIX}/mail-security/forwarding-hosts`,
    forwardingHost: (id: string) => `${API_PREFIX}/mail-security/forwarding-hosts/${seg(id)}`,
    rateLimits: `${API_PREFIX}/mail-security/rate-limits`,
    rateLimit: (object: string) => `${API_PREFIX}/mail-security/rate-limits/${addressSeg(object)}`,
    mailboxTags: `${API_PREFIX}/mail-security/mailbox-tags`,
    mailboxTag: (username: string) =>
      `${API_PREFIX}/mail-security/mailbox-tags/${addressSeg(username)}`,
    quarantine: `${API_PREFIX}/mail-security/quarantine`,
    quarantineItem: (id: string) => `${API_PREFIX}/mail-security/quarantine/${seg(id)}`,
    quarantineMessage: (id: string) => `${API_PREFIX}/mail-security/quarantine/${seg(id)}/message`,
    quarantineRelease: (id: string) => `${API_PREFIX}/mail-security/quarantine/${seg(id)}/release`,
    quarantineReleaseHam: (id: string) =>
      `${API_PREFIX}/mail-security/quarantine/${seg(id)}/release-ham`,
    quarantineLearnSpam: (id: string) =>
      `${API_PREFIX}/mail-security/quarantine/${seg(id)}/learn-spam`,
    quarantineSettings: `${API_PREFIX}/mail-security/quarantine-settings`,
    queue: `${API_PREFIX}/mail-security/queue`,
    queueMessage: (id: string) => `${API_PREFIX}/mail-security/queue/${seg(id)}`,
    queueAction: (id: string, action: string) =>
      `${API_PREFIX}/mail-security/queue/${seg(id)}/${seg(action)}`,
    queueFlush: `${API_PREFIX}/mail-security/queue/flush`,
    rspamdStats: `${API_PREFIX}/mail-security/rspamd/stats`,
    rspamdHistory: `${API_PREFIX}/mail-security/rspamd/history`,
  },
  observability: {
    logServices: `${API_PREFIX}/observability/logs/services`,
    logs: `${API_PREFIX}/observability/logs`,
  },
  templates: {
    ...collectionOf('/templates'),
    versions: (id: string) => `${API_PREFIX}/templates/${seg(id)}/versions`,
    version: (id: string, version: number) =>
      `${API_PREFIX}/templates/${seg(id)}/versions/${version}`,
    publish: (id: string, version: number) =>
      `${API_PREFIX}/templates/${seg(id)}/versions/${version}/publish`,
    preview: (id: string) => `${API_PREFIX}/templates/${seg(id)}/preview`,
    versionCheck: (id: string, version: number) =>
      `${API_PREFIX}/templates/${seg(id)}/versions/${version}/check`,
    testSend: (id: string, version: number) =>
      `${API_PREFIX}/templates/${seg(id)}/versions/${version}/test-send`,
    check: `${API_PREFIX}/templates/check`,
    brandKit: `${API_PREFIX}/templates/brand-kit`,
    assets: `${API_PREFIX}/templates/assets`,
    asset: (id: string) => `${API_PREFIX}/templates/assets/${seg(id)}`,
    meta: `${API_PREFIX}/templates/meta`,
  },
  transactional: {
    sendingDomains: `${API_PREFIX}/transactional/sending-domains`,
  },
  suppression: {
    check: `${API_PREFIX}/suppression/check`,
    entries: collectionOf('/suppression/entries'),
    import: `${API_PREFIX}/suppression/entries/import`,
    imports: `${API_PREFIX}/suppression/imports`,
    stats: `${API_PREFIX}/suppression/stats`,
    meta: `${API_PREFIX}/suppression/meta`,
  },
  contacts: {
    ...collectionOf('/contacts'),
    meta: `${API_PREFIX}/contacts/meta`,
    export: (id: string) => `${API_PREFIX}/contacts/${seg(id)}/export`,
    consents: (id: string) => `${API_PREFIX}/contacts/${seg(id)}/consents`,
    consent: (id: string) => `${API_PREFIX}/contacts/${seg(id)}/consent`,
    consentRequest: (id: string) => `${API_PREFIX}/contacts/${seg(id)}/consent/request`,
    imports: collectionOf('/contacts/imports'),
    lists: collectionOf('/contacts/lists'),
    listMembers: (id: string) => `${API_PREFIX}/contacts/lists/${seg(id)}/members`,
    listMembersRemove: (id: string) => `${API_PREFIX}/contacts/lists/${seg(id)}/members/remove`,
    attributes: `${API_PREFIX}/contacts/attributes`,
    attribute: (key: string) => `${API_PREFIX}/contacts/attributes/${seg(key)}`,
  },
  segments: {
    ...collectionOf('/segments'),
    meta: `${API_PREFIX}/segments/meta`,
    preview: `${API_PREFIX}/segments/preview`,
    contacts: (id: string) => `${API_PREFIX}/segments/${seg(id)}/contacts`,
  },
  campaigns: {
    ...collectionOf('/campaigns'),
    meta: `${API_PREFIX}/campaigns/meta`,
    schedule: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/schedule`,
    start: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/start`,
    pause: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/pause`,
    resume: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/resume`,
    cancel: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/cancel`,
    test: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/test`,
    batches: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/batches`,
    phases: (id: string) => `${API_PREFIX}/campaigns/${seg(id)}/phases`,
  },
  automations: {
    meta: `${API_PREFIX}/automations/meta`,
    workflows: collectionOf('/automations/workflows'),
    activate: (id: string) => `${API_PREFIX}/automations/workflows/${seg(id)}/activate`,
    pause: (id: string) => `${API_PREFIX}/automations/workflows/${seg(id)}/pause`,
    archive: (id: string) => `${API_PREFIX}/automations/workflows/${seg(id)}/archive`,
    runs: (id: string) => `${API_PREFIX}/automations/workflows/${seg(id)}/runs`,
    run: (id: string) => `${API_PREFIX}/automations/runs/${seg(id)}`,
    doubleOptIn: `${API_PREFIX}/automations/double-opt-in`,
    deliveries: `${API_PREFIX}/automations/double-opt-in/deliveries`,
  },
  analytics: {
    meta: `${API_PREFIX}/analytics/meta`,
    overview: `${API_PREFIX}/analytics/overview`,
    timeseries: `${API_PREFIX}/analytics/timeseries`,
    campaigns: collectionOf('/analytics/campaigns'),
    campaignLinks: (id: string) => `${API_PREFIX}/analytics/campaigns/${seg(id)}/links`,
    domains: `${API_PREFIX}/analytics/domains`,
  },
  billing: {
    meta: `${API_PREFIX}/billing/meta`,
    subscription: `${API_PREFIX}/billing/subscription`,
    usage: `${API_PREFIX}/billing/usage`,
    plans: collectionOf('/billing/plans'),
    retirePlan: (id: string) => `${API_PREFIX}/billing/plans/${seg(id)}/retire`,
    subscriptions: collectionOf('/billing/subscriptions'),
    tenantUsage: (tenantId: string) => `${API_PREFIX}/billing/usage/${seg(tenantId)}`,
  },
  reputation: {
    meta: `${API_PREFIX}/reputation/meta`,
    status: `${API_PREFIX}/reputation/status`,
    history: `${API_PREFIX}/reputation/history`,
    tenants: `${API_PREFIX}/reputation/tenants`,
    limits: (tenantId: string, sendClass: string) =>
      `${API_PREFIX}/reputation/tenants/${seg(tenantId)}/limits/${seg(sendClass)}`,
    suspend: (tenantId: string, sendClass: string) =>
      `${API_PREFIX}/reputation/tenants/${seg(tenantId)}/${seg(sendClass)}/suspend`,
    release: (tenantId: string, sendClass: string) =>
      `${API_PREFIX}/reputation/tenants/${seg(tenantId)}/${seg(sendClass)}/release`,
  },
  scheduler: {
    meta: `${API_PREFIX}/scheduler/meta`,
    handlers: `${API_PREFIX}/scheduler/handlers`,
    jobs: collectionOf('/scheduler/jobs'),
    enable: (id: string) => `${API_PREFIX}/scheduler/jobs/${seg(id)}/enable`,
    disable: (id: string) => `${API_PREFIX}/scheduler/jobs/${seg(id)}/disable`,
    run: (id: string) => `${API_PREFIX}/scheduler/jobs/${seg(id)}/run`,
    history: (id: string) => `${API_PREFIX}/scheduler/jobs/${seg(id)}/history`,
    cancelExecution: (id: string) => `${API_PREFIX}/scheduler/executions/${seg(id)}/cancel`,
    retryExecution: (id: string) => `${API_PREFIX}/scheduler/executions/${seg(id)}/retry`,
    tasks: collectionOf('/scheduler/tasks'),
    cancelTask: (id: string) => `${API_PREFIX}/scheduler/tasks/${seg(id)}/cancel`,
  },
  // services/webmail: prefijo autenticado por el propio servicio con la cookie cf_wm, sin
  // el access token de la plataforma. Solo lo usa api/webmail.ts. El nombre de carpeta va
  // codificado entero (una subcarpeta lleva el separador como %2F) y el servicio lo
  // decodifica.
  webmail: {
    session: `${API_PREFIX}/webmail/session`,
    meta: `${API_PREFIX}/webmail/meta`,
    identities: `${API_PREFIX}/webmail/identities`,
    vacation: `${API_PREFIX}/webmail/vacation`,
    addressBook: `${API_PREFIX}/webmail/address-book`,
    events: `${API_PREFIX}/webmail/events`,
    folders: `${API_PREFIX}/webmail/folders`,
    messages: (folder: string) => `${API_PREFIX}/webmail/folders/${seg(folder)}/messages`,
    message: (folder: string, uid: number) =>
      `${API_PREFIX}/webmail/folders/${seg(folder)}/messages/${uid}`,
    flags: (folder: string, uid: number) =>
      `${API_PREFIX}/webmail/folders/${seg(folder)}/messages/${uid}/flags`,
    move: (folder: string, uid: number) =>
      `${API_PREFIX}/webmail/folders/${seg(folder)}/messages/${uid}/move`,
    part: (folder: string, uid: number, part: string) =>
      `${API_PREFIX}/webmail/folders/${seg(folder)}/messages/${uid}/parts/${seg(part)}`,
    send: `${API_PREFIX}/webmail/send`,
    drafts: `${API_PREFIX}/webmail/drafts`,
  },
} as const;
