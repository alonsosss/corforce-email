import type { PermissionTriple } from '@/api/access';

const WILDCARD = '*';

/**
 * Espejo de la regla de pkg/authz y de domain.AccessPolicy.HasPermission (access-control):
 * un permiso concede cuando el modulo es el mismo y, por separado, el recurso coincide o es
 * "*" y la accion coincide o es "*". Los dos comodines son independientes: (*, read)
 * concede leer cualquier recurso del modulo, pero nada de otro modulo.
 */
export function evaluatePermission(
  permissions: readonly PermissionTriple[],
  module: string,
  resource: string,
  action: string,
): boolean {
  return permissions.some(
    (p) =>
      p.module === module &&
      (p.resource === resource || p.resource === WILDCARD) &&
      (p.action === action || p.action === WILDCARD),
  );
}
