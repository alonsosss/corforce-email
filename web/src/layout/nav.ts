import type { ComponentType } from 'react';
import { MODULES, type ModuleName } from '@/access/modules';
import { SYSTEM_ROLES, type SystemRole } from '@/access/roles';
import type { Access } from '@/access/useAccess';
import type { MessageKey } from '@/i18n';
import { paths } from '@/paths';
import {
  IconAlertTriangle,
  IconBuilding,
  IconClipboard,
  IconHome,
  IconLayers,
  IconLink,
  IconMonitor,
  IconServer,
  IconShieldCheck,
  IconShieldOff,
  IconUser,
  IconUsers,
  type IconProps,
} from '@/design/icons';

export interface NavItem {
  to: string;
  labelKey: MessageKey;
  icon: ComponentType<IconProps>;
  /** Modulo de permiso necesario; sin el, el item se muestra a cualquier sesion. */
  module?: ModuleName;
  /** Rol del sistema exigido por el handler. El superadmin siempre pasa. */
  role?: SystemRole;
}

export interface NavGroup {
  labelKey: MessageKey;
  items: NavItem[];
}

// El menu se construye desde esta declaracion y se filtra con los modulos de
// GET /access/my-modules. Las pantallas de correo (dominios, buzones, aliases) se
// anadiran como un grupo mas cuando existan sus APIs.
export const NAV: NavGroup[] = [
  {
    labelKey: 'nav.group.general',
    items: [
      { to: paths.home, labelKey: 'nav.home', icon: IconHome },
      { to: paths.account, labelKey: 'nav.account', icon: IconUser },
    ],
  },
  {
    labelKey: 'nav.group.identity',
    items: [
      { to: paths.users, labelKey: 'nav.users', icon: IconUsers, module: MODULES.identity },
      {
        to: paths.sessions,
        labelKey: 'nav.sessions',
        icon: IconMonitor,
        module: MODULES.identity,
        role: SYSTEM_ROLES.tenantAdmin,
      },
    ],
  },
  {
    labelKey: 'nav.group.access',
    items: [
      { to: paths.roles, labelKey: 'nav.roles', icon: IconShieldCheck, module: MODULES.access },
      { to: paths.denials, labelKey: 'nav.denials', icon: IconShieldOff, module: MODULES.access },
    ],
  },
  {
    labelKey: 'nav.group.platform',
    items: [
      {
        to: paths.organizations,
        labelKey: 'nav.organizations',
        icon: IconBuilding,
        module: MODULES.organization,
      },
      {
        to: paths.migrations,
        labelKey: 'nav.migrations',
        icon: IconLayers,
        module: MODULES.organization,
        role: SYSTEM_ROLES.superadmin,
      },
      {
        to: paths.cells,
        labelKey: 'nav.cells',
        icon: IconServer,
        module: MODULES.organization,
        role: SYSTEM_ROLES.superadmin,
      },
    ],
  },
  {
    labelKey: 'nav.group.audit',
    items: [
      {
        to: paths.auditLogs,
        labelKey: 'nav.auditLogs',
        icon: IconClipboard,
        module: MODULES.audit,
      },
      {
        to: paths.securityEvents,
        labelKey: 'nav.securityEvents',
        icon: IconAlertTriangle,
        module: MODULES.audit,
      },
      { to: paths.integrity, labelKey: 'nav.integrity', icon: IconLink, module: MODULES.audit },
    ],
  },
];

export function canSee(
  access: Pick<Access, 'hasModule' | 'hasRole' | 'isSuperadmin'>,
  item: { module?: ModuleName; role?: SystemRole },
): boolean {
  if (item.module && !access.hasModule(item.module)) return false;
  if (item.role && !access.isSuperadmin && !access.hasRole(item.role)) return false;
  return true;
}

export function visibleNav(
  access: Pick<Access, 'hasModule' | 'hasRole' | 'isSuperadmin'>,
): NavGroup[] {
  return NAV.map((group) => ({
    ...group,
    items: group.items.filter((item) => canSee(access, item)),
  })).filter((group) => group.items.length > 0);
}
