import { useState } from 'react';
import { mailDirectoryApi } from '@/api/mailDirectory';
import { ERROR_CODES, isApiError } from '@/api/errors';
import { errorMessage } from '@/api/messages';
import type { MtaStsMode } from '@/api/mtaSts';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DescriptionList,
  ErrorState,
  Skeleton,
  useToast,
  type BadgeTone,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';

const MODE_TONE: Record<MtaStsMode, BadgeTone> = {
  none: 'neutral',
  testing: 'warning',
  enforce: 'success',
};

const SECONDS_PER_DAY = 24 * 60 * 60;

export interface MtaStsCardProps {
  /** Nombre del dominio: la politica se pide y se cambia por el nombre, que es la clave en la celda. */
  domain: string;
  /** El modo cambio: la ficha vuelve a pedir los registros, porque el TXT _mta-sts lleva la version nueva. */
  onChanged: () => void;
}

/**
 * Estado de MTA-STS del dominio y el paso entre modos. Se entra por testing y se sale por testing;
 * enforce pide confirmacion porque, con los MX o el certificado mal, otros servidores dejan de
 * entregar el correo de la empresa. Los pasos posibles los da el servicio (allowed_modes).
 */
export function MtaStsCard({ domain, onChanged }: MtaStsCardProps) {
  const { can } = useAccess();
  const toast = useToast();
  const [confirmEnforce, setConfirmEnforce] = useState(false);
  const state = useQuery(async () => (await mailDirectoryApi.getMtaSts(domain)).data, [domain]);
  const canUpdate = can(...PERMISSIONS.mtaSts.update);

  const change = useAction(async (mode: MtaStsMode) => {
    const { data } = await mailDirectoryApi.setMtaSts(domain, mode);
    state.setData(data);
    toast.success(t('domains.mtaSts.changed'));
    onChanged();
  });

  if (state.error) {
    // Un dominio que el directorio de la celda aun no tiene es un dominio sin verificar.
    if (isApiError(state.error) && state.error.code === ERROR_CODES.NOT_FOUND) {
      return (
        <Card title={t('domains.mtaSts.title')} description={t('domains.mtaSts.description')}>
          <Alert tone="info">{t('domains.mtaSts.notInDirectory')}</Alert>
        </Card>
      );
    }
    return (
      <Card title={t('domains.mtaSts.title')}>
        <ErrorState error={state.error} onRetry={state.reload} />
      </Card>
    );
  }
  if (!state.data) {
    return (
      <Card title={t('domains.mtaSts.title')}>
        <Skeleton lines={3} />
      </Card>
    );
  }

  const current = state.data;
  const next = (mode: MtaStsMode) => current.allowed_modes.includes(mode);

  return (
    <Card title={t('domains.mtaSts.title')} description={t('domains.mtaSts.description')}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            {
              label: t('domains.mtaSts.mode'),
              value: (
                <Badge tone={MODE_TONE[current.mode]}>
                  {tEnum('domains.mtaSts.mode', current.mode)}
                </Badge>
              ),
            },
            {
              label: t('domains.mtaSts.policyId'),
              value: current.policy_id ? (
                <span className="cf-mono">{current.policy_id}</span>
              ) : (
                t('common.dash')
              ),
            },
            {
              label: t('domains.mtaSts.maxAge'),
              value: current.max_age
                ? t('domains.mtaSts.maxAgeDays', {
                    days: Math.round(current.max_age / SECONDS_PER_DAY),
                  })
                : t('common.dash'),
            },
            { label: t('domains.mtaSts.updatedAt'), value: formatDateTime(current.updated_at) },
          ]}
        />
        <p className="cf-text-muted cf-text-sm">{tEnum('domains.mtaSts.hint', current.mode)}</p>
        {current.mode !== 'none' ? (
          <p className="cf-text-muted cf-text-sm">{t('domains.mtaSts.policyUrl', { domain })}</p>
        ) : null}
        {current.mode === 'testing' ? (
          <Alert tone="warning">{t('domains.mtaSts.enforceRisk')}</Alert>
        ) : null}
        {change.error ? (
          <Alert tone="danger" title={t('domains.mtaSts.changeFailed')}>
            {errorMessage(change.error)}
          </Alert>
        ) : null}
        {canUpdate ? (
          <div className="cf-inline">
            {next('testing') ? (
              <Button
                variant={current.mode === 'none' ? 'primary' : 'secondary'}
                loading={change.busy}
                onClick={() => void change.run('testing')}
              >
                {current.mode === 'enforce'
                  ? t('domains.mtaSts.backToTesting')
                  : t('domains.mtaSts.activate')}
              </Button>
            ) : null}
            {next('enforce') ? (
              <Button
                variant="danger"
                disabled={change.busy}
                onClick={() => setConfirmEnforce(true)}
              >
                {t('domains.mtaSts.enforce')}
              </Button>
            ) : null}
            {next('none') ? (
              <Button loading={change.busy} onClick={() => void change.run('none')}>
                {t('domains.mtaSts.deactivate')}
              </Button>
            ) : null}
          </div>
        ) : null}
        {canUpdate && current.mode === 'testing' && !next('enforce') && !current.domain_active ? (
          <p className="cf-text-muted cf-text-sm">{t('domains.mtaSts.enforceNeedsActive')}</p>
        ) : null}
      </div>
      <ConfirmDialog
        open={confirmEnforce}
        title={t('domains.mtaSts.enforceTitle')}
        message={t('domains.mtaSts.enforceConfirm', { domain })}
        confirmLabel={t('domains.mtaSts.enforce')}
        danger
        onCancel={() => setConfirmEnforce(false)}
        onConfirm={async () => {
          const { data } = await mailDirectoryApi.setMtaSts(domain, 'enforce');
          state.setData(data);
          toast.success(t('domains.mtaSts.changed'));
          setConfirmEnforce(false);
          onChanged();
        }}
      />
    </Card>
  );
}
