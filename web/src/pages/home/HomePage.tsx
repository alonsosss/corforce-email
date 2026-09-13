import type { ComponentType } from 'react';
import { Link } from 'react-router-dom';
import { MODULES, type ModuleName } from '@/access/modules';
import { SYSTEM_ROLES, type SystemRole } from '@/access/roles';
import { useAccess } from '@/access/useAccess';
import { canSee } from '@/layout/nav';
import { Card, EmptyState, PageHeader } from '@/design/components';
import {
  IconArchive,
  IconBan,
  IconBuilding,
  IconClipboard,
  IconFileText,
  IconFilter,
  IconGlobe,
  IconInbox,
  IconMonitor,
  IconRoute,
  IconServer,
  IconShieldCheck,
  IconUser,
  IconUsers,
  type IconProps,
} from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import { paths } from '@/paths';

interface HomeCard {
  to: string;
  titleKey: MessageKey;
  descriptionKey: MessageKey;
  icon: ComponentType<IconProps>;
  module?: ModuleName;
  role?: SystemRole;
}

// Tarjetas de acceso a cada modulo. Sin cifras: ningun servicio del plano de control
// expone hoy un resumen agregado, y no se inventan.
const CARDS: HomeCard[] = [
  {
    to: paths.account,
    titleKey: 'home.card.account',
    descriptionKey: 'home.card.accountDesc',
    icon: IconUser,
  },
  {
    to: paths.domains,
    titleKey: 'home.card.domains',
    descriptionKey: 'home.card.domainsDesc',
    icon: IconGlobe,
    module: MODULES.domains,
  },
  {
    to: paths.mailboxes,
    titleKey: 'home.card.mailboxes',
    descriptionKey: 'home.card.mailboxesDesc',
    icon: IconInbox,
    module: MODULES.mailboxes,
  },
  {
    to: paths.mailRouting,
    titleKey: 'home.card.mailRouting',
    descriptionKey: 'home.card.mailRoutingDesc',
    icon: IconRoute,
    module: MODULES.mailRouting,
  },
  {
    to: paths.mailSecurity,
    titleKey: 'home.card.mailSecurity',
    descriptionKey: 'home.card.mailSecurityDesc',
    icon: IconFilter,
    module: MODULES.mailSecurity,
  },
  {
    to: paths.quarantine,
    titleKey: 'home.card.quarantine',
    descriptionKey: 'home.card.quarantineDesc',
    icon: IconArchive,
    module: MODULES.mailSecurity,
  },
  {
    to: paths.templates,
    titleKey: 'home.card.templates',
    descriptionKey: 'home.card.templatesDesc',
    icon: IconFileText,
    module: MODULES.templates,
  },
  {
    to: paths.suppression,
    titleKey: 'home.card.suppression',
    descriptionKey: 'home.card.suppressionDesc',
    icon: IconBan,
    module: MODULES.suppression,
  },
  {
    to: paths.users,
    titleKey: 'home.card.users',
    descriptionKey: 'home.card.usersDesc',
    icon: IconUsers,
    module: MODULES.identity,
  },
  {
    to: paths.sessions,
    titleKey: 'home.card.sessions',
    descriptionKey: 'home.card.sessionsDesc',
    icon: IconMonitor,
    module: MODULES.identity,
    role: SYSTEM_ROLES.tenantAdmin,
  },
  {
    to: paths.roles,
    titleKey: 'home.card.roles',
    descriptionKey: 'home.card.rolesDesc',
    icon: IconShieldCheck,
    module: MODULES.access,
  },
  {
    to: paths.organizations,
    titleKey: 'home.card.organizations',
    descriptionKey: 'home.card.organizationsDesc',
    icon: IconBuilding,
    module: MODULES.organization,
  },
  {
    to: paths.cells,
    titleKey: 'home.card.cells',
    descriptionKey: 'home.card.cellsDesc',
    icon: IconServer,
    module: MODULES.organization,
    role: SYSTEM_ROLES.superadmin,
  },
  {
    to: paths.auditLogs,
    titleKey: 'home.card.audit',
    descriptionKey: 'home.card.auditDesc',
    icon: IconClipboard,
    module: MODULES.audit,
  },
];

export default function HomePage() {
  const access = useAccess();
  const cards = CARDS.filter((card) => canSee(access, card));

  return (
    <div>
      <PageHeader title={t('home.title')} description={t('home.subtitle')} />
      {access.loaded && access.modules.length === 0 && cards.length <= 1 ? (
        <Card>
          <EmptyState title={t('home.noModules')} />
        </Card>
      ) : null}
      <div className="cf-grid-cards">
        {cards.map((card) => {
          const Icon = card.icon;
          return (
            <Card key={card.to}>
              <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
                <span className="cf-inline" style={{ color: 'var(--cf-accent)' }}>
                  <Icon size={22} />
                  <span className="cf-card__title">{t(card.titleKey)}</span>
                </span>
                <p className="cf-text-secondary cf-text-sm">{t(card.descriptionKey)}</p>
                <Link
                  className="cf-btn cf-btn--secondary cf-btn--sm"
                  to={card.to}
                  style={{ alignSelf: 'flex-start' }}
                >
                  {t('home.open')}
                </Link>
              </div>
            </Card>
          );
        })}
      </div>
    </div>
  );
}
