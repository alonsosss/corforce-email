// Modulos de permiso (docs/Usuarios_Roles_y_Acceso.md, seccion 3). Coinciden con la
// columna `module` de access_control.permissions y con el campo `module` de
// services/gateway/routes.json.
export const MODULES = {
  organization: 'organization',
  identity: 'identity',
  access: 'access',
  audit: 'audit',
  scheduler: 'scheduler',
  domains: 'domains',
  mailboxes: 'mailboxes',
  mailMigration: 'migration',
  mailRouting: 'mail_routing',
  mailSecurity: 'mail_security',
  templates: 'templates',
  transactional: 'transactional',
  suppression: 'suppression',
  contacts: 'contacts',
  segments: 'segments',
  campaigns: 'campaigns',
  automations: 'automations',
  analytics: 'analytics',
  billing: 'billing',
  reputation: 'reputation',
} as const;

export type ModuleName = (typeof MODULES)[keyof typeof MODULES];
