import { useState, type FormEvent } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  DMARC_POLICIES,
  DOMAIN_PURPOSES,
  domainsApi,
  type DkimRotation,
  type DmarcPolicy,
  type DomainDetail,
  type DomainPurpose,
  type DomainWithRecords,
  type RevokeDkimResult,
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
  CopyButton,
  DataTable,
  DescriptionList,
  ErrorState,
  FormField,
  Modal,
  PageHeader,
  Select,
  Skeleton,
  Textarea,
  useToast,
  type Column,
} from '@/design/components';
import { IconEdit, IconKey, IconRefresh, IconShieldOff, IconTrash } from '@/design/icons';
import { formatDateTime } from '@/lib/format';
import { t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { DnsAutomationNotice, DnsPublishingCard } from './DnsPublishingCard';
import { DnsRecordsTable } from './DnsRecordsTable';
import { domainStatusTone, verifyOutcomeTone } from './domainStatus';

type Dialog = 'edit' | 'rotate' | 'revoke' | 'delete' | null;

export default function DomainDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const [verification, setVerification] = useState<VerifyResult | null>(null);
  const [rotation, setRotation] = useState<RotateDkimResult | null>(null);
  const [revocation, setRevocation] = useState<RevokeDkimResult | null>(null);

  const detail = useQuery(async () => (await domainsApi.get(id)).data, [id]);

  const verify = useAction(async () => {
    const { data } = await domainsApi.verify(id);
    detail.setData((current) => (current ? { ...current, ...data } : current));
    setVerification(data);
  });

  // Repite la ultima revocacion con su selector y su motivo: el backend la reconoce, no genera otra
  // clave y solo reintenta lo que falta en los servidores de correo.
  const retryRevocation = useAction(async (last: DkimRotation) => {
    const { data } = await domainsApi.revokeDkim(id, {
      current_selector: last.revoked_selectors[0] ?? '',
      reason: last.reason,
    });
    setRevocation(data);
    detail.reload();
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
  const lastRevocation = d.dkim_rotations[0]?.kind === 'compromised' ? d.dkim_rotations[0] : undefined;

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
            {can(...PERMISSIONS.domains.revokeDkim) ? (
              <Button
                variant="danger"
                icon={<IconShieldOff size={16} />}
                onClick={() => setDialog('revoke')}
              >
                {t('domains.revokeDkim')}
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
      {d.dkim_revocation_pending ? (
        <Alert tone="danger" title={t('domains.revocation.pendingTitle')}>
          <span>{t('domains.revocation.enginesPending')}</span>
          {lastRevocation && can(...PERMISSIONS.domains.revokeDkim) ? (
            <div>
              <Button
                size="sm"
                loading={retryRevocation.busy}
                onClick={() => void retryRevocation.run(lastRevocation)}
              >
                {t('domains.revocation.retry')}
              </Button>
            </div>
          ) : null}
          {retryRevocation.error ? (
            <div role="alert">{errorMessage(retryRevocation.error)}</div>
          ) : null}
        </Alert>
      ) : null}

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
                    {d.dkim_previous_until
                      ? `; ${t('domains.detail.previousUntil', { date: formatDateTime(d.dkim_previous_until) })}`
                      : null}
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

      <DnsPublishingCard
        domain={d}
        onDomainChanged={(next) =>
          detail.setData((current) => (current ? { ...current, ...next } : current))
        }
        onPublished={(result) => {
          detail.setData((current) => (current ? { ...current, ...result } : current));
          setVerification(result.outcome ? { ...result, outcome: result.outcome } : null);
        }}
      />

      <Card flush title={t('domains.records.title')} description={t('domains.records.description')}>
        <DnsRecordsTable records={d.dns_records} checks={d.dns_checks} />
      </Card>

      {d.dkim_rotations.length ? (
        <Card flush title={t('domains.history.title')} description={t('domains.history.description')}>
          <DkimHistoryTable rotations={d.dkim_rotations} />
        </Card>
      ) : null}

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
            {rotation.dns_automation ? (
              <DnsAutomationNotice automation={rotation.dns_automation} />
            ) : null}
            <Alert tone="warning">
              {t('domains.rotation.description', { date: formatDateTime(rotation.grace_until) })}
            </Alert>
            <DnsRecordsTable records={[rotation.dns_record]} />
          </div>
        </Modal>
      ) : null}
      {dialog === 'revoke' ? (
        <DkimRevokeForm
          domain={d}
          onClose={close}
          onRevoked={(result) => {
            setRevocation(result);
            close();
            detail.reload();
          }}
        />
      ) : null}
      {revocation ? (
        <RevocationResult result={revocation} onClose={() => setRevocation(null)} />
      ) : null}
    </div>
  );
}

function DkimHistoryTable({ rotations }: { rotations: DkimRotation[] }) {
  const columns: Column<DkimRotation>[] = [
    {
      key: 'date',
      header: t('domains.history.column.date'),
      render: (r) => formatDateTime(r.rotated_at),
    },
    {
      key: 'kind',
      header: t('domains.history.column.kind'),
      render: (r) => (
        <Badge tone={r.kind === 'compromised' ? 'danger' : 'neutral'}>
          {tEnum('domains.rotationKind', r.kind)}
        </Badge>
      ),
    },
    {
      key: 'selector',
      header: t('domains.history.column.selector'),
      render: (r) => <span className="cf-mono">{r.selector}</span>,
    },
    {
      key: 'retired',
      header: t('domains.history.column.retired'),
      render: (r) => {
        const retired =
          r.kind === 'compromised' ? r.revoked_selectors : r.previous_selector ? [r.previous_selector] : [];
        return retired.length ? <span className="cf-mono">{retired.join(', ')}</span> : t('common.dash');
      },
    },
    {
      key: 'reason',
      header: t('domains.history.column.reason'),
      render: (r) => r.reason || t('common.dash'),
    },
  ];
  return <DataTable columns={columns} rows={rotations} rowKey={(r) => r.id} />;
}

const REVOKE_FORM_ID = 'domain-revoke-form';

// El selector actual viaja con la peticion: si otra persona ya revoco o roto las claves, el backend
// lo detecta en vez de revocar la clave nueva.
function DkimRevokeForm({
  domain,
  onClose,
  onRevoked,
}: {
  domain: DomainDetail;
  onClose: () => void;
  onRevoked: (result: RevokeDkimResult) => void;
}) {
  const [reason, setReason] = useState('');

  const action = useAction(async () => {
    const { data } = await domainsApi.revokeDkim(domain.id, {
      current_selector: domain.dkim_selector,
      reason,
    });
    onRevoked(data);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    await action.run();
  };

  return (
    <Modal
      open
      title={t('domains.revokeDkim')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={action.busy}>
            {t('common.cancel')}
          </Button>
          <Button
            type="submit"
            form={REVOKE_FORM_ID}
            variant="danger"
            loading={action.busy}
            disabled={!reason.trim()}
          >
            {t('domains.revoke.confirm')}
          </Button>
        </>
      }
    >
      <form id={REVOKE_FORM_ID} className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
        <Alert tone="danger">{t('domains.revoke.warning', { domain: domain.domain })}</Alert>
        <FormField
          label={t('domains.revoke.reason')}
          htmlFor="domain-revoke-reason"
          hint={t('domains.revoke.reasonHint')}
          required
        >
          <Textarea
            id="domain-revoke-reason"
            rows={3}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </FormField>
        {action.error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(action.error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}

function RevocationResult({ result, onClose }: { result: RevokeDkimResult; onClose: () => void }) {
  // En modo automatico la plataforma ya retiro sus TXT revocados y publico el nuevo: solo quedan a
  // cargo del cliente los TXT suyos que la notificacion enumera.
  const automated = Boolean(result.dns_automation?.publication && !result.dns_automation.error_code);
  return (
    <Modal
      open
      size="lg"
      title={t('domains.revocation.title')}
      onClose={onClose}
      footer={<Button onClick={onClose}>{t('common.close')}</Button>}
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        {result.dns_automation ? <DnsAutomationNotice automation={result.dns_automation} /> : null}
        {automated ? null : (
        <Alert tone="danger">
          <span>{t('domains.revocation.removeNow')}</span>
          <ul className="cf-rules">
            {result.remove_dns_records.map((r) => (
              <li key={r.host}>
                <span className="cf-mono">
                  {r.type} {r.host}
                </span>{' '}
                <CopyButton value={r.host} label={t('domains.records.copyHost')} />
              </li>
            ))}
          </ul>
        </Alert>
        )}
        {!result.engines_retired ? (
          <Alert tone="warning">
            <span>{t('domains.revocation.enginesPending')}</span>
            {result.integration_errors.length ? (
              <ul className="cf-rules">
                {result.integration_errors.map((message) => (
                  <li key={message}>{message}</li>
                ))}
              </ul>
            ) : null}
          </Alert>
        ) : null}
        {automated ? null : <span>{t('domains.revocation.publishNew')}</span>}
        <DnsRecordsTable records={[result.dns_record]} />
      </div>
    </Modal>
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
