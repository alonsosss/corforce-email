import { useCallback } from 'react';
import {
  useAccessStore,
  selectCan,
  selectHasModule,
  selectHasRole,
  selectIsSuperadmin,
} from './store';
import { SYSTEM_ROLES } from './roles';

export interface Access {
  loaded: boolean;
  modules: string[];
  roles: string[];
  isAdmin: boolean;
  isSuperadmin: boolean;
  isTenantAdmin: boolean;
  disabledModules: string[];
  hasModule: (module: string) => boolean;
  hasRole: (role: string) => boolean;
  can: (module: string, resource: string, action: string) => boolean;
}

export function useAccess(): Access {
  const state = useAccessStore();
  const hasModule = useCallback((module: string) => selectHasModule(state, module), [state]);
  const hasRole = useCallback((role: string) => selectHasRole(state, role), [state]);
  const can = useCallback(
    (module: string, resource: string, action: string) =>
      selectCan(state, module, resource, action),
    [state],
  );
  return {
    loaded: state.loaded,
    modules: state.modules,
    roles: state.roles,
    isAdmin: state.isAdmin,
    isSuperadmin: selectIsSuperadmin(state),
    isTenantAdmin: selectHasRole(state, SYSTEM_ROLES.tenantAdmin),
    disabledModules: state.disabledModules,
    hasModule,
    hasRole,
    can,
  };
}
