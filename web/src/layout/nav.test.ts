import { describe, expect, it } from 'vitest';
import { MODULES } from '@/access/modules';
import { SYSTEM_ROLES } from '@/access/roles';
import { PUBLIC_SCREENS, SCREENS } from '@/routes';
import { NAV, visibleNav } from './nav';

const allItems = NAV.flatMap((group) => group.items);

function accessWith(modules: string[], roles: string[] = []) {
  return {
    hasModule: (m: string) => modules.includes(m),
    hasRole: (r: string) => roles.includes(r),
    isSuperadmin: roles.includes(SYSTEM_ROLES.superadmin),
  };
}

describe('menu frente a rutas', () => {
  it('cada entrada del menu apunta a una pantalla declarada', () => {
    const declared = new Set(SCREENS.map((s) => s.path));
    const orphans = allItems.filter((item) => !declared.has(item.to)).map((item) => item.to);
    expect(orphans).toEqual([]);
  });

  it('el menu y la ruta exigen el mismo modulo y el mismo rol', () => {
    for (const item of allItems) {
      const screen = SCREENS.find((s) => s.path === item.to);
      expect(screen, item.to).toBeDefined();
      expect(screen?.module, item.to).toBe(item.module);
      expect(screen?.role, item.to).toBe(item.role);
    }
  });

  it('no hay rutas duplicadas ni entradas repetidas en el menu', () => {
    const paths = [...PUBLIC_SCREENS, ...SCREENS].map((s) => s.path);
    expect(new Set(paths).size).toBe(paths.length);
    const targets = allItems.map((i) => i.to);
    expect(new Set(targets).size).toBe(targets.length);
  });

  it('filtra por los modulos del usuario', () => {
    const groups = visibleNav(accessWith([MODULES.identity]));
    const visible = groups.flatMap((g) => g.items.map((i) => i.module ?? null));
    expect(visible).toContain(MODULES.identity);
    expect(visible).not.toContain(MODULES.access);
    expect(visible).not.toContain(MODULES.organization);
    expect(groups.every((g) => g.items.length > 0)).toBe(true);
  });

  it('exige el rol del sistema cuando la pantalla lo pide, salvo al superadmin', () => {
    const modules: string[] = [MODULES.identity, MODULES.organization];
    const withRole = (roles: string[]) =>
      visibleNav(accessWith(modules, roles))
        .flatMap((g) => g.items)
        .filter((i) => i.role)
        .map((i) => i.to);

    expect(withRole([])).toEqual([]);
    const tenantAdmin = withRole([SYSTEM_ROLES.tenantAdmin]);
    expect(tenantAdmin.length).toBeGreaterThan(0);
    expect(
      tenantAdmin.every(
        (to) => allItems.find((i) => i.to === to)?.role === SYSTEM_ROLES.tenantAdmin,
      ),
    ).toBe(true);
    const everyRoleItem = allItems
      .filter((i) => i.role && modules.includes(i.module ?? ''))
      .map((i) => i.to);
    expect(withRole([SYSTEM_ROLES.superadmin]).sort()).toEqual(everyRoleItem.sort());
  });
});
