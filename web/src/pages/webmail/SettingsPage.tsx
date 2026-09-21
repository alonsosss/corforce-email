import { Link } from 'react-router-dom';
import { webmailApi } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Card, ErrorState, PageHeader, Skeleton } from '@/design/components';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { VacationForm } from '@/pages/shared/VacationForm';

/** Ajustes del buzon con el que se abrio la sesion: por ahora, su respuesta automatica. */
export default function SettingsPage() {
  const vacation = useQuery((signal) => webmailApi.vacation(signal), []);

  return (
    <div className="cf-wm-settings">
      <PageHeader
        title={t('webmail.settings.title')}
        back={{ to: paths.webmail, label: t('webmail.settings.back') }}
      />
      {vacation.error ? (
        <Card title={t('vacation.title')}>
          <ErrorState
            error={vacation.error}
            title={t('webmail.settings.unavailable')}
            onRetry={vacation.reload}
          />
        </Card>
      ) : !vacation.data ? (
        <Card title={t('vacation.title')}>
          <Skeleton lines={8} />
        </Card>
      ) : (
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
      )}
      <p className="cf-text-sm cf-text-secondary">
        <Link to={paths.webmail}>{t('webmail.settings.back')}</Link>
      </p>
    </div>
  );
}
