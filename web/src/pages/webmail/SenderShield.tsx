import { useState } from 'react';
import { FOLDER_ROLES, webmailApi, type MailAddress, type WebmailFolder } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { Alert, Badge, Button, ConfirmDialog, useToast } from '@/design/components';
import { IconBan, IconLink, IconMail, IconUser } from '@/design/icons';
import { t } from '@/i18n';
import { SenderPanel } from './SenderPanel';
import { isWarning, reasonText, safeUnsubscribeURL, warningReasons } from './shield';

// En lo que escribe el propio buzon no hay remitente que juzgar ni boletin del que darse de baja.
const OWN_ROLES: readonly string[] = [
  FOLDER_ROLES.sent,
  FOLDER_ROLES.drafts,
  FOLDER_ROLES.scheduled,
];

export interface SenderShieldProps {
  folderName: string;
  role: string;
  uid: number;
  folders: readonly WebmailFolder[];
  /** Remitente del mensaje ya leido: la ficha se abre aunque el escudo no responda. */
  sender: MailAddress | undefined;
  /** Mueve el mensaje a Spam como fraude; sin el (ya esta en Spam o no hay Spam) no se ofrece. */
  onReportFraud?: () => void;
  reporting?: boolean;
}

/**
 * Escudo antifraude, ficha del remitente y baja del boletin de un mensaje recibido. Lo calcula el
 * servicio (GET /sender-insight); si no responde, el mensaje se lee igual y solo queda la ficha.
 */
export function SenderShield(props: SenderShieldProps) {
  if (OWN_ROLES.includes(props.role)) return null;
  return <SenderShieldBody {...props} />;
}

function SenderShieldBody({
  folderName,
  uid,
  folders,
  sender,
  onReportFraud,
  reporting = false,
}: SenderShieldProps) {
  const toast = useToast();
  const [panel, setPanel] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const insight = useQuery(
    (signal) => webmailApi.senderInsight(folderName, uid, signal),
    [folderName, uid],
  );
  const data = insight.data;
  const shield = data?.shield;
  const unsubscribe = data?.unsubscribe;
  const warning = shield && isWarning(shield.level) ? shield.level : null;
  const webURL = unsubscribe?.method === 'web' ? safeUnsubscribeURL(unsubscribe.url) : null;
  const canUnsubscribe = unsubscribe?.method === 'one_click' || unsubscribe?.method === 'mailto';
  const senderDomain = sender?.email.split('@')[1] ?? '';

  return (
    <section className="cf-wm-shield" aria-label={t('webmail.shield.label')}>
      {warning ? (
        <Alert
          tone={warning === 'danger' ? 'danger' : 'warning'}
          title={t(
            warning === 'danger' ? 'webmail.shield.dangerTitle' : 'webmail.shield.cautionTitle',
          )}
        >
          <ul className="cf-wm-shield__reasons">
            {warningReasons(shield?.reasons ?? []).map((reason) => (
              <li key={`${reason.code}-${Object.values(reason.params).join('|')}`}>
                {reasonText(reason)}
              </li>
            ))}
          </ul>
          <p className="cf-wm-shield__advice">{t('webmail.shield.advice')}</p>
          {onReportFraud ? (
            <Button
              size="sm"
              variant="danger"
              icon={<IconBan size={14} />}
              loading={reporting}
              onClick={onReportFraud}
            >
              {t('webmail.shield.reportFraud')}
            </Button>
          ) : null}
        </Alert>
      ) : null}
      <div className="cf-wm-shield__bar">
        {shield?.external ? (
          <span title={t('webmail.shield.externalHint', { domain: senderDomain })}>
            <Badge tone="warning">{t('webmail.shield.external')}</Badge>
          </span>
        ) : null}
        {sender ? (
          <Button
            size="sm"
            variant="ghost"
            icon={<IconUser size={14} />}
            aria-expanded={panel}
            onClick={() => setPanel(true)}
          >
            {t('webmail.sender.open')}
          </Button>
        ) : null}
        {canUnsubscribe ? (
          <Button
            size="sm"
            variant="ghost"
            icon={<IconMail size={14} />}
            onClick={() => setConfirming(true)}
          >
            {t('webmail.unsubscribe.action')}
          </Button>
        ) : null}
        {webURL ? (
          <a
            className="cf-btn cf-btn--ghost cf-btn--sm"
            href={webURL}
            target="_blank"
            rel="noopener noreferrer nofollow"
            referrerPolicy="no-referrer"
          >
            <IconLink size={14} />
            {t('webmail.unsubscribe.openPage', { target: unsubscribe?.target ?? '' })}
          </a>
        ) : null}
        {shield?.partial ? (
          <span className="cf-field__hint">{t('webmail.shield.partial')}</span>
        ) : null}
      </div>
      {panel && sender ? (
        <SenderPanel
          sender={sender}
          internal={shield ? !shield.external : null}
          folderName={folderName}
          uid={uid}
          folders={folders}
          onClose={() => setPanel(false)}
        />
      ) : null}
      <ConfirmDialog
        open={confirming}
        title={t('webmail.unsubscribe.title')}
        message={t(
          unsubscribe?.method === 'mailto'
            ? 'webmail.unsubscribe.confirmMailto'
            : 'webmail.unsubscribe.confirmOneClick',
          { target: unsubscribe?.target ?? '' },
        )}
        confirmLabel={t('webmail.unsubscribe.action')}
        onCancel={() => setConfirming(false)}
        onConfirm={async () => {
          const result = await webmailApi.unsubscribe(folderName, uid);
          toast.success(t('webmail.unsubscribe.done', { target: result.target }));
          setConfirming(false);
        }}
      />
    </section>
  );
}
