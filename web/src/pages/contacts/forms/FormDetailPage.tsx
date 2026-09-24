import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { contactsApi } from '@/api/contacts';
import { formsApi, formsMeta, type FormStats, type SubscriptionForm } from '@/api/forms';
import { PERMISSIONS } from '@/access/permissions';
import { useAccess } from '@/access/useAccess';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  CopyButton,
  DescriptionList,
  ErrorState,
  PageHeader,
  Select,
  Skeleton,
  useToast,
} from '@/design/components';
import { IconEdit, IconTrash } from '@/design/icons';
import { formatDate, formatDateTime } from '@/lib/format';
import { getLocale, t, tEnum } from '@/i18n';
import { paths } from '@/paths';
import { SeriesChart } from '@/pages/analytics/SeriesChart';
import { iframeSnippet, scriptSnippet, submitBodyExample } from './embed';
import { formStatusTone } from './formStatus';
import { SubscriptionFormModal } from './SubscriptionFormModal';
import './forms.css';

type Dialog = 'edit' | 'delete' | null;

/** Periodos que se ofrecen, recortados al tope max_stats_days del servicio. */
const STATS_DAYS = [7, 30, 90, 365] as const;
const DEFAULT_STATS_DAYS = 30;

const COUNTERS = [
  'submitted',
  'confirmation_sent',
  'already_subscribed',
  'not_reachable',
  'confirmed',
] as const;

const backToForms = `${paths.contacts}?tab=forms`;

/** Un dia AAAA-MM-DD se muestra tal cual, sin moverlo de zona. */
function dayOrInstant(value: string): string {
  return formatDate(/^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T12:00:00Z` : value);
}

export default function FormDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAccess();
  const [dialog, setDialog] = useState<Dialog>(null);
  const form = useQuery(() => formsApi.get(id), [id]);

  if (form.error) {
    return (
      <div>
        <PageHeader
          title={t('contacts.forms.title')}
          back={{ to: backToForms, label: t('contacts.forms.title') }}
        />
        <Card>
          <ErrorState
            error={form.error}
            title={t('contacts.forms.notFound')}
            onRetry={form.reload}
          />
        </Card>
      </div>
    );
  }
  if (!form.data) {
    return (
      <Card>
        <Skeleton lines={6} />
      </Card>
    );
  }

  const f = form.data;
  const close = () => setDialog(null);

  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <PageHeader
        title={f.name}
        description={
          <Badge tone={formStatusTone(f.status)}>{tEnum('contacts.forms.status', f.status)}</Badge>
        }
        back={{ to: backToForms, label: t('contacts.forms.title') }}
        actions={
          <>
            {can(...PERMISSIONS.subscriptionForms.update) ? (
              <Button icon={<IconEdit size={16} />} onClick={() => setDialog('edit')}>
                {t('common.edit')}
              </Button>
            ) : null}
            {can(...PERMISSIONS.subscriptionForms.delete) ? (
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
      {f.status === 'disabled' ? (
        <Alert tone="warning">{t('contacts.forms.disabledNotice')}</Alert>
      ) : null}
      <FormSummary form={f} />
      <Card title={t('contacts.forms.preview')} description={t('contacts.forms.previewHint')}>
        <FormPreview form={f} />
      </Card>
      <EmbedCode form={f} />
      <FormStatsCard formId={f.id} />

      {dialog === 'edit' ? (
        <SubscriptionFormModal
          item={f}
          onClose={close}
          onSaved={() => {
            toast.success(t('contacts.forms.updated'));
            close();
            form.reload();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={dialog === 'delete'}
        title={t('contacts.forms.delete')}
        message={t('contacts.forms.deleteConfirm', { name: f.name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={close}
        onConfirm={async () => {
          await formsApi.remove(f.id);
          toast.success(t('contacts.forms.deleted'));
          navigate(backToForms, { replace: true });
        }}
      />
    </div>
  );
}

function FormSummary({ form }: { form: SubscriptionForm }) {
  const { can } = useAccess();
  const canReadLists = can(...PERMISSIONS.contactLists.read);
  const list = useQuery(
    async () => (canReadLists ? (await contactsApi.getList(form.list_id)).data : null),
    [form.list_id, canReadLists],
  );
  return (
    <Card title={t('contacts.forms.summary')}>
      <DescriptionList
        items={[
          {
            label: t('contacts.forms.list'),
            value: list.data?.name ?? <span className="cf-mono">{form.list_id}</span>,
          },
          {
            label: t('contacts.forms.fields'),
            value: (
              <span className="cf-inline-list">
                {form.fields.map((field) => (
                  <Badge key={field.key} tone={field.required ? 'accent' : 'neutral'}>
                    <span className="cf-mono">{field.key}</span>
                  </Badge>
                ))}
              </span>
            ),
          },
          {
            label: t('contacts.forms.afterSubmit'),
            value: form.redirect_url ? (
              <span className="cf-mono cf-break">{form.redirect_url}</span>
            ) : (
              form.texts.success_message || t('common.dash')
            ),
          },
          {
            label: t('contacts.forms.allowedOrigins'),
            value: form.allowed_origins.length ? (
              <span className="cf-inline-list">
                {form.allowed_origins.map((origin) => (
                  <Badge key={origin}>
                    <span className="cf-mono">{origin}</span>
                  </Badge>
                ))}
              </span>
            ) : (
              t('contacts.forms.noOrigins')
            ),
          },
          {
            label: t('contacts.forms.doubleOptInLabel'),
            value: t('contacts.forms.doubleOptInAlways'),
          },
          { label: t('common.updatedAt'), value: formatDateTime(form.updated_at) },
        ]}
      />
    </Card>
  );
}

/**
 * El formulario servido por la plataforma, como lo vera quien lo rellene. Sin allow-scripts:
 * el marco no ejecuta codigo junto a la sesion de la aplicacion.
 */
export function FormPreview({ form }: { form: SubscriptionForm }) {
  return (
    <iframe
      className="cf-embed-frame"
      src={form.embed.iframe_url}
      title={t('contacts.forms.previewTitle', { name: form.name })}
      sandbox="allow-forms allow-same-origin"
      loading="lazy"
      referrerPolicy="no-referrer"
    />
  );
}

function CodeBlock({ label, value }: { label: string; value: string }) {
  return (
    <div className="cf-embed-code">
      <div className="cf-embed-code__head">
        <span className="cf-field__label">{label}</span>
        <CopyButton value={value} withText />
      </div>
      <pre className="cf-pre">{value}</pre>
    </div>
  );
}

export function EmbedCode({ form }: { form: SubscriptionForm }) {
  const { embed } = form;
  const title = form.texts.title.trim() || form.name;
  return (
    <Card title={t('contacts.forms.embed')} description={t('contacts.forms.embedHint')}>
      <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
        {form.allowed_origins.length === 0 ? (
          <Alert tone="info">{t('contacts.forms.embedNoOrigins')}</Alert>
        ) : null}
        <CodeBlock label={t('contacts.forms.embedScript')} value={scriptSnippet(embed)} />
        <CodeBlock label={t('contacts.forms.embedIframe')} value={iframeSnippet(embed, title)} />
        <div className="cf-form__section">{t('contacts.forms.integration')}</div>
        <span className="cf-text-sm cf-text-secondary">{t('contacts.forms.integrationHint')}</span>
        <CodeBlock label={t('contacts.forms.definitionUrl')} value={embed.definition_url} />
        <CodeBlock label={t('contacts.forms.submitUrl')} value={embed.submit_url} />
        <CodeBlock
          label={t('contacts.forms.submitBody')}
          value={submitBodyExample(form.fields, t('contacts.forms.tokenPlaceholder'))}
        />
      </div>
    </Card>
  );
}

function FormStatsCard({ formId }: { formId: string }) {
  const [days, setDays] = useState<number>(DEFAULT_STATS_DAYS);
  const meta = useResource(formsMeta);
  const maxDays = meta.data?.limits.max_stats_days;
  const periods = maxDays ? STATS_DAYS.filter((n) => n <= maxDays) : [DEFAULT_STATS_DAYS];
  const stats = useQuery(() => formsApi.stats(formId, days), [formId, days]);
  return (
    <Card
      title={t('contacts.forms.stats')}
      actions={
        <Select
          aria-label={t('contacts.forms.statsPeriod')}
          options={periods.map((n) => ({
            value: String(n),
            label: t('contacts.forms.lastDays', { n }),
          }))}
          value={String(days)}
          onChange={(e) => setDays(Number(e.target.value))}
        />
      }
    >
      {stats.error ? (
        <ErrorState error={stats.error} onRetry={stats.reload} />
      ) : !stats.data ? (
        <Skeleton lines={4} />
      ) : (
        <FormStatsView stats={stats.data} />
      )}
    </Card>
  );
}

export function FormStatsView({ stats }: { stats: FormStats }) {
  const format = new Intl.NumberFormat(getLocale());
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-4)' }}>
      <span className="cf-text-sm cf-text-secondary">
        {t('contacts.forms.statsRange', {
          from: dayOrInstant(stats.from),
          to: dayOrInstant(stats.to),
        })}
      </span>
      <div className="cf-kpis">
        {COUNTERS.map((key) => (
          <div key={key} className="cf-kpi">
            <span className="cf-kpi__label">{tEnum('contacts.forms.counter', key)}</span>
            <span className="cf-kpi__value">{format.format(stats[key])}</span>
          </div>
        ))}
      </div>
      {stats.daily.length > 0 ? (
        <SeriesChart
          title={t('contacts.forms.statsDaily')}
          days={stats.daily.map((d) => d.date)}
          series={[
            {
              key: 'submitted',
              label: tEnum('contacts.forms.counter', 'submitted'),
              values: stats.daily.map((d) => d.submitted),
            },
            {
              key: 'confirmed',
              label: tEnum('contacts.forms.counter', 'confirmed'),
              values: stats.daily.map((d) => d.confirmed),
            },
          ]}
        />
      ) : (
        <span className="cf-text-muted">{t('contacts.forms.statsEmpty')}</span>
      )}
    </div>
  );
}
