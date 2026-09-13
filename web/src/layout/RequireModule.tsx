import type { ReactNode } from 'react';
import type { ModuleName } from '@/access/modules';
import type { SystemRole } from '@/access/roles';
import { useAccess } from '@/access/useAccess';
import { LoadingBlock } from '@/design/components';
import { canSee } from './nav';
import ForbiddenPage from '@/pages/ForbiddenPage';

export interface RequireModuleProps {
  module?: ModuleName;
  role?: SystemRole;
  children: ReactNode;
}

/** Primera capa de control: la pantalla solo se monta si el acceso la incluye. */
export function RequireModule({ module, role, children }: RequireModuleProps) {
  const access = useAccess();
  if (!module && !role) return <>{children}</>;
  if (!access.loaded) return <LoadingBlock />;
  if (!canSee(access, { module, role })) return <ForbiddenPage />;
  return <>{children}</>;
}
