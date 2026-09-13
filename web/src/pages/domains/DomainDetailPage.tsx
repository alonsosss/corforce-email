import { useState, type FormEvent } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  DMARC_POLICIES,
  DOMAIN_PURPOSES,
  domainsApi,
  type DmarcPolicy,
  type DomainDetail,
  type DomainPurpose,
  type DomainWithRecords,
  type RotateDkimResult,
  type UpdateDomainRequest,
  type VerifyResult,
} from '@/api/domains';
import { ERROR_CODES } from '@/api/errors';
import { errorMessage } from '@/api/messages';
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
  FormField,
  Modal,
  PageHeader,
  Select,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconEdit, IconKey, IconRefresh, IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { DnsRecordsTable } from './DnsRecordsTable';
import { domainStatusTone, verifyOutcomeTone } from './domainStatus';

type Dialog = 'edit' | 'rotate' | 'delete' | null;

export default function DomainDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const [verification, setVerification] = useState<VerifyResult | null>(null);
  const [rotation, setRotation] = useState<RotateDkimResult | null>(null);

  const detail = useQuery(async () => (await domainsApi.get(id)).data, [id]);

  const verify = useAction(async () => {
    const { data } = await domainsApi.verify(id);
    detail.setData(data);
    setVerification(data);
  });

  const close = () => setDialog(null);

  if (detail.error) {
    return (
      <div>
        <PageHeader
          title={t('domains.title')}
          back={{ to: paths.domains, label: t('nav.domains') }}
        />
        <Card>
          <ErrorState error={detail.error} title={t('domains.notFound')} onRetry={detail.reload} />
        </Card>
      </div>
    );
  }
  if (!detail.data) {
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  }

  const d = detail.data;

  return (
    <div className="cf-stack">
      <PageHeader
        title={d.domain}
        description={
          <Badge tone={domainStatusTone(d.status)}>{tEnum('domains.status', d.status)}</Badge>
        }
        back={{ to: paths.domains, label: t('nav.domains') }}
        actions={
          <>
            {can(...PERMISSIONS.domains.verify) ? (
              <Button
                variant="primary"
                icon={<IconRefresh size={16} />}
                loading={verify.busy}
                onClick={() => void verify.run()}
              >
                {t('domains.verify')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.domains.rotateDkim) ? (
              <Button icon={<IconKey size={16} />} onClick={() => setDialog('rotate')}>
                {t('domains.rotateDkim')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.domains.update) ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.domains.delete) ? (
              <Button
                variant="danger"
                icon={<IconTrash size={16} />}
                onClick={() => setDialog('delete')}
              >
                {t('common.delete')}
              </Button>
            ) : null}
          </>
        }
      />

      {verify.error ? (
        <Alert tone="danger" title={t('domains.verifyFailed')}>
          {errorMessage(verify.error)}
        </Alert>
      ) : null}
      {verification ? <VerificationSummary result={verification} /> : null}

      <Card>
        <DescriptionList
          items={[
            { label: t('domains.column.purpose'), value: tEnum('domains.purpose', d.purpose) },
            { label: t('domains.column.dmarc'), value: tEnum('domains.dmarc', d.dmarc_policy) },
            { label: t('domains.column.verifiedAt'), value: formatDateTime(d.verified_at) },
            { label: t('domains.column.lastChecked'), value: formatDateTime(d.last_checked_at) },
            {
              label: t('domains.detail.dkimSelector'),
              value: (
                <span className="cf-mono">
                  {d.dkim_selector} ({t('domains.detail.keyBits', { bits: d.dkim_key_bits })})
                </span>
              ),
            },
            {
              label: t('domains.detail.dkimPrevious'),
              value: d.dkim_previous_selector ? (
                <span>
                  <span className="cf-mono">{d.dkim_previous_selector}</span>{' '}
                  <span className="cf-text-muted">
                    {t('domains.detail.rotatedAt', { date: formatDateTime(d.dkim_rotated_at) })}
                  </span>
                </span>
              ) : (
                t('common.dash')
              ),
            },
            { label: t('common.createdAt'), value: formatDateTime(d.created_at) },
            { label: t('common.id'), value: <span className="cf-mono">{d.id}</span> },
          ]}
        />
      </Card>

      <Card flush title={t('domains.records.title')} description={t('domains.records.description')}>
        <DnsRecordsTable records={d.dns_records} checks={d.dns_checks} />
      </Card>

      {dialog === 'edit' ? (
        <DomainEditForm
          domain={d}
          onClose={close}
          onSaved={(next) => {
            detail.setData((current) => (current ? { ...current, ...next } : current));
            toast.success(t('domains.updated'));
            close();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'rotate'}
        title={t('domains.rotateDkim')}
        message={t('domains.rotateConfirm', { domain: d.domain })}
        confirmLabel={t('domains.rotateDkim')}
        onCancel={close}
        onConfirm={async () => {
          const { data } = await domainsApi.rotateDkim(d.id);
          setRotation(data);
          close();
          detail.reload();
        }}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('domains.delete')}
        message={t('domains.deleteConfirm', { domain: d.domain })}
        confirmLabel={t('common.delete')}
        danger
        errorOverrides={{ [ERROR_CODES.CONFLICT]: 'domains.deleteHasMailboxes' }}
        onCancel={close}
        onConfirm={async () => {
          await domainsApi.remove(d.id);
          toast.success(t('domains.deleted'));
          navigate(paths.domains, { replace: true });
        }}
      />
      {rotation ? (
        <Modal
          open
          size="lg"
          title={t('domains.rotation.title')}
          onClose={() => setRotation(null)}
          footer={<Button onClick={() => setRotation(null)}>{t('common.close')}</Button>}
        >
          <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
            <Alert tone="warning">
              {t('domains.rotation.description', { date: formatDateTime(rotation.grace_until) })}
            </Alert>
            <DnsRecordsTable records={[rotation.dns_record]} />
          </div>
        </Modal>
      ) : null}
    </div>
  );
}

function VerificationSummary({ result }: { result: VerifyResult }) {
  return (
    <Alert
      tone={verifyOutcomeTone(result.outcome)}
      title={tEnum('domains.outcome', result.outcome)}
    >
      <span>{tEnum('domains.outcomeHint', result.outcome)}</span>
      {result.integration_errors.length ? (
        <ul className="cf-rules">
          {result.integration_errors.map((message) => (
            <li key={message}>{message}</li>
          ))}
        </ul>
      ) : null}
    </Alert>
  );
}

const EDIT_FORM_ID = 'domain-edit-form';

function DomainEditForm({
  domain,
  onClose,
  onSaved,
}: {
  domain: DomainDetail;
  onClose: () => void;
  onSaved: (next: DomainWithRecords) => void;
}) {
  const [purpose, setPurpose] = useState<DomainPurpose>(domain.purpose);
  const [dmarc, setDmarc] = useState<DmarcPolicy>(domain.dmarc_policy);

  const action = useAction(async () => {
    const body: UpdateDomainRequest = {
      purpose: purpose !== domain.purpose ? purpose : undefined,
      dmarc_policy: dmarc !== domain.dmarc_policy ? dmarc : undefined,
    };
    if (body.purpose === undefined && body.dmarc_policy === undefined) {
      onClose();
      return;
    }
    const { data } = await domainsApi.update(domain.id, body);
    onSaved(data);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    await action.run();
  };

  return (
    <Modal
      open
      title={t('domains.form.editTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={EDIT_FORM_ID} variant="primary" loading={action.busy}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <form id={EDIT_FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <FormField
          label={t('domains.form.purpose')}
          htmlFor="domain-edit-purpose"
          hint={tEnum('domains.purposeHint', purpose)}
        >
          <Select
            id="domain-edit-purpose"
            options={DOMAIN_PURPOSES.map((p) => ({ value: p, label: tEnum('domains.purpose', p) }))}
            value={purpose}
            onChange={(e) => setPurpose(e.target.value as DomainPurpose)}
          />
        </FormField>
        <FormField
          label={t('domains.form.dmarc')}
          htmlFor="domain-edit-dmarc"
          hint={tEnum('domains.dmarcHint', dmarc)}
        >
          <Select
            id="domain-edit-dmarc"
            options={DMARC_POLICIES.map((p) => ({ value: p, label: tEnum('domains.dmarc', p) }))}
            value={dmarc}
            onChange={(e) => setDmarc(e.target.value as DmarcPolicy)}
          />
        </FormField>
        {purpose !== domain.purpose ? (
          <Alert tone="warning">{t('domains.form.purposeChange')}</Alert>
        ) : null}
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
