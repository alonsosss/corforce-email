import { useState } from 'react';
import { auditApi, type ChainIntegrity } from '@/api/audit';
import { errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import { Button, Card, DescriptionList, EmptyState, PageHeader } from '@/design/components';
import { IconAlertTriangle, IconCheckCircle, IconLink } from '@/design/icons';
import { t } from '@/i18n';

export default function IntegrityPage() {
  const { can } = useAccess();
  const [result, setResult] = useState<ChainIntegrity | null>(null);
  const verify = useAction(async () => {
    const { data } = await auditApi.integrity();
    setResult(data);
  });

  const canVerify = can(...PERMISSIONS.integrity.verify);

  return (
    <div>
      <PageHeader
        title={t('audit.integrity.title')}
        description={t('audit.integrity.subtitle')}
        actions={
          canVerify ? (
            <Button
              variant="primary"
              icon={<IconLink size={16} />}
              loading={verify.busy}
              onClick={() => void verify.run()}
            >
              {t('audit.integrity.verify')}
            </Button>
          ) : null
        }
      />
      <Card>
        {verify.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(verify.error)}
          </div>
        ) : result === null ? (
          <EmptyState
            icon={<IconLink size={32} />}
            title={canVerify ? t('audit.integrity.idle') : t('audit.integrity.noVerify')}
          />
        ) : result.ok ? (
          <EmptyState
            icon={
              <span style={{ color: 'var(--cf-success)' }}>
                <IconCheckCircle size={36} />
              </span>
            }
            title={t('audit.integrity.ok')}
            description={
              <>
                <p>{t('audit.integrity.okDescription')}</p>
                <p>{t('audit.integrity.checked', { n: result.checked })}</p>
              </>
            }
          />
        ) : (
          <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
            <EmptyState
              icon={
                <span style={{ color: 'var(--cf-danger)' }}>
                  <IconAlertTriangle size={36} />
                </span>
              }
              title={t('audit.integrity.broken')}
              description={t('audit.integrity.brokenDescription')}
            />
            <DescriptionList
              items={[
                { label: t('audit.integrity.checked', { n: result.checked }), value: '' },
                {
                  label: t('audit.integrity.brokenId'),
                  value: <span className="cf-mono">{result.broken_id ?? t('common.dash')}</span>,
                },
              ]}
            />
          </div>
        )}
      </Card>
    </div>
  );
}
