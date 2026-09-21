import { MODULES } from './modules';

// Triples (module, resource, action) del catalogo sembrado en
// migrations/registry/005_seed_permissions.sql y 006 a 010 (correo, seguridad del correo,
// dominios, supresion y plantillas). La interfaz pregunta can() con estos valores; si el
// catalogo cambia, cambia aqui y en ningun otro sitio.
type Triple = readonly [module: string, resource: string, action: string];

export interface CrudPermissions {
  read: Triple;
  create: Triple;
  update: Triple;
  delete: Triple;
}

function crud<M extends string, R extends string>(module: M, resource: R) {
  return {
    read: [module, resource, 'read'],
    create: [module, resource, 'create'],
    update: [module, resource, 'update'],
    delete: [module, resource, 'delete'],
  } as const;
}

export const PERMISSIONS = {
  users: {
    read: [MODULES.identity, 'users', 'read'],
    create: [MODULES.identity, 'users', 'create'],
    update: [MODULES.identity, 'users', 'update'],
    delete: [MODULES.identity, 'users', 'delete'],
  },
  sessions: {
    read: [MODULES.identity, 'sessions', 'read'],
    revoke: [MODULES.identity, 'sessions', 'revoke'],
  },
  sessionPolicies: {
    read: [MODULES.identity, 'session_policies', 'read'],
    update: [MODULES.identity, 'session_policies', 'update'],
  },
  roles: {
    read: [MODULES.access, 'roles', 'read'],
    create: [MODULES.access, 'roles', 'create'],
    update: [MODULES.access, 'roles', 'update'],
    delete: [MODULES.access, 'roles', 'delete'],
  },
  userRoles: {
    read: [MODULES.access, 'user_roles', 'read'],
    assign: [MODULES.access, 'user_roles', 'assign'],
    revoke: [MODULES.access, 'user_roles', 'revoke'],
  },
  denials: {
    read: [MODULES.access, 'denials', 'read'],
  },
  tenants: {
    read: [MODULES.organization, 'tenants', 'read'],
    create: [MODULES.organization, 'tenants', 'create'],
    update: [MODULES.organization, 'tenants', 'update'],
    delete: [MODULES.organization, 'tenants', 'delete'],
  },
  tenantModules: {
    read: [MODULES.organization, 'modules', 'read'],
    update: [MODULES.organization, 'modules', 'update'],
  },
  migrations: {
    read: [MODULES.organization, 'migrations', 'read'],
    run: [MODULES.organization, 'migrations', 'run'],
  },
  auditLogs: {
    read: [MODULES.audit, 'logs', 'read'],
  },
  // scheduler/jobs/delete esta sembrado pero no tiene ruta: la interfaz no lo pregunta.
  schedulerJobs: {
    read: [MODULES.scheduler, 'jobs', 'read'],
    create: [MODULES.scheduler, 'jobs', 'create'],
    update: [MODULES.scheduler, 'jobs', 'update'],
    run: [MODULES.scheduler, 'jobs', 'run'],
  },
  schedulerExecutions: {
    read: [MODULES.scheduler, 'executions', 'read'],
    cancel: [MODULES.scheduler, 'executions', 'cancel'],
    retry: [MODULES.scheduler, 'executions', 'retry'],
  },
  schedulerTasks: {
    read: [MODULES.scheduler, 'tasks', 'read'],
    cancel: [MODULES.scheduler, 'tasks', 'cancel'],
  },
  securityEvents: {
    read: [MODULES.audit, 'security_events', 'read'],
    acknowledge: [MODULES.audit, 'security_events', 'acknowledge'],
  },
  // Lanzar, ver y cancelar una verificacion de la cadena (/audit/integrity/runs) exige verify;
  // integrity/read no tiene ruta, asi que la interfaz no lo pregunta.
  integrity: {
    verify: [MODULES.audit, 'integrity', 'verify'],
  },

  domains: {
    ...crud(MODULES.domains, 'domains'),
    verify: [MODULES.domains, 'domains', 'verify'],
    rotateDkim: [MODULES.domains, 'domains', 'rotate_dkim'],
    revokeDkim: [MODULES.domains, 'domains', 'revoke_dkim'],
    publishDns: [MODULES.domains, 'domains', 'publish_dns'],
  },
  // Conexion de la empresa con su proveedor DNS (032_domain_service_dns_providers.sql).
  dnsProviders: {
    read: [MODULES.domains, 'dns_providers', 'read'],
    connect: [MODULES.domains, 'dns_providers', 'connect'],
    disconnect: [MODULES.domains, 'dns_providers', 'disconnect'],
  },
  aliasDomains: crud(MODULES.domains, 'alias_domains'),
  // Politica MTA-STS de un dominio (registry 034_mail_directory_mta_sts_permissions.sql).
  mtaSts: {
    read: [MODULES.domains, 'mta_sts', 'read'],
    update: [MODULES.domains, 'mta_sts', 'update'],
  },

  mailboxes: {
    ...crud(MODULES.mailboxes, 'mailboxes'),
    setPassword: [MODULES.mailboxes, 'mailboxes', 'set_password'],
  },
  appPasswords: crud(MODULES.mailboxes, 'app_passwords'),
  sieve: {
    read: [MODULES.mailboxes, 'sieve', 'read'],
    update: [MODULES.mailboxes, 'sieve', 'update'],
  },

  // Migracion de buzones desde otro servidor IMAP (mail-migration).
  mailMigrationJobs: {
    read: [MODULES.mailMigration, 'jobs', 'read'],
    create: [MODULES.mailMigration, 'jobs', 'create'],
    cancel: [MODULES.mailMigration, 'jobs', 'cancel'],
  },

  aliases: crud(MODULES.mailRouting, 'aliases'),
  spamAliases: crud(MODULES.mailRouting, 'spam_aliases'),
  senderAcl: crud(MODULES.mailRouting, 'sender_acl'),
  relayhosts: crud(MODULES.mailRouting, 'relayhosts'),
  transports: crud(MODULES.mailRouting, 'transports'),
  tlsPolicies: crud(MODULES.mailRouting, 'tls_policies'),
  recipientMaps: crud(MODULES.mailRouting, 'recipient_maps'),
  bccMaps: crud(MODULES.mailRouting, 'bcc_maps'),

  spamScores: {
    read: [MODULES.mailSecurity, 'spam_scores', 'read'],
    update: [MODULES.mailSecurity, 'spam_scores', 'update'],
    delete: [MODULES.mailSecurity, 'spam_scores', 'delete'],
  },
  addressLists: {
    read: [MODULES.mailSecurity, 'address_lists', 'read'],
    create: [MODULES.mailSecurity, 'address_lists', 'create'],
    delete: [MODULES.mailSecurity, 'address_lists', 'delete'],
  },
  footers: {
    read: [MODULES.mailSecurity, 'footers', 'read'],
    update: [MODULES.mailSecurity, 'footers', 'update'],
    delete: [MODULES.mailSecurity, 'footers', 'delete'],
  },
  forwardingHosts: {
    read: [MODULES.mailSecurity, 'forwarding_hosts', 'read'],
    create: [MODULES.mailSecurity, 'forwarding_hosts', 'create'],
    delete: [MODULES.mailSecurity, 'forwarding_hosts', 'delete'],
  },
  rateLimits: {
    read: [MODULES.mailSecurity, 'rate_limits', 'read'],
    update: [MODULES.mailSecurity, 'rate_limits', 'update'],
    delete: [MODULES.mailSecurity, 'rate_limits', 'delete'],
  },
  mailboxTags: {
    read: [MODULES.mailSecurity, 'mailbox_tags', 'read'],
    update: [MODULES.mailSecurity, 'mailbox_tags', 'update'],
  },
  quarantine: {
    read: [MODULES.mailSecurity, 'quarantine', 'read'],
    delete: [MODULES.mailSecurity, 'quarantine', 'delete'],
    release: [MODULES.mailSecurity, 'quarantine', 'release'],
    learn: [MODULES.mailSecurity, 'quarantine', 'learn'],
  },
  quarantineSettings: {
    read: [MODULES.mailSecurity, 'quarantine_settings', 'read'],
    update: [MODULES.mailSecurity, 'quarantine_settings', 'update'],
  },

  templates: {
    ...crud(MODULES.templates, 'templates'),
    publish: [MODULES.templates, 'templates', 'publish'],
    render: [MODULES.templates, 'templates', 'render'],
  },

  suppressionEntries: {
    read: [MODULES.suppression, 'entries', 'read'],
    create: [MODULES.suppression, 'entries', 'create'],
    delete: [MODULES.suppression, 'entries', 'delete'],
    import: [MODULES.suppression, 'entries', 'import'],
  },
  suppressionStats: {
    read: [MODULES.suppression, 'stats', 'read'],
  },

  // 012 a 017: contactos, segmentos, campanas, analitica, plan y reputacion. Los permisos
  // de plataforma (billing plans y subscriptions, reputation tenants) no se piden aqui:
  // esas pantallas son del rol superadmin.
  contacts: {
    ...crud(MODULES.contacts, 'contacts'),
    import: [MODULES.contacts, 'contacts', 'import'],
    export: [MODULES.contacts, 'contacts', 'export'],
  },
  contactLists: crud(MODULES.contacts, 'lists'),
  contactAttributes: crud(MODULES.contacts, 'attributes'),
  consents: {
    read: [MODULES.contacts, 'consents', 'read'],
    create: [MODULES.contacts, 'consents', 'create'],
  },
  segments: {
    ...crud(MODULES.segments, 'segments'),
    preview: [MODULES.segments, 'segments', 'preview'],
  },
  campaigns: {
    ...crud(MODULES.campaigns, 'campaigns'),
    send: [MODULES.campaigns, 'campaigns', 'send'],
    cancel: [MODULES.campaigns, 'campaigns', 'cancel'],
  },
  campaignStats: {
    read: [MODULES.campaigns, 'stats', 'read'],
  },
  // 019: activate cubre activar, pausar y archivar; settings, el doble opt-in y su historial.
  automationWorkflows: {
    ...crud(MODULES.automations, 'workflows'),
    activate: [MODULES.automations, 'workflows', 'activate'],
  },
  automationRuns: {
    read: [MODULES.automations, 'runs', 'read'],
  },
  automationSettings: {
    read: [MODULES.automations, 'settings', 'read'],
    update: [MODULES.automations, 'settings', 'update'],
  },
  analyticsReports: {
    read: [MODULES.analytics, 'reports', 'read'],
  },
  billingSubscription: {
    read: [MODULES.billing, 'subscription', 'read'],
  },
  billingUsage: {
    read: [MODULES.billing, 'usage', 'read'],
  },
  reputationStatus: {
    read: [MODULES.reputation, 'status', 'read'],
  },
  reputationHistory: {
    read: [MODULES.reputation, 'history', 'read'],
  },
} as const satisfies Record<string, Record<string, Triple>>;

export type PermissionTripleConst = Triple;
