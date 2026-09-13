import { afterEach, describe, expect, it } from 'vitest';
import type { PermissionTriple } from '@/api/access';
import { evaluatePermission } from './can';
import { selectCan, useAccessStore } from './store';
import { MODULES } from './modules';

const perm = (module: string, resource: string, action: string): PermissionTriple => ({
  module,
  resource,
  action,
});

describe('evaluatePermission (espejo de AccessPolicy.HasPermission)', () => {
  it('concede la triple exacta y nada mas', () => {
    const perms = [perm(MODULES.identity, 'users', 'read')];
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'read')).toBe(true);
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'delete')).toBe(false);
    expect(evaluatePermission(perms, MODULES.identity, 'sessions', 'read')).toBe(false);
    expect(evaluatePermission(perms, MODULES.access, 'users', 'read')).toBe(false);
  });

  it('(*, *) cubre todo el modulo, pero solo ese modulo', () => {
    const perms = [perm(MODULES.audit, '*', '*')];
    expect(evaluatePermission(perms, MODULES.audit, 'logs', 'read')).toBe(true);
    expect(evaluatePermission(perms, MODULES.audit, 'integrity', 'verify')).toBe(true);
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'read')).toBe(false);
  });

  it('(recurso, *) cubre todas las acciones de ese recurso', () => {
    const perms = [perm(MODULES.access, 'roles', '*')];
    expect(evaluatePermission(perms, MODULES.access, 'roles', 'update')).toBe(true);
    expect(evaluatePermission(perms, MODULES.access, 'user_roles', 'assign')).toBe(false);
  });

  it('(*, accion) no concede nada, igual que en el backend', () => {
    const perms = [perm(MODULES.identity, '*', 'read')];
    expect(evaluatePermission(perms, MODULES.identity, 'users', 'read')).toBe(false);
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
});
