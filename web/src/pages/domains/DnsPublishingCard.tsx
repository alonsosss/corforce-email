import { useState } from 'react';
import {
  DNS_MODE_MANUAL,
  DNS_PROVIDER_CLOUDFLARE,
  domainsApi,
  type DnsAutomation,
  type DnsPublication,
  type DnsPublicationRecord,
  type DnsRecordAction,
  type DnsRecordKind,
  type DomainDetail,
  type DomainWithRecords,
  type PublishDnsResult,
} from '@/api/domains';
import { codeMessage, errorMessage } from '@/api/messages';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useAction } from '@/hooks/useAction';
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  ConfirmDialog,
  DataTable,
  DescriptionList,
  useToast,
  type BadgeTone,
  type Column,
} from '@/design/components';
import { IconRefresh, IconSend } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';

const ACTION_TONE: Record<DnsRecordAction, BadgeTone> = {
  unchanged: 'neutral',
  created: 'success',
  updated: 'success',
  replaced: 'warning',
  conflict: 'danger',
  failed: 'danger',
};

export interface DnsPublishingCardProps {
  domain: DomainDetail;
  /** El dominio cambio de modo: la ficha funde los datos nuevos. */
  onDomainChanged: (next: DomainWithRecords) => void;
  /** Una publicacion termino: la ficha funde el dominio y muestra la verificacion. */
  onPublished: (result: PublishDnsResult) => void;
}

/**
 * Modo de publicacion del DNS del dominio. En manual no cambia nada de lo de siempre; en automatico
 * publica en Cloudflare y muestra los registros del cliente en conflicto, que solo se reemplazan
 * marcandolos y confirmando.
 */
export function DnsPublishingCard({ domain, onDomainChanged, onPublished }: DnsPublishingCardProps) {
  const { can } = useAccess();
  const toast = useToast();
  const [publication, setPublication] = useState<DnsPublication | null>(null);
  const [replace, setReplace] = useState<DnsRecordKind[]>([]);
  const [confirmManual, setConfirmManual] = useState(false);
  const canPublish = can(...PERMISSIONS.domains.publishDns);
  const automatic = domain.dns_mode !== DNS_MODE_MANUAL;

  const switchToCloudflare = useAction(async () => {
    const { data } = await domainsApi.setDnsMode(domain.id, DNS_PROVIDER_CLOUDFLARE);
    onDomainChanged(data);
    toast.success(t('domains.dns.modeChanged'));
  });

  const publish = useAction(async (kinds: DnsRecordKind[]) => {
    const { data } = await domainsApi.publishDns(domain.id, kinds);
    setPublication(data.dns_publication);
    setReplace([]);
    onPublished(data);
    if (data.dns_publication.complete) toast.success(t('domains.dns.published'));
  });

  const conflicts = publication?.records.filter((r) => r.action === 'conflict') ?? [];
  const actionError = switchToCloudflare.error ?? publish.error;

  return (
    <Card
      title={t('domains.dns.title')}
      description={t('domains.dns.description')}
      actions={
        canPublish ? (
          automatic ? (
            <>
              <Button
                variant="primary"
                icon={<IconSend size={16} />}
                loading={publish.busy}
                onClick={() => void publish.run([])}
              >
                {t('domains.dns.publish')}
              </Button>
              <Button onClick={() => setConfirmManual(true)}>{t('domains.dns.useManual')}</Button>
            </>
          ) : (
            <Button
              icon={<IconRefresh size={16} />}
              loading={switchToCloudflare.busy}
              onClick={() => void switchToCloudflare.run()}
            >
              {t('domains.dns.useCloudflare')}
            </Button>
          )
        ) : null
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        <DescriptionList
          items={[
            {
              label: t('domains.dns.mode'),
              value: (
                <Badge tone={automatic ? 'accent' : 'neutral'}>
                  {tEnum('domains.dnsMode', domain.dns_mode)}
                </Badge>
              ),
            },
            ...(automatic
              ? [{ label: t('domains.dns.publishedAt'), value: formatDateTime(domain.dns_published_at) }]
              : []),
          ]}
        />
        <p className="cf-text-muted">
          {automatic ? t('domains.dns.automaticHint') : t('domains.dns.manualHint')}
        </p>
        {actionError ? (
          <Alert tone="danger">{errorMessage(actionError)}</Alert>
        ) : null}
        {publication ? <PublicationTable publication={publication} /> : null}
        {conflicts.length && canPublish ? (
          <Alert tone="warning" title={t('domains.dns.conflict.title')}>
            <span>{t('domains.dns.conflict.description')}</span>
            <ul className="cf-rules">
              {conflicts.map((r) => (
                <li key={r.record}>
                  <Checkbox
                    label={t('domains.dns.conflict.replace', { record: tEnum('domains.record', r.record) })}
                    checked={replace.includes(r.record)}
                    onChange={(e) =>
                      setReplace((current) =>
                        e.target.checked
                          ? [...current, r.record]
                          : current.filter((kind) => kind !== r.record),
                      )
                    }
                  />
                  {r.existing.map((value) => (
                    <div key={value} className="cf-mono">
                      {t('domains.dns.conflict.existing', { value })}
                    </div>
                  ))}
                </li>
              ))}
            </ul>
            <div>
              <Button
                size="sm"
                variant="danger"
                disabled={!replace.length}
                loading={publish.busy}
                onClick={() => void publish.run(replace)}
              >
                {t('domains.dns.conflict.confirm')}
              </Button>
            </div>
          </Alert>
        ) : null}
      </div>
      <ConfirmDialog
        open={confirmManual}
        title={t('domains.dns.useManual')}
        message={t('domains.dns.useManualConfirm')}
        confirmLabel={t('domains.dns.useManual')}
        onCancel={() => setConfirmManual(false)}
        onConfirm={async () => {
          const { data } = await domainsApi.setDnsMode(domain.id, DNS_MODE_MANUAL);
          setPublication(null);
          setConfirmManual(false);
          onDomainChanged(data);
          toast.success(t('domains.dns.modeChanged'));
        }}
      />
    </Card>
  );
}

function PublicationTable({ publication }: { publication: DnsPublication }) {
  const columns: Column<DnsPublicationRecord>[] = [
    {
      key: 'record',
      header: t('domains.records.column.record'),
      render: (r) => <strong>{tEnum('domains.record', r.record)}</strong>,
    },
    {
      key: 'host',
      header: t('domains.records.column.host'),
      render: (r) => (
        <span className="cf-mono">
          {r.type} {r.host}
        </span>
      ),
    },
    {
      key: 'action',
      header: t('domains.dns.column.action'),
      render: (r) => (
        <div className="cf-cell-stack">
          <Badge tone={ACTION_TONE[r.action]}>{tEnum('domains.dns.action', r.action)}</Badge>
          {r.error_code ? <span>{codeMessage(r.error_code) ?? r.error_code}</span> : null}
        </div>
      ),
    },
  ];
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
      <Alert
        tone={publication.complete ? 'success' : 'warning'}
        title={t('domains.dns.result.title', { zone: publication.zone })}
      >
        {publication.complete ? t('domains.dns.result.complete') : t('domains.dns.result.incomplete')}
      </Alert>
      <DataTable columns={columns} rows={publication.records} rowKey={(r) => r.record} />
    </div>
  );
}

/** Lo que hizo la publicacion automatica tras rotar o revocar claves DKIM. */
export function DnsAutomationNotice({ automation }: { automation: DnsAutomation }) {
  if (automation.error_code || !automation.publication) {
    const reason = automation.error_code ? codeMessage(automation.error_code) : null;
    return (
      <Alert tone="warning" title={t('domains.dns.automation.failed')}>
        {reason}
      </Alert>
    );
  }
  const { publication } = automation;
  return (
    <Alert tone="success">
      <span>{t('domains.dns.automation.published')}</span>
      {publication.removed.length ? <div>{t('domains.dns.automation.removed')}</div> : null}
      {publication.kept.length ? (
        <>
          <div>{t('domains.dns.automation.kept')}</div>
          <ul className="cf-rules">
            {publication.kept.map((host) => (
              <li key={host} className="cf-mono">
                {host}
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </Alert>
  );
}
