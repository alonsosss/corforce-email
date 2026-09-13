import { useState } from 'react';
import { identityApi } from '@/api/identity';
import { useAccess } from '@/access/useAccess';
import { PERMISSIONS } from '@/access/permissions';
import { useAuth } from '@/auth/useAuth';
import { usePagination } from '@/hooks/usePagination';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, Checkbox, ConfirmDialog, useToast } from '@/design/components';
import { SessionsTable } from '@/pages/shared/SessionsTable';
import { t } from '@/i18n';

export function MySessions() {
  const { endSession } = useAuth();
  const { can } = useAccess();
  const toast = useToast();
  const pager = usePagination();
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [confirmAll, setConfirmAll] = useState(false);

  const sessions = useQuery(
    () =>
      identityApi.mySessions({
        page: pager.page,
        per_page: pager.perPage,
        active: includeRevoked ? false : undefined,
      }),
    [pager.page, pager.perPage, includeRevoked],
  );

  // DELETE /sessions/{id} lo reserva el servicio al administrador; el resto de usuarios
  // solo puede cerrar todas sus sesiones a la vez.
  const canRevokeOne = can(...PERMISSIONS.sessions.revoke);

  return (
    <Card
      title={t('account.tab.sessions')}
      description={t('account.sessions.description')}
      flush
      actions={
        <>
          <Checkbox
            label={t('session.showRevoked')}
            checked={includeRevoked}
            onChange={(e) => {
              setIncludeRevoked(e.target.checked);
              pager.reset();
            }}
          />
          <Button variant="danger" onClick={() => setConfirmAll(true)}>
            {t('account.sessions.revokeAll')}
          </Button>
        </>
      }
    >
      <SessionsTable
        rows={sessions.data?.items ?? []}
        loading={sessions.loading}
        error={sessions.error}
        onRetry={sessions.reload}
        pagination={{
          page: sessions.data?.page ?? pager.page,
          perPage: pager.perPage,
          total: sessions.data?.total ?? 0,
          totalPages: sessions.data?.totalPages ?? 0,
          onPageChange: pager.setPage,
        }}
        onRevoke={
          canRevokeOne
            ? async (s) => {
                await identityApi.revokeSession(s.id);
                toast.success(t('session.revoked'));
                sessions.reload();
              }
            : undefined
        }
      />
      <ConfirmDialog
        open={confirmAll}
        title={t('account.sessions.revokeAll')}
        message={t('account.sessions.revokeAllConfirm')}
        confirmLabel={t('account.sessions.revokeAll')}
        danger
        onCancel={() => setConfirmAll(false)}
        onConfirm={async () => {
          await identityApi.logoutAll();
          setConfirmAll(false);
          endSession();
        }}
      />
    </Card>
  );
}
