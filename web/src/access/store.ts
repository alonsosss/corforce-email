import { create } from 'zustand';
import { accessApi, type PermissionTriple } from '@/api/access';
import { evaluatePermission } from './can';
import { SYSTEM_ROLES } from './roles';

/*
 * Acceso operativo del usuario, cargado una vez por sesion:
 *  - GET /access/my-modules: modulos con permiso (menu), roles y si es administrador.
 *  - GET /policy/{userID}: permisos (module, resource, action) para can().
 *
 * El menu es la primera de las tres capas de control; el gateway y cada handler siguen
 * decidiendo por su cuenta. Si la politica no se puede leer, can() cae a is_admin: el
 * backend sigue siendo la barrera y dejar sin botones a un administrador por un fallo
 * de red es peor.
 */
export interface AccessState {
  loaded: boolean;
  isAdmin: boolean;
  roles: string[];
  modules: string[];
  writeModules: string[];
  writeActions: Record<string, string[]>;
  disabledModules: string[];
  permissions: PermissionTriple[];
  policyLoaded: boolean;
  load: (userId: string) => Promise<void>;
  reset: () => void;
}

const initial = {
  loaded: false,
  isAdmin: false,
  roles: [] as string[],
  modules: [] as string[],
  writeModules: [] as string[],
  writeActions: {} as Record<string, string[]>,
  disabledModules: [] as string[],
  permissions: [] as PermissionTriple[],
  policyLoaded: false,
};

export const useAccessStore = create<AccessState>((set) => ({
  ...initial,

  load: async (userId) => {
    const [modulesRes, policyRes] = await Promise.all([
      accessApi.myModules(),
      accessApi.policy(userId).catch(() => null),
    ]);
    const access = modulesRes.data;
    set({
      loaded: true,
      isAdmin: access.is_admin,
      roles: access.roles ?? [],
      modules: access.modules ?? [],
      writeModules: access.write_modules ?? [],
      writeActions: access.write_actions ?? {},
      disabledModules: access.disabled_modules ?? [],
      permissions: policyRes?.data.permissions ?? [],
      policyLoaded: policyRes !== null,
    });
  },

  reset: () => set({ ...initial }),
}));

export function selectHasModule(state: AccessState, module: string): boolean {
  return state.modules.includes(module);
}

export function selectCan(
  state: AccessState,
  module: string,
  resource: string,
  action: string,
): boolean {
  if (!state.policyLoaded) return state.isAdmin;
  return evaluatePermission(state.permissions, module, resource, action);
}

export function selectHasRole(state: AccessState, role: string): boolean {
  return state.roles.includes(role);
}

export function selectIsSuperadmin(state: AccessState): boolean {
  return selectHasRole(state, SYSTEM_ROLES.superadmin);
}
