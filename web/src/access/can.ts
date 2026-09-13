import type { PermissionTriple } from '@/api/access';

const WILDCARD = '*';

/**
 * Espejo exacto de domain.AccessPolicy.HasPermission (access-control): mismo modulo y
 * despues una de tres formas, (recurso, accion), (*, *) o (recurso, *). Un permiso
 * (*, accion) NO cubre nada en el backend, asi que aqui tampoco: ofrecer en la interfaz
 * algo que el servidor va a rechazar es peor que no ofrecerlo.
 */
export function evaluatePermission(
  permissions: readonly PermissionTriple[],
  module: string,
  resource: string,
  action: string,
): boolean {
  for (const p of permissions) {
    if (p.module !== module) continue;
    if (p.resource === resource && p.action === action) return true;
    if (p.resource === WILDCARD && p.action === WILDCARD) return true;
    if (p.resource === resource && p.action === WILDCARD) return true;
  }
  return false;
}
