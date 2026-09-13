import { organizationApi } from '@/api/organization';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, PageHeader, useToast } from '@/design/components';
import { IconLayers } from '@/design/icons';
import { errorMessage } from '@/api/messages';
import { t } from '@/i18n';
import { MigrationsTable } from './MigrationsTable';

export default function MigrationsPage() {
  const toast = useToast();
  const { can } = useAccess();
  const status = useQuery(async () => (await organizationApi.migrationsStatus()).data.tenants, []);

  const runAll = useAction(async () => {
    const { data } = await organizationApi.migrateAll();
    toast.success(
      t('orgs.migrations.ran', {
        migrated: data.migrated,
        failed: data.failed,
        locked: data.locked,
      }),
    );
    status.reload();
  });

  const canRun = can(...PERMISSIONS.migrations.run);

  return (
    <div>
      <PageHeader
        title={t('orgs.migrations.title')}
        description={t('orgs.migrations.description')}
        actions={
          canRun ? (
            <Button
              variant="primary"
              icon={<IconLayers size={16} />}
              loading={runAll.busy}
              onClick={() => {
                void runAll.run().then((ok) => {
                  if (!ok) toast.error(errorMessage(runAll.error));
                });
              }}
            >
              {t('orgs.migrations.runAll')}
            </Button>
          ) : null
        }
      />
      <Card flush>
        <MigrationsTable
          rows={status.data ?? []}
          loading={status.loading}
          error={status.error}
          onRetry={status.reload}
          onMigrated={status.reload}
          canRun={canRun}
        />
      </Card>
    </div>
  );
}
