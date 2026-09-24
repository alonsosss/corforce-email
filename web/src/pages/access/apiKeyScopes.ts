import type { PermissionTriple } from '@/api/access';
import type { ApiKeyScope } from '@/api/apiKeys';

export function scopeKey(scope: PermissionTriple): string {
  return `${scope.module}/${scope.resource}/${scope.action}`;
}

/** Descripcion del catalogo de permisos; sin ella, el triple tal cual. */
export function scopeLabel(scope: ApiKeyScope): string {
  return scope.description?.trim() || scopeKey(scope);
}
