import { mailDirectoryApi, type Mailbox } from '@/api/mailDirectory';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import { Card, ErrorState, Skeleton } from '@/design/components';
import { t } from '@/i18n';
import { VacationForm } from '@/pages/shared/VacationForm';

/** Respuesta automatica del buzon, editada por quien administra la empresa (permisos de sieve). */
export function VacationTab({ mailbox }: { mailbox: Mailbox }) {
  const { can } = useAccess();
  const vacation = useQuery(
    async () => (await mailDirectoryApi.getVacation(mailbox.id)).data,
    [mailbox.id],
  );

  if (vacation.error) {
    return (
      <Card title={t('vacation.title')}>
        <ErrorState error={vacation.error} onRetry={vacation.reload} />
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
      editable={can(...PERMISSIONS.sieve.update)}
      onSave={async (input) => {
        const { data } = await mailDirectoryApi.putVacation(mailbox.id, input);
        vacation.setData(data);
        return data;
      }}
    />
  );
}
