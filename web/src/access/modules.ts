// Modulos de permiso del plano de control (docs/Usuarios_Roles_y_Acceso.md, seccion 3).
// Coinciden con la columna `module` de access_control.permissions y con el campo `module`
// de services/gateway/routes.json. Los modulos de correo se anadiran con sus pantallas.
export const MODULES = {
  organization: 'organization',
  identity: 'identity',
  access: 'access',
  audit: 'audit',
  scheduler: 'scheduler',
} as const;

export type ModuleName = (typeof MODULES)[keyof typeof MODULES];
