import { useSearchParams } from 'react-router-dom';
import { webmailApi } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Card, ErrorState, PageHeader, Skeleton, Tabs } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { VacationForm } from '@/pages/shared/VacationForm';
import { FiltersSettings } from './settings/FiltersSettings';
import { LargeFilesSettings } from './settings/LargeFilesSettings';
import { PasswordSettings } from './settings/PasswordSettings';
import { QuickRepliesSettings } from './settings/QuickRepliesSettings';
import { SignatureSettings } from './settings/SignatureSettings';

const TABS = [
  'vacation',
  'signature',
  'quickReplies',
  'rules',
  'forwarding',
  'password',
  'files',
] as const;
type SettingsTab = (typeof TABS)[number];

const TAB_LABELS = {
  vacation: 'webmail.settings.tab.vacation',
  signature: 'webmail.settings.tab.signature',
  quickReplies: 'webmail.settings.tab.quickReplies',
  rules: 'webmail.settings.tab.rules',
  forwarding: 'webmail.settings.tab.forwarding',
  password: 'webmail.settings.tab.password',
  files: 'webmail.settings.tab.files',
} as const;

function parseTab(raw: string | null): SettingsTab {
  return TABS.find((tab) => tab === raw) ?? 'vacation';
}

/** Ajustes del buzon con el que se abrio la sesion. La pestana viaja en la URL. */
export default function SettingsPage() {
  const [params, setParams] = useSearchParams();
  const tab = parseTab(params.get('tab'));

  return (
    <div className="cf-wm-settings">
      <PageHeader
        title={t('webmail.settings.title')}
        back={{ to: paths.webmail, label: t('webmail.settings.back') }}
      />
      <Tabs
        label={t('webmail.settings.title')}
        items={TABS.map((id) => ({ id, label: t(TAB_LABELS[id]) }))}
        value={tab}
        onChange={(next) => setParams({ tab: next }, { replace: true })}
      />
      <div role="tabpanel" aria-label={t(TAB_LABELS[tab])} className="cf-wm-settings__panel">
        {tab === 'vacation' ? <VacationSettings /> : null}
        {tab === 'signature' ? <SignatureSettings /> : null}
        {tab === 'quickReplies' ? <QuickRepliesSettings /> : null}
        {tab === 'rules' ? <FiltersSettings part="rules" /> : null}
        {tab === 'forwarding' ? <FiltersSettings part="forwarding" /> : null}
        {tab === 'password' ? <PasswordSettings /> : null}
        {tab === 'files' ? <LargeFilesSettings /> : null}
      </div>
    </div>
  );
}

function VacationSettings() {
  const vacation = useQuery((signal) => webmailApi.vacation(signal), []);

  if (vacation.error) {
    return (
      <Card title={t('vacation.title')}>
        <ErrorState
          error={vacation.error}
          title={t('webmail.settings.unavailable')}
          onRetry={vacation.reload}
        />
      </Card>
    );
  }
  if (!vacation.data) {
    return (
      <Card title={t('vacation.title')}>
        <Skeleton lines={8} />
      </Card>
    );
  }
  return (
    <VacationForm
      key={vacation.data.updated_at ?? 'nueva'}
      initial={vacation.data}
      editable
      onSave={async (input) => {
        const saved = await webmailApi.setVacation(input);
        vacation.setData(saved);
        return saved;
      }}
    />
  );
}
