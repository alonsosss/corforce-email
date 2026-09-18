import { afterEach, describe, expect, it } from 'vitest';
import type { PermissionTriple } from '@/api/access';
import { evaluatePermission } from './can';
import { selectCan, useAccessStore } from './store';
import { MODULES } from './modules';
import { SYSTEM_ROLES } from './roles';

const perm = (module: string, resource: string, action: string): PermissionTriple => ({
  module,
  resource,
  action,
});

describe('evaluatePermission (espejo de pkg/authz y AccessPolicy.HasPermission)', () => {
  it('concede la triple exacta y nada mas', () => {
    const perms = [perm(MODULES.identity, 'users', 'read')];
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'read')).toBe(true);
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'delete')).toBe(false);
    expect(evaluatePermission(perms, MODULES.identity, 'sessions', 'read')).toBe(false);
    expect(evaluatePermission(perms, MODULES.access, 'users', 'read')).toBe(false);
  });

  it('el comodin de recurso es independiente: (*, read) lee cualquier recurso del modulo', () => {
    const perms = [perm(MODULES.contacts, '*', 'read')];
    expect(evaluatePermission(perms, MODULES.contacts, 'lists', 'read')).toBe(true);
    expect(evaluatePermission(perms, MODULES.contacts, 'contacts', 'read')).toBe(true);
    expect(evaluatePermission(perms, MODULES.contacts, 'lists', 'delete')).toBe(false);
  });

  it('el comodin de accion es independiente: (recurso, *) cubre solo ese recurso', () => {
    const perms = [perm(MODULES.access, 'roles', '*')];
    expect(evaluatePermission(perms, MODULES.access, 'roles', 'update')).toBe(true);
    expect(evaluatePermission(perms, MODULES.access, 'roles', 'delete')).toBe(true);
    expect(evaluatePermission(perms, MODULES.access, 'user_roles', 'assign')).toBe(false);
  });

  it('(*, *) cubre todo el modulo', () => {
    const perms = [perm(MODULES.audit, '*', '*')];
    expect(evaluatePermission(perms, MODULES.audit, 'logs', 'read')).toBe(true);
    expect(evaluatePermission(perms, MODULES.audit, 'integrity', 'verify')).toBe(true);
  });

  it('ningun comodin cruza de modulo', () => {
    const perms = [
      perm(MODULES.audit, '*', '*'),
      perm(MODULES.contacts, '*', 'read'),
      perm(MODULES.access, 'roles', '*'),
    ];
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'read')).toBe(false);
    expect(evaluatePermission(perms, MODULES.segments, 'segments', 'read')).toBe(false);
    expect(evaluatePermission(perms, MODULES.identity, 'roles', 'update')).toBe(false);
  });

  it('sin permisos no concede nada', () => {
    expect(evaluatePermission([], MODULES.identity, 'users', 'read')).toBe(false);
  });
});

describe('selectCan', () => {
  afterEach(() => useAccessStore.getState().reset());

  it('decide con la politica cuando se pudo cargar', () => {
    useAccessStore.setState({
      loaded: true,
      isAdmin: true,
      policyLoaded: true,
      permissions: [perm(MODULES.identity, 'users', 'read')],
    });
    const state = useAccessStore.getState();
    expect(selectCan(state, MODULES.identity, 'users', 'read')).toBe(true);
    expect(selectCan(state, MODULES.identity, 'users', 'delete')).toBe(false);
  });

  it('sin politica cae a is_admin: el backend sigue siendo la barrera', () => {
    useAccessStore.setState({ loaded: true, isAdmin: true, policyLoaded: false, permissions: [] });
    expect(selectCan(useAccessStore.getState(), MODULES.identity, 'users', 'delete')).toBe(true);
    useAccessStore.setState({ isAdmin: false });
    expect(selectCan(useAccessStore.getState(), MODULES.identity, 'users', 'read')).toBe(false);
  });

  it('el superadmin pasa aunque la politica cargada no traiga el permiso de plataforma', () => {
    // Caso real (2026-09-18): organization/tenants/create es de alcance plataforma y
    // access_control nunca lo concede en role_permissions, ni al superadmin. La politica
    // carga bien (policyLoaded: true) pero sin ese permiso, y el boton "Nueva empresa"
    // -que la pagina guarda con `isSuperadmin && can(...)`- no aparecia nunca.
    useAccessStore.setState({
      loaded: true,
      isAdmin: true,
      roles: [SYSTEM_ROLES.superadmin],
      policyLoaded: true,
      permissions: [],
    });
    expect(selectCan(useAccessStore.getState(), MODULES.organization, 'tenants', 'create')).toBe(
      true,
    );
  });

  it('un tenant_admin sin el permiso concreto sigue sin pasar', () => {
    useAccessStore.setState({
      loaded: true,
      isAdmin: true,
      roles: [SYSTEM_ROLES.tenantAdmin],
      policyLoaded: true,
      permissions: [],
    });
    expect(selectCan(useAccessStore.getState(), MODULES.organization, 'tenants', 'create')).toBe(
      false,
    );
  });
});
