import { mailDirectoryApi, type Mailbox } from '@/api/mailDirectory';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, ErrorState, Meter, Skeleton } from '@/design/components';
import { IconRefresh } from '@/design/icons';
import { formatBytes, formatQuota, usageRatio } from '@/lib/quota';
import { getLocale, t } from '@/i18n';

export function MailboxQuotaTab({ mailbox }: { mailbox: Mailbox }) {
  const usage = useQuery(
    async () => (await mailDirectoryApi.mailboxQuota(mailbox.id)).data,
    [mailbox.id],
  );

  const refresh = (
    <Button
      size="sm"
      icon={<IconRefresh size={14} />}
      onClick={usage.reload}
      loading={usage.loading}
    >
      {t('common.refresh')}
    </Button>
  );

  if (usage.error) {
    return (
      <Card title={t('mailboxes.quota.title')} actions={refresh}>
        <ErrorState error={usage.error} onRetry={usage.reload} />
      </Card>
    );
  }
  if (!usage.data) {
    return (
      <Card title={t('mailboxes.quota.title')}>
        <Skeleton lines={3} />
      </Card>
    );
  }

  const u = usage.data;
  const ratio = usageRatio(u.used_bytes, u.quota_bytes);
  const percent =
    ratio === null
      ? null
      : new Intl.NumberFormat(getLocale(), { style: 'percent', maximumFractionDigits: 1 }).format(
          ratio,
        );

  return (
    <Card
      title={t('mailboxes.quota.title')}
      description={t('mailboxes.quota.description')}
      actions={refresh}
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <div className="cf-grid-cards">
          <div>
            <div className="cf-stat__label">{t('mailboxes.quota.used')}</div>
            <div className="cf-stat__value">{formatBytes(u.used_bytes)}</div>
          </div>
          <div>
            <div className="cf-stat__label">{t('mailboxes.quota.limit')}</div>
            <div className="cf-stat__value">{formatQuota(u.quota_bytes)}</div>
          </div>
          <div>
            <div className="cf-stat__label">{t('mailboxes.quota.messages')}</div>
            <div className="cf-stat__value">
              {new Intl.NumberFormat(getLocale()).format(u.messages)}
            </div>
          </div>
        </div>
        {ratio !== null && percent !== null ? (
          <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
            <Meter ratio={ratio} label={t('mailboxes.quota.meterLabel')} />
            <span className="cf-text-sm cf-text-secondary">
              {t('mailboxes.quota.usedOf', {
                percent,
                used: formatBytes(u.used_bytes),
                limit: formatBytes(u.quota_bytes),
              })}
            </span>
          </div>
        ) : (
          <span className="cf-text-sm cf-text-secondary">{t('mailboxes.quota.unlimitedHint')}</span>
        )}
      </div>
    </Card>
  );
}
