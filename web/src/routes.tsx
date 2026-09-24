import { lazy, Suspense, type ComponentType } from 'react';
import { createBrowserRouter, Navigate, RouterProvider } from 'react-router-dom';
import { MODULES, type ModuleName } from '@/access/modules';
import { SYSTEM_ROLES, type SystemRole } from '@/access/roles';
import { PlatformSession } from '@/layout/PlatformSession';
import { RequireAuth } from '@/layout/RequireAuth';
import { RequireModule } from '@/layout/RequireModule';
import { Shell } from '@/layout/Shell';
import { LoadingBlock } from '@/design/components';

import { paths } from '@/paths';

// Unico lugar donde se declaran las pantallas y como se cargan. El menu (layout/nav.ts)
// solo puede apuntar a rutas de aqui; un test lo verifica.
type PageLoader = () => Promise<{ default: ComponentType }>;

export interface ScreenDecl {
  /** Ruta absoluta; :param para segmentos dinamicos. */
  path: string;
  /** Modulo de permiso que debe tener el usuario para entrar. Ausente = cualquier sesion. */
  module?: ModuleName;
  /** Rol del sistema que exige el handler ademas del modulo. El superadmin siempre pasa. */
  role?: SystemRole;
  load: PageLoader;
}

export const PUBLIC_SCREENS: readonly ScreenDecl[] = [
  { path: paths.login, load: () => import('@/pages/auth/LoginPage') },
  { path: paths.forgotPassword, load: () => import('@/pages/auth/ForgotPasswordPage') },
  { path: paths.resetPassword, load: () => import('@/pages/auth/ResetPasswordPage') },
  { path: paths.sessionExpired, load: () => import('@/pages/auth/SessionExpiredPage') },
];

export const SCREENS: readonly ScreenDecl[] = [
  { path: paths.home, load: () => import('@/pages/home/HomePage') },
  { path: paths.account, load: () => import('@/pages/account/AccountPage') },
  { path: paths.users, module: MODULES.identity, load: () => import('@/pages/users/UsersPage') },
  {
    path: `${paths.users}/:id`,
    module: MODULES.identity,
    load: () => import('@/pages/users/UserDetailPage'),
  },
  { path: paths.roles, module: MODULES.access, load: () => import('@/pages/roles/RolesPage') },
  {
    path: `${paths.roles}/:id`,
    module: MODULES.access,
    load: () => import('@/pages/roles/RoleDetailPage'),
  },
  { path: paths.denials, module: MODULES.access, load: () => import('@/pages/access/DenialsPage') },
  {
    path: paths.sessions,
    module: MODULES.identity,
    role: SYSTEM_ROLES.tenantAdmin,
    load: () => import('@/pages/sessions/SessionsPage'),
  },
  {
    path: paths.organizations,
    module: MODULES.organization,
    load: () => import('@/pages/organizations/OrganizationsPage'),
  },
  {
    path: paths.migrations,
    module: MODULES.organization,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/organizations/MigrationsPage'),
  },
  {
    path: `${paths.organizations}/:id`,
    module: MODULES.organization,
    load: () => import('@/pages/organizations/OrganizationDetailPage'),
  },
  {
    path: paths.cells,
    module: MODULES.organization,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/cells/CellsPage'),
  },
  {
    path: paths.auditLogs,
    module: MODULES.audit,
    load: () => import('@/pages/audit/AuditLogsPage'),
  },
  {
    path: paths.securityEvents,
    module: MODULES.audit,
    load: () => import('@/pages/audit/SecurityEventsPage'),
  },
  {
    path: paths.integrity,
    module: MODULES.audit,
    load: () => import('@/pages/audit/IntegrityPage'),
  },
  {
    path: paths.domains,
    module: MODULES.domains,
    load: () => import('@/pages/domains/DomainsPage'),
  },
  {
    path: `${paths.domains}/:id`,
    module: MODULES.domains,
    load: () => import('@/pages/domains/DomainDetailPage'),
  },
  {
    path: paths.mailboxes,
    module: MODULES.mailboxes,
    load: () => import('@/pages/mailboxes/MailboxesPage'),
  },
  {
    path: `${paths.mailboxes}/:id`,
    module: MODULES.mailboxes,
    load: () => import('@/pages/mailboxes/MailboxDetailPage'),
  },
  {
    path: paths.mailRouting,
    module: MODULES.mailRouting,
    load: () => import('@/pages/routing/RoutingPage'),
  },
  {
    path: paths.mailSecurity,
    module: MODULES.mailSecurity,
    load: () => import('@/pages/mailSecurity/MailSecurityPage'),
  },
  {
    path: paths.quarantine,
    module: MODULES.mailSecurity,
    load: () => import('@/pages/quarantine/QuarantinePage'),
  },
  {
    path: paths.templates,
    module: MODULES.templates,
    load: () => import('@/pages/templates/TemplatesPage'),
  },
  {
    path: `${paths.templates}/:id`,
    module: MODULES.templates,
    load: () => import('@/pages/templates/TemplateDetailPage'),
  },
  {
    path: paths.brandKit,
    module: MODULES.templates,
    load: () => import('@/pages/templates/brandKit/BrandKitPage'),
  },
  {
    path: paths.landingPages,
    module: MODULES.templates,
    load: () => import('@/pages/templates/landing/LandingPagesPage'),
  },
  {
    path: `${paths.landingPages}/:id`,
    module: MODULES.templates,
    load: () => import('@/pages/templates/landing/LandingPageDetailPage'),
  },
  {
    path: paths.suppression,
    module: MODULES.suppression,
    load: () => import('@/pages/suppression/SuppressionPage'),
  },
  {
    path: paths.reputation,
    module: MODULES.reputation,
    load: () => import('@/pages/reputation/ReputationPage'),
  },
  {
    path: paths.contacts,
    module: MODULES.contacts,
    load: () => import('@/pages/contacts/ContactsPage'),
  },
  {
    path: `${paths.contacts}/:id`,
    module: MODULES.contacts,
    load: () => import('@/pages/contacts/ContactDetailPage'),
  },
  {
    path: paths.contactListPattern,
    module: MODULES.contacts,
    load: () => import('@/pages/contacts/ListDetailPage'),
  },
  {
    path: paths.contactFormPattern,
    module: MODULES.contacts,
    load: () => import('@/pages/contacts/forms/FormDetailPage'),
  },
  {
    path: paths.segments,
    module: MODULES.segments,
    load: () => import('@/pages/segments/SegmentsPage'),
  },
  {
    path: paths.segmentNew,
    module: MODULES.segments,
    load: () => import('@/pages/segments/SegmentEditorPage'),
  },
  {
    path: `${paths.segments}/:id`,
    module: MODULES.segments,
    load: () => import('@/pages/segments/SegmentEditorPage'),
  },
  {
    path: paths.campaigns,
    module: MODULES.campaigns,
    load: () => import('@/pages/campaigns/CampaignsPage'),
  },
  {
    path: `${paths.campaigns}/:id`,
    module: MODULES.campaigns,
    load: () => import('@/pages/campaigns/CampaignDetailPage'),
  },
  {
    path: paths.automations,
    module: MODULES.automations,
    load: () => import('@/pages/automations/AutomationsPage'),
  },
  {
    path: paths.automationNew,
    module: MODULES.automations,
    load: () => import('@/pages/automations/WorkflowPage'),
  },
  {
    path: `${paths.automations}/:id`,
    module: MODULES.automations,
    load: () => import('@/pages/automations/WorkflowPage'),
  },
  {
    path: paths.automationRunPattern,
    module: MODULES.automations,
    load: () => import('@/pages/automations/RunDetailPage'),
  },
  {
    path: paths.analytics,
    module: MODULES.analytics,
    load: () => import('@/pages/analytics/AnalyticsPage'),
  },
  {
    path: paths.analyticsCampaignLinksPattern,
    module: MODULES.analytics,
    load: () => import('@/pages/analytics/CampaignLinksPage'),
  },
  {
    path: paths.billing,
    module: MODULES.billing,
    load: () => import('@/pages/billing/PlanPage'),
  },
  {
    path: paths.scheduler,
    module: MODULES.scheduler,
    load: () => import('@/pages/scheduler/SchedulerPage'),
  },
  {
    path: `${paths.scheduler}/:id`,
    module: MODULES.scheduler,
    load: () => import('@/pages/scheduler/JobDetailPage'),
  },
  // Operacion de la plataforma: la exige el rol superadmin en el propio servicio, no un
  // permiso de modulo (billing plans y subscriptions, reputation tenants).
  {
    path: paths.platformBilling,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/billing/PlatformBillingPage'),
  },
  {
    path: paths.platformReputation,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/reputation/PlatformReputationPage'),
  },
  {
    path: paths.platformMailQueue,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/mailQueue/MailQueuePage'),
  },
  {
    path: paths.platformLogs,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/logs/LogsPage'),
  },
  {
    path: paths.platformRspamd,
    role: SYSTEM_ROLES.superadmin,
    load: () => import('@/pages/rspamd/RspamdPage'),
  },
];

/**
 * Pantallas de pantalla completa: con sesion y con la misma primera capa de control, pero
 * fuera del Shell (sin menu ni barra superior), como el editor visual de plantillas.
 */
export const FULLSCREEN_SCREENS: readonly ScreenDecl[] = [
  {
    path: paths.templateEditorPattern,
    module: MODULES.templates,
    load: () => import('@/pages/templates/editor/TemplateEditorPage'),
  },
  {
    path: paths.templateNewEditor,
    module: MODULES.templates,
    load: () => import('@/pages/templates/editor/NewTemplateEditorPage'),
  },
  {
    path: paths.landingPageEditorPattern,
    module: MODULES.templates,
    load: () => import('@/pages/templates/landing/editor/PageEditorPage'),
  },
];

const NotFoundPage = lazy(() => import('@/pages/NotFoundPage'));

function lazyElement(load: PageLoader) {
  const Page = lazy(load);
  return (
    <Suspense fallback={<LoadingBlock />}>
      <Page />
    </Suspense>
  );
}

/**
 * El webmail es otra sesion (la del buzon, cookie cf_wm): vive fuera del layout y de la
 * sesion de la plataforma, en su propio chunk, con sus propias rutas.
 */
export const WEBMAIL_ROUTE = `${paths.webmail}/*`;

export function createAppRouter() {
  return createBrowserRouter([
    { path: WEBMAIL_ROUTE, element: lazyElement(() => import('@/pages/webmail/WebmailApp')) },
    {
      element: <PlatformSession />,
      children: [
        ...PUBLIC_SCREENS.map((screen) => ({
          path: screen.path,
          element: lazyElement(screen.load),
        })),
        {
          element: <RequireAuth />,
          children: [
            ...FULLSCREEN_SCREENS.map((screen) => ({
              path: screen.path,
              element: (
                <RequireModule module={screen.module} role={screen.role}>
                  {lazyElement(screen.load)}
                </RequireModule>
              ),
            })),
            {
              element: <Shell />,
              children: [
                ...SCREENS.map((screen) => ({
                  path: screen.path,
                  element: (
                    <RequireModule module={screen.module} role={screen.role}>
                      {lazyElement(screen.load)}
                    </RequireModule>
                  ),
                })),
                {
                  path: '*',
                  element: (
                    <Suspense fallback={<LoadingBlock />}>
                      <NotFoundPage />
                    </Suspense>
                  ),
                },
              ],
            },
          ],
        },
        { path: '*', element: <Navigate to={paths.home} replace /> },
      ],
    },
  ]);
}

export function AppRoutes() {
  return <RouterProvider router={createAppRouter()} />;
}
