import { useSearchParams } from 'react-router-dom';
import { PageHeader, Tabs } from '@/design/components';
import { t } from '@/i18n';
import { ProfileForm } from './ProfileForm';
import { ChangePasswordForm } from './ChangePasswordForm';
import { MfaSection } from './MfaSection';
import { MySessions } from './MySessions';

const TABS = [
  { id: 'profile', label: t('account.tab.profile') },
  { id: 'password', label: t('account.tab.password') },
  { id: 'mfa', label: t('account.tab.mfa') },
  { id: 'sessions', label: t('account.tab.sessions') },
] as const;

type TabId = (typeof TABS)[number]['id'];

function isTab(value: string | null): value is TabId {
  return TABS.some((tab) => tab.id === value);
}

export default function AccountPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab: TabId = isTab(raw) ? raw : 'profile';

  return (
    <div>
      <PageHeader title={t('account.title')} description={t('account.subtitle')} />
      <Tabs
        items={TABS}
        value={tab}
        onChange={(id) => setParams({ tab: id })}
        label={t('account.title')}
      />
      {tab === 'profile' ? <ProfileForm /> : null}
      {tab === 'password' ? <ChangePasswordForm /> : null}
      {tab === 'mfa' ? <MfaSection /> : null}
      {tab === 'sessions' ? <MySessions /> : null}
    </div>
  );
}
